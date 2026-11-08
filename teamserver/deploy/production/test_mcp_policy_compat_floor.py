import hashlib
from pathlib import Path
import tempfile
import unittest
from unittest import mock

import mcp_policy_compat_floor as floor


class PolicyCompatibilityFloorTests(unittest.TestCase):
    def setUp(self):
        self.digest = 'a' * 64
        self.route = b'proxy_pass http://127.0.0.1:7709;\nproxy_pass http://127.0.0.1:7709;'
        self.floor = {'schema_version': 1, 'host': 'zhycit.com', 'phase': 'policy', 'targets': [
            {'port': 7709, 'unit': 'wecom-mcp-zhycit-migration.service', 'sha256': self.digest, 'route_sha256': hashlib.sha256(self.route).hexdigest(), 'runtime_sha256': 'c' * 64, 'nginx_config_sha256': 'e' * 64},
        ]}

    def test_allows_only_the_live_verified_compatible_target(self):
        self.assertEqual(floor.validate_route(self.route, self.floor, lambda _unit, _port: (self.digest, 'c' * 64)), 'e' * 64)
        with self.assertRaises(ValueError):
            floor.validate_route(self.route.replace(b'7709', b'7708'), self.floor, lambda _unit, _port: (self.digest, 'c' * 64))
        with self.assertRaises(ValueError):
            floor.validate_route(self.route, self.floor, lambda _unit, _port: ('b' * 64, 'c' * 64))
        with self.assertRaises(ValueError):
            floor.validate_route(self.route, self.floor, lambda _unit, _port: (self.digest, 'd' * 64))

    def test_partial_or_malformed_routes_fail_closed(self):
        for route in (b'', self.route.replace(b'7709', b'7708', 1), self.route + b'\nproxy_pass http://127.0.0.1:7709;', b'# ' + self.route + b'\nproxy_pass http://localhost:7708;'):
            with self.subTest(route=route), self.assertRaises(ValueError):
                floor.validate_route(route, self.floor, lambda _unit, _port: self.digest)

    def test_floor_cannot_be_empty_or_ambiguous(self):
        for value in ({}, {**self.floor, 'host': 'other'}, {**self.floor, 'targets': []},
                      {**self.floor, 'targets': self.floor['targets'] * 2}):
            with self.subTest(floor=value), self.assertRaises(ValueError):
                floor.validate_route(self.route, value, lambda _unit, _port: self.digest)

    def test_missing_or_symlinked_floor_never_bypasses_guard(self):
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory) / 'floor.json'
            with mock.patch.object(floor, 'FLOOR', target):
                with self.assertRaises(FileNotFoundError):
                    floor.guard_route(self.route)
                actual = Path(directory) / 'actual.json'
                actual.write_text('{}')
                target.symlink_to(actual)
                with self.assertRaises(OSError):
                    floor.guard_route(self.route)

    def test_nginx_and_mcp_must_share_network_namespace(self):
        with mock.patch.object(floor, '_nginx_processes', return_value=frozenset({20, 21})):
            with mock.patch.object(floor, '_namespace', side_effect=lambda pid: (1, 2) if pid in (10, 20, 21) else (1, 3)), mock.patch.object(floor, '_start_time', return_value='100'):
                self.assertEqual(floor._assert_nginx_namespace(10), ((1, 2), frozenset({(20, '100'), (21, '100')})))
            with mock.patch.object(floor, '_namespace', side_effect=lambda pid: (1, 3) if pid == 21 else (1, 2)), mock.patch.object(floor, '_start_time', return_value='100'):
                with self.assertRaises(ValueError):
                    floor._assert_nginx_namespace(10)

    def test_effective_nginx_configuration_must_match(self):
        nginx_output = b'reviewed nginx include topology'
        with mock.patch.object(floor.subprocess, 'run', return_value=mock.Mock(stdout=nginx_output)):
            floor.guard_staged_nginx(hashlib.sha256(nginx_output).hexdigest())
            with self.assertRaises(ValueError):
                floor.guard_staged_nginx('f' * 64)


if __name__ == '__main__':
    unittest.main()
