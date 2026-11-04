"""Controlled local receipt-consumer regression; no production listener is used."""
import http.client
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import importlib.util
from pathlib import Path
import threading
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
        payload = (self.server.marker + ':' + self.headers.get('Host', '')).encode()
        self.send_response(200)
        self.send_header('Content-Length', str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)


def start(server):
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    return thread


class LoopbackGatewayTests(unittest.TestCase):
    def setUp(self):
        self.green = ThreadingHTTPServer(('127.0.0.1', 0), Backend)
        self.green.marker = 'green'
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
        self.addCleanup(self.blue.shutdown); self.addCleanup(self.blue.server_close)
        self.addCleanup(self.green.shutdown); self.addCleanup(self.green.server_close)

    def request(self):
        client = http.client.HTTPConnection('127.0.0.1', self.gateway.server_port, timeout=3)
        try:
            client.request('GET', '/healthz', headers={'Host': 'mcp.wesiyu.com'})
            response = client.getresponse()
            return response.status, response.getheader(g.COLOR_HEADER), response.read()
        finally:
            client.close()

    def test_reloads_receipt_for_each_request_and_preserves_host(self):
        self.assertEqual(self.request(), (200, 'green', b'green:mcp.wesiyu.com'))
        self.color = 'blue'
        self.assertEqual(self.request(), (200, 'blue', b'blue:mcp.wesiyu.com'))

    def test_invalid_receipt_never_exposes_upstream_or_request_data(self):
        self.gateway.receipt_reader = lambda: (_ for _ in ()).throw(ValueError('credential=value'))
        status, color, body = self.request()
        self.assertEqual(status, 502)
        self.assertIsNone(color)
        self.assertEqual(body, b'loopback gateway unavailable\n')


if __name__ == '__main__':
    unittest.main()
