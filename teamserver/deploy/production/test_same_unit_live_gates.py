import hashlib
import json
import os
from pathlib import Path
import subprocess
import tempfile
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
    def test_fleet_probe_checks_identity_and_cleans_up_after_timeout(self):
        expected = {'bindings': [{'host': 'tenant', 'state': 'ready'}]}
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp); probe = root / 'probe'; probe.write_bytes(b'probe'); probe.chmod(0o555)
            sha = hashlib.sha256(b'probe').hexdigest(); calls = []
            def runner(args, **kwargs):
                calls.append(args)
                if args[0].endswith('systemd-run'):
                    if len(calls) == 1: raise subprocess.TimeoutExpired(args, 1)
                return subprocess.CompletedProcess(args, 0, stdout=b'not-found\n')
            with self.assertRaises(subprocess.TimeoutExpired):
                gates.fleet_probe(expected, None, probe, sha, os.getuid(), root, runner)
            self.assertEqual([args[1] for args in calls[1:]], ['stop', 'show'])
            probe.chmod(0o777)
            with self.assertRaises(ValueError):
                gates.fleet_probe(expected, None, probe, sha, os.getuid(), root, runner)

    def test_fleet_probe_waits_for_delayed_systemd_collection(self):
        expected = {'mode': 'read-only-gnas-discovery', 'bindings': []}
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp); probe = root / 'probe'; probe.write_bytes(b'probe'); probe.chmod(0o555)
            sha = hashlib.sha256(b'probe').hexdigest(); states = iter((b'loaded\n', b'not-found\n'))
            def runner(args, **kwargs):
                if args[0].endswith('systemd-run'):
                    return subprocess.CompletedProcess(args, 0, stdout=json.dumps(expected).encode())
                if args[1] == 'show': return subprocess.CompletedProcess(args, 0, stdout=next(states))
                return subprocess.CompletedProcess(args, 0, stdout=b'')
            with patch('time.sleep', lambda _: None):
                gates.fleet_probe(expected, None, probe, sha, os.getuid(), root, runner)

    def test_fleet_ignores_unrelated_tenant_when_checking_target(self):
        old = {'mode': 'read-only-gnas-discovery', 'bindings': [
            {'host': 'target', 'state': 'ready'}, {'host': 'other', 'state': 'unavailable'}]}
        new = {'mode': 'read-only-gnas-discovery', 'bindings': [
            {'host': 'target', 'state': 'ready'}, {'host': 'other', 'state': 'ready'}]}
        gates.compare_fleet_result(json.dumps(new).encode(), old, ('target',))
        with self.assertRaises(ValueError): gates.compare_fleet_result(json.dumps(new).encode(), old, ('other',))

    def test_fleet_result_rejects_changed_binding(self):
        expected = {'bindings': [{'host': 'tenant', 'state': 'ready'}]}
        gates.compare_fleet_result(json.dumps(expected).encode(), expected)
        with self.assertRaises(ValueError):
            gates.compare_fleet_result(b'{\"bindings\":[]}', expected)

    @patch.object(gates.http.client, 'HTTPSConnection', Connection)
    def test_public_probe_verifies_resource_and_issuer_not_only_status(self):
        self.assertEqual(gates.public_probe()['mcp.wesiyu.com'], [401, 200, 200])
        for wrong in ('resource', 'issuer'):
            Connection.wrong = wrong
            with self.subTest(wrong=wrong), self.assertRaises(ValueError): gates.public_probe()


if __name__ == '__main__':
    unittest.main()
