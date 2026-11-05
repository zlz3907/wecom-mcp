#!/usr/bin/python3 -I
"""Root-owned loopback-only receipt consumer for a future blue/green MCP cutover.

This process never configures a public proxy.  A separately reviewed public
route may point at its fixed loopback listener only after its own controlled
change.  The active receipt is re-read, validated and ownership-checked for
every request; it is deliberately not cached.
"""
import http.client
import json
import os
import queue
import socket
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
import stat
import sys
import threading

ACTIVE = Path('/var/lib/wecom-mcp-release/entrypoint/active.json')
LISTEN = ('127.0.0.1', 7704)
COLORS = {'green': ('127.0.0.1', 7702), 'blue': ('127.0.0.1', 7703)}
HOP_BY_HOP = {'connection', 'keep-alive', 'proxy-authenticate', 'proxy-authorization',
              'te', 'trailers', 'transfer-encoding', 'upgrade'}
COLOR_HEADER = 'X-WeCom-MCP-Loopback-Color'
# Keep local connect failure bounded, but align first-response and ordinary
# response waits with the reviewed public MCP proxy_read_timeout.  A tool call
# may legitimately take longer than the old 15s gateway default before it can
# send its first headers/body.
UPSTREAM_CONNECT_TIMEOUT_SECONDS = 15
UPSTREAM_RESPONSE_HEADER_TIMEOUT_SECONDS = 3600


def require(ok, message):
    if not ok:
        raise ValueError(message)


def trusted_directory(path):
    value = path.lstat()
    require(stat.S_ISDIR(value.st_mode) and path.resolve() == path and value.st_uid == 0
            and not value.st_mode & 0o022, 'untrusted active receipt parent')


def trusted_receipt(path=ACTIVE):
    require(path == ACTIVE and path.is_absolute() and path.parent == ACTIVE.parent,
            'fixed active receipt required')
    trusted_directory(path.parent)
    value = path.lstat()
    require(stat.S_ISREG(value.st_mode) and path.resolve() == path and value.st_uid == 0
            and not value.st_mode & 0o022 and value.st_size <= 4096,
            'untrusted active receipt')
    with path.open() as handle:
        receipt = json.load(handle)
    color = receipt.get('color') if isinstance(receipt, dict) else None
    require(color in COLORS and receipt == {'version': 1, 'color': color,
                        'loopback': ':'.join(map(str, COLORS.get(color, ())))},
            'invalid active receipt')
    return COLORS[color]


def forwarded_headers(headers):
    connection_tokens = set()
    for value in headers.get_all('Connection', []):
        connection_tokens.update(token.strip().lower() for token in value.split(','))
    blocked = HOP_BY_HOP | connection_tokens | {'content-length'}
    return {key: value for key, value in headers.items() if key.lower() not in blocked}


def request_body(handler):
    transfer = handler.headers.get('Transfer-Encoding', '').lower()
    if transfer:
        require(transfer == 'chunked', 'unsupported request transfer encoding')
        while True:
            line = handler.rfile.readline(8192)
            require(line and len(line) < 8192, 'invalid chunk framing')
            size = int(line.split(b';', 1)[0].strip(), 16)
            require(0 <= size <= 16 * 1024 * 1024, 'invalid chunk length')
            if size == 0:
                while True:
                    trailer = handler.rfile.readline(8192)
                    require(trailer and len(trailer) < 8192, 'invalid chunk trailer')
                    if trailer in (b'\r\n', b'\n'):
                        return
            data = handler.rfile.read(size)
            require(len(data) == size and handler.rfile.read(2) == b'\r\n', 'truncated request body')
            yield data
        return
    length = handler.headers.get('Content-Length')
    if length is None:
        return
    count = int(length)
    require(0 <= count <= 64 * 1024 * 1024, 'invalid request length')
    while count:
        data = handler.rfile.read(min(count, 64 * 1024))
        require(data, 'truncated request body')
        count -= len(data)
        yield data


def downstream_closed(connection):
    """Non-destructively observe a downstream FIN/RST while SSE is silent."""
    try:
        flags = socket.MSG_PEEK | getattr(socket, 'MSG_DONTWAIT', 0)
        value = connection.recv(1, flags)
        # This gateway declares Connection: close.  A post-request downstream
        # byte is therefore a pipelined/smuggled follow-up, not a request this
        # stream may accept; treat it like a close rather than holding an
        # upstream SSE open indefinitely.
        return value == b'' or bool(value)
    except (BlockingIOError, InterruptedError):
        return False
    except OSError:
        return True


def stream_reader(response, output, stopped):
    """Read upstream in a worker so main can observe downstream disconnects."""
    try:
        while not stopped.is_set():
            data = response.read1(64 * 1024)
            item = ('eof', None) if not data else ('data', data)
            while not stopped.is_set():
                try:
                    output.put(item, timeout=0.1)
                    break
                except queue.Full:
                    continue
            if not data:
                return
    except (OSError, http.client.HTTPException, AttributeError) as error:
        while not stopped.is_set():
            try:
                output.put(('error', error), timeout=0.1)
                return
            except queue.Full:
                continue


def header_reader(upstream, output, stopped):
    """Wait for first upstream headers without blocking cancellation checks."""
    try:
        response = upstream.getresponse()
        if not stopped.is_set():
            output.put(('response', response))
    except (OSError, http.client.HTTPException, AttributeError) as error:
        if not stopped.is_set():
            output.put(('error', error))


def shutdown_socket(value):
    if value is None:
        return
    try:
        value.shutdown(socket.SHUT_RDWR)
    except OSError:
        pass
    try:
        value.close()
    except OSError:
        pass


class ReceiptGatewayHandler(BaseHTTPRequestHandler):
    protocol_version = 'HTTP/1.1'

    def log_message(self, format, *args):
        # Request targets and headers can carry identifiers; do not log either.
        return

    def _proxy(self):
        response_started = False
        try:
            host, port = self.server.receipt_reader()
            color = self.server.colors.get((host, port))
            require(color in COLORS, 'receipt selected disallowed backend')
            headers = forwarded_headers(self.headers)
            headers['Host'] = self.headers.get('Host', '')
            upstream = http.client.HTTPConnection(host, port, timeout=UPSTREAM_CONNECT_TIMEOUT_SECONDS)
            try:
                upstream.connect()
                require(upstream.sock is not None, 'upstream connect failed')
                upstream.sock.settimeout(UPSTREAM_RESPONSE_HEADER_TIMEOUT_SECONDS)
                has_body = (self.headers.get('Transfer-Encoding') is not None
                            or self.headers.get('Content-Length') is not None)
                upstream.request(self.command, self.path,
                                 body=request_body(self) if has_body else None, headers=headers,
                                 encode_chunked=has_body)
                headers_stopped = threading.Event()
                header_result = queue.Queue(maxsize=1)
                header_worker = threading.Thread(target=header_reader,
                                                 args=(upstream, header_result, headers_stopped), daemon=True)
                header_worker.start()
                response = None
                try:
                    while response is None:
                        try:
                            kind, value = header_result.get(timeout=0.25)
                        except queue.Empty:
                            if downstream_closed(self.connection):
                                return
                            continue
                        if kind == 'error':
                            raise value
                        response = value
                finally:
                    if response is None:
                        headers_stopped.set()
                        shutdown_socket(upstream.sock)
                        upstream.close()
                    header_worker.join(timeout=1)
                    require(not header_worker.is_alive(), 'upstream header reader did not stop')
                # Bound the connect/request-header phase, but an MCP SSE
                # response is intentionally long-lived.  After headers, each
                # upstream event must be relayed as it arrives rather than
                # timing out or waiting for a 64KiB buffer/EOF.
                stream_socket = upstream.sock
                if stream_socket is None:
                    stream_socket = getattr(getattr(response, 'fp', None), 'raw', None)
                    stream_socket = getattr(stream_socket, '_sock', None)
                require(stream_socket is not None, 'upstream stream socket unavailable')
                stream_socket.settimeout(None)
                self.send_response(response.status)
                for key, value in response.getheaders():
                    if key.lower() not in HOP_BY_HOP | {'content-length', COLOR_HEADER.lower()}:
                        self.send_header(key, value)
                # This is a non-sensitive, public routing attestation.  The
                # executor requires it during every public vector probe; a
                # direct old Nginx->7702 route therefore fails closed.
                self.send_header(COLOR_HEADER, color)
                self.send_header('Connection', 'close')
                self.end_headers()
                response_started = True
                stopped = threading.Event()
                chunks = queue.Queue(maxsize=1)
                reader = threading.Thread(target=stream_reader, args=(response, chunks, stopped), daemon=True)
                reader.start()
                try:
                    while True:
                        try:
                            kind, data = chunks.get(timeout=0.25)
                        except queue.Empty:
                            if downstream_closed(self.connection):
                                return
                            continue
                        if kind == 'eof':
                            return
                        if kind == 'error':
                            raise data
                        self.wfile.write(data)
                        self.wfile.flush()
                finally:
                    stopped.set()
                    # For HTTP/1.1 responses delimited by Connection: close,
                    # HTTPConnection may have transferred ownership of the
                    # socket to HTTPResponse and set upstream.sock=None.
                    # First shut down the real socket: response.close() may
                    # wait for a reader thread holding BufferedReader's lock,
                    # which would otherwise delay a truly silent upstream's
                    # EOF until it emits another event.
                    shutdown_socket(stream_socket)
                    response.close()
                    upstream.close()
                    reader.join(timeout=1)
                    require(not reader.is_alive(), 'upstream reader did not stop')
            finally:
                upstream.close()
        except (OSError, ValueError, json.JSONDecodeError, http.client.HTTPException):
            # Once a proxied status/headers have reached a client, appending a
            # second HTTP response would corrupt an SSE/stream.  A peer close
            # is therefore just a close; before headers we retain fail-closed
            # generic 502 behavior without upstream details.
            if not response_started:
                self.send_response(502)
                self.send_header('Cache-Control', 'no-store')
                self.send_header('Connection', 'close')
                self.end_headers()
                self.wfile.write(b'loopback gateway unavailable\n')
        finally:
            self.close_connection = True

    do_GET = _proxy
    do_POST = _proxy
    do_PUT = _proxy
    do_PATCH = _proxy
    do_DELETE = _proxy
    do_OPTIONS = _proxy
    do_HEAD = _proxy


def server(address=LISTEN, receipt_reader=trusted_receipt, allowed_backends=None):
    require(address == LISTEN or (address[0] == '127.0.0.1' and isinstance(address[1], int)
                                  and 0 <= address[1] < 65536), 'loopback listener required')
    value = ThreadingHTTPServer(address, ReceiptGatewayHandler)
    value.receipt_reader = receipt_reader
    values = tuple(COLORS.values()) if allowed_backends is None else tuple(allowed_backends)
    require(len(values) == 2 and len(set(values)) == 2, 'exactly two distinct backends required')
    value.colors = ({backend: color for color, backend in COLORS.items()} if allowed_backends is None
                    else {backend: color for color, backend in zip(('green', 'blue'), values)})
    return value


def main():
    require(os.geteuid() == 0, 'root required')
    os.environ['PATH'] = '/usr/sbin:/usr/bin:/sbin:/bin'
    value = server()
    try:
        value.serve_forever(poll_interval=0.5)
    finally:
        value.server_close()


if __name__ == '__main__':
    try:
        main()
    except Exception as error:
        print('LOOPBACK_GATEWAY_REFUSED: ' + str(error) if isinstance(error, ValueError)
              else 'LOOPBACK_GATEWAY_REFUSED: controlled operation failed', file=sys.stderr)
        sys.exit(1)
