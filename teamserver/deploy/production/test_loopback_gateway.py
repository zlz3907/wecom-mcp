"""Controlled local receipt-consumer regression; no production listener is used."""
import http.client
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import importlib.util
from pathlib import Path
import select
import socket
import struct
import threading
import time
import unittest


HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location('gateway', HERE / 'wecom-mcp-loopback-gateway.py')
g = importlib.util.module_from_spec(spec)
spec.loader.exec_module(g)


class Backend(BaseHTTPRequestHandler):
    protocol_version = 'HTTP/1.1'

    def log_message(self, format, *args):
        return

    def do_GET(self):
        if self.path == '/events':
            self.send_response(200)
            self.send_header('Content-Type', 'text/event-stream')
            self.send_header('Cache-Control', 'no-store')
            self.send_header('Connection', 'close')
            self.end_headers()
            self.wfile.write(b'data: first\n\n'); self.wfile.flush()
            self.server.first_event.set()
            time.sleep(self.server.heartbeat_delay)
            self.wfile.write(b': heartbeat\n\n'); self.wfile.flush()
            self.server.heartbeat_event.set()
            self.server.stream_finish.wait(2)
            self.close_connection = True
            return
        if self.path == '/disconnect':
            self.send_response(200)
            self.send_header('Content-Type', 'text/event-stream')
            self.send_header('Connection', 'close')
            self.end_headers()
            self.wfile.write(b'data: first\n\n'); self.wfile.flush()
            self.server.first_event.set()
            self.server.disconnect_release.wait(2)
            try:
                for _ in range(32):
                    self.wfile.write(b'x' * 65536); self.wfile.flush()
                    time.sleep(0.005)
            except (BrokenPipeError, ConnectionResetError):
                self.server.upstream_closed.set()
            self.close_connection = True
            return
        if self.path == '/idle-events':
            self.send_response(200)
            self.send_header('Content-Type', 'text/event-stream')
            self.send_header('Cache-Control', 'no-store')
            self.send_header('Connection', 'close')
            self.end_headers()
            self.wfile.write(b'data: first\n\n'); self.wfile.flush()
            self.server.first_event.set()
            # Regression boundary: the old gateway retained a 15-second
            # socket timeout after headers and dropped this follow-up event.
            time.sleep(15.2)
            self.wfile.write(b'data: after-idle\n\n'); self.wfile.flush()
            self.close_connection = True
            return
        if self.path == '/late-headers':
            # Regression boundary: a tool can need more than the historical
            # 15s before producing *any* response headers/body.
            time.sleep(15.2)
            payload = b'late-first-packet'
            self.send_response(200)
            self.send_header('Content-Length', str(len(payload)))
            self.send_header('Connection', 'close')
            self.end_headers()
            self.wfile.write(payload); self.wfile.flush()
            self.close_connection = True
            return
        if self.path == '/late-headers-cancel':
            self.server.header_wait_started.set()
            deadline = time.monotonic() + 3
            while time.monotonic() < deadline:
                readable, _, _ = select.select([self.connection], [], [], 0.05)
                if readable:
                    try:
                        if self.connection.recv(1) == b'':
                            self.server.header_wait_upstream_closed.set()
                    except OSError:
                        self.server.header_wait_upstream_closed.set()
                    return
            return
        if self.path == '/silent-disconnect':
            self.send_response(200)
            self.send_header('Content-Type', 'text/event-stream')
            self.send_header('Connection', 'close')
            self.end_headers()
            self.wfile.write(b'data: first\n\n'); self.wfile.flush()
            self.server.first_event.set()
            deadline = time.monotonic() + 3
            while time.monotonic() < deadline:
                readable, _, _ = select.select([self.connection], [], [], 0.05)
                if readable:
                    try:
                        if self.connection.recv(1) == b'':
                            self.server.silent_upstream_closed.set()
                    except OSError:
                        self.server.silent_upstream_closed.set()
                    return
            return
        payload = (self.server.marker + ':' + self.headers.get('Host', '')).encode()
        self.send_response(200)
        self.send_header('Content-Length', str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)

    def do_POST(self):
        if self.headers.get('Transfer-Encoding', '').lower() == 'chunked':
            chunks = []
            while True:
                count = int(self.rfile.readline().split(b';', 1)[0], 16)
                if count == 0:
                    self.rfile.readline()
                    break
                chunks.append(self.rfile.read(count))
                self.rfile.read(2)
            body = b''.join(chunks)
        else:
            body = self.rfile.read(int(self.headers.get('Content-Length', '0')))
        self.server.posted_body = body
        self.send_response(200)
        self.send_header('Content-Length', str(len(body)))
        self.end_headers()
        self.wfile.write(body)


def start(server):
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    return thread


class LoopbackGatewayTests(unittest.TestCase):
    def setUp(self):
        self.green = ThreadingHTTPServer(('127.0.0.1', 0), Backend)
        self.green.marker = 'green'
        self.green.first_event = threading.Event()
        self.green.heartbeat_event = threading.Event()
        self.green.disconnect_release = threading.Event()
        self.green.upstream_closed = threading.Event()
        self.green.silent_upstream_closed = threading.Event()
        self.green.header_wait_started = threading.Event()
        self.green.header_wait_upstream_closed = threading.Event()
        self.green.stream_finish = threading.Event()
        self.green.heartbeat_delay = 0.25
        self.blue = ThreadingHTTPServer(('127.0.0.1', 0), Backend)
        self.blue.marker = 'blue'
        start(self.green); start(self.blue)
        self.color = 'green'

        def receipt():
            return ('127.0.0.1', self.green.server_port if self.color == 'green' else self.blue.server_port)
        self.gateway = g.server(('127.0.0.1', 0), receipt,
                                [('127.0.0.1', self.green.server_port),
                                 ('127.0.0.1', self.blue.server_port)])
        start(self.gateway)
        self.addCleanup(self.gateway.shutdown); self.addCleanup(self.gateway.server_close)
        self.addCleanup(self.green.stream_finish.set)
        self.addCleanup(self.green.disconnect_release.set)
        self.addCleanup(self.blue.shutdown); self.addCleanup(self.blue.server_close)
        self.addCleanup(self.green.shutdown); self.addCleanup(self.green.server_close)

    def request(self, path='/healthz'):
        client = http.client.HTTPConnection('127.0.0.1', self.gateway.server_port, timeout=3)
        try:
            client.request('GET', path, headers={'Host': 'mcp.wesiyu.com'})
            response = client.getresponse()
            return response.status, response.getheader(g.COLOR_HEADER), response.read()
        finally:
            client.close()

    def test_reloads_receipt_for_each_request_and_preserves_host(self):
        self.assertEqual(self.request(), (200, 'green', b'green:mcp.wesiyu.com'))
        self.color = 'blue'
        self.assertEqual(self.request(), (200, 'blue', b'blue:mcp.wesiyu.com'))

    def test_color_attestation_covers_mcp_readiness_and_metadata_routes(self):
        for path in ('/healthz', '/readyz', '/mcp', '/.well-known/oauth-protected-resource/mcp'):
            status, color, _ = self.request(path)
            self.assertEqual(status, 200, path)
            self.assertEqual(color, 'green', path)

    def test_invalid_receipt_never_exposes_upstream_or_request_data(self):
        self.gateway.receipt_reader = lambda: (_ for _ in ()).throw(ValueError('credential=value'))
        status, color, body = self.request()
        self.assertEqual(status, 502)
        self.assertIsNone(color)
        self.assertEqual(body, b'loopback gateway unavailable\n')

    def recv_until(self, client, marker, timeout=1):
        data = b''
        client.settimeout(timeout)
        while marker not in data:
            block = client.recv(65536)
            if not block:
                raise AssertionError('gateway closed before expected stream data')
            data += block
        return data

    def test_sse_first_event_and_idle_heartbeat_are_flushed_without_eof(self):
        client = socket.create_connection(('127.0.0.1', self.gateway.server_port), timeout=1)
        try:
            client.sendall(b'GET /events HTTP/1.1\r\nHost: mcp.wesiyu.com\r\nConnection: close\r\n\r\n')
            first = self.recv_until(client, b'data: first\n\n')
            self.assertIn(b'200 OK', first)
            self.assertTrue(self.green.first_event.wait(0.5))
            # The upstream intentionally remains open.  The heartbeat proves
            # the proxy does not wait for EOF or a 64KiB response buffer.
            heartbeat = self.recv_until(client, b': heartbeat\n\n')
            self.assertIn(b': heartbeat\n\n', heartbeat)
            self.assertTrue(self.green.heartbeat_event.wait(0.5))
        finally:
            self.green.stream_finish.set()
            client.close()

    def test_sse_over_fifteen_seconds_idle_still_relays_next_event(self):
        client = socket.create_connection(('127.0.0.1', self.gateway.server_port), timeout=1)
        try:
            client.sendall(b'GET /idle-events HTTP/1.1\r\nHost: mcp.wesiyu.com\r\nConnection: close\r\n\r\n')
            self.recv_until(client, b'data: first\n\n')
            started = time.monotonic()
            after_idle = self.recv_until(client, b'data: after-idle\n\n', timeout=17)
            self.assertGreaterEqual(time.monotonic() - started, 15)
            self.assertIn(b'data: after-idle\n\n', after_idle)
        finally:
            client.close()

    def test_tool_response_headers_can_arrive_after_fifteen_seconds(self):
        client = socket.create_connection(('127.0.0.1', self.gateway.server_port), timeout=1)
        try:
            started = time.monotonic()
            client.sendall(b'GET /late-headers HTTP/1.1\r\nHost: mcp.wesiyu.com\r\nConnection: close\r\n\r\n')
            response = self.recv_until(client, b'late-first-packet', timeout=17)
            self.assertGreaterEqual(time.monotonic() - started, 15)
            self.assertIn(b'200 OK', response)
        finally:
            client.close()

    def test_client_reset_cancels_upstream_waiting_for_first_response_headers(self):
        client = socket.create_connection(('127.0.0.1', self.gateway.server_port), timeout=1)
        try:
            client.sendall(b'GET /late-headers-cancel HTTP/1.1\r\nHost: mcp.wesiyu.com\r\nConnection: keep-alive\r\n\r\n')
            self.assertTrue(self.green.header_wait_started.wait(1), 'upstream did not enter header wait')
            client.setsockopt(socket.SOL_SOCKET, socket.SO_LINGER, struct.pack('ii', 1, 0))
        finally:
            client.close()
        self.assertTrue(self.green.header_wait_upstream_closed.wait(3),
                        'gateway left upstream header wait open after client reset')

    def test_chunked_request_body_reaches_upstream_without_buffered_rewrite(self):
        client = socket.create_connection(('127.0.0.1', self.gateway.server_port), timeout=1)
        try:
            client.sendall(
                b'POST /mcp HTTP/1.1\r\nHost: mcp.wesiyu.com\r\nTransfer-Encoding: chunked\r\nConnection: close\r\n\r\n'
                b'4\r\ntest\r\n5\r\n-body\r\n0\r\n\r\n')
            response = self.recv_until(client, b'test-body')
            self.assertIn(b'200 OK', response)
            self.assertEqual(self.green.posted_body, b'test-body')
        finally:
            client.close()

    def test_client_reset_closes_the_upstream_stream(self):
        client = socket.create_connection(('127.0.0.1', self.gateway.server_port), timeout=1)
        try:
            client.sendall(b'GET /disconnect HTTP/1.1\r\nHost: mcp.wesiyu.com\r\nConnection: close\r\n\r\n')
            self.recv_until(client, b'data: first\n\n')
            client.setsockopt(socket.SOL_SOCKET, socket.SO_LINGER, struct.pack('ii', 1, 0))
        finally:
            client.close()
        self.green.disconnect_release.set()
        self.assertTrue(self.green.upstream_closed.wait(2), 'gateway did not close upstream after client reset')

    def test_client_reset_closes_silent_upstream_without_waiting_for_next_event(self):
        original = g.downstream_closed
        observed = threading.Event()
        def monitored(connection):
            value = original(connection)
            if value:
                observed.set()
            return value
        g.downstream_closed = monitored
        client = socket.create_connection(('127.0.0.1', self.gateway.server_port), timeout=1)
        try:
            client.sendall(b'GET /silent-disconnect HTTP/1.1\r\nHost: mcp.wesiyu.com\r\nConnection: keep-alive\r\n\r\n')
            self.recv_until(client, b'data: first\n\n')
            client.setsockopt(socket.SOL_SOCKET, socket.SO_LINGER, struct.pack('ii', 1, 0))
        finally:
            client.close()
        try:
            self.assertTrue(observed.wait(0.5), 'gateway did not observe client reset')
            self.assertTrue(self.green.silent_upstream_closed.wait(3),
                            'gateway left a silent upstream open after client reset')
        finally:
            g.downstream_closed = original


if __name__ == '__main__':
    unittest.main()
