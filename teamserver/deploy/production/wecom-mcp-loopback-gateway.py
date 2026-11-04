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
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
import stat
import sys

ACTIVE = Path('/var/lib/wecom-mcp-release/entrypoint/active.json')
LISTEN = ('127.0.0.1', 7704)
COLORS = {'green': ('127.0.0.1', 7702), 'blue': ('127.0.0.1', 7703)}
HOP_BY_HOP = {'connection', 'keep-alive', 'proxy-authenticate', 'proxy-authorization',
              'te', 'trailers', 'transfer-encoding', 'upgrade'}
COLOR_HEADER = 'X-WeCom-MCP-Loopback-Color'


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


class ReceiptGatewayHandler(BaseHTTPRequestHandler):
    protocol_version = 'HTTP/1.1'

    def log_message(self, format, *args):
        # Request targets and headers can carry identifiers; do not log either.
        return

    def _proxy(self):
        try:
            host, port = self.server.receipt_reader()
            color = self.server.colors.get((host, port))
            require(color in COLORS, 'receipt selected disallowed backend')
            headers = forwarded_headers(self.headers)
            headers['Host'] = self.headers.get('Host', '')
            upstream = http.client.HTTPConnection(host, port, timeout=15)
            try:
                upstream.request(self.command, self.path, body=request_body(self), headers=headers,
                                 encode_chunked=True)
                response = upstream.getresponse()
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
                while True:
                    data = response.read(64 * 1024)
                    if not data:
                        break
                    self.wfile.write(data)
                self.wfile.flush()
            finally:
                upstream.close()
        except (OSError, ValueError, json.JSONDecodeError, http.client.HTTPException):
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
