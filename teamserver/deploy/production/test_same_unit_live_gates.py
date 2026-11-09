import json
import unittest
from unittest.mock import patch

import same_unit_live_gates as gates


class Response:
    def __init__(self, status, body): self.status, self.body = status, body
    def read(self, _): return self.body


class Connection:
    wrong = None
    def __init__(self, host, timeout): self.host, self.path = host, None
    def request(self, method, path, headers): self.path = path
    def getresponse(self):
        if self.host == 'mcp.jianpinke.com': return Response(503, b'')
        if self.path == '/mcp': return Response(401, b'')
        issuer = 'https://' + self.host + '/gnas/oauth'
        if self.path.endswith('/mcp'):
            body = {'resource': 'https://' + self.host + '/mcp', 'authorization_servers': [issuer]}
            if self.wrong == 'resource' and self.host == 'mcp.wesiyu.com': body['resource'] = 'https://wrong/mcp'
        else:
            body = {'issuer': issuer if self.wrong != 'issuer' else 'https://wrong/gnas/oauth'}
        return Response(200, json.dumps(body).encode())
    def close(self): pass


class LiveGatesTest(unittest.TestCase):
    def tearDown(self): Connection.wrong = None
    @patch.object(gates.http.client, 'HTTPSConnection', Connection)
    def test_public_probe_verifies_resource_and_issuer_not_only_status(self):
        self.assertEqual(gates.public_probe()['mcp.wesiyu.com'], [401, 200, 200])
        for wrong in ('resource', 'issuer'):
            Connection.wrong = wrong
            with self.subTest(wrong=wrong), self.assertRaises(ValueError): gates.public_probe()


if __name__ == '__main__':
    unittest.main()
