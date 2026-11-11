import datetime as dt
import hashlib
import importlib.util
import json
from pathlib import Path
import shlex
import tempfile
import unittest
from unittest.mock import patch

path = Path(__file__).with_name('mcp_target_canary.py')
spec = importlib.util.spec_from_file_location('mcp_target_canary', path)
m = importlib.util.module_from_spec(spec); spec.loader.exec_module(m)


class TargetCanaryTests(unittest.TestCase):
    def test_exec_uses_isolated_authority_and_target_only_scopes(self):
        unit = m.unit_content().decode()
        start = next(x for x in unit.splitlines() if x.startswith('ExecStart='))
        args = shlex.split(start.split('=', 1)[1])
        self.assertEqual(args[0], '/usr/bin/env')
        self.assertIn('GNAS_BASE_URL=http://127.0.0.1:7503', args)
        self.assertIn('TEAM_MCP_ADVERTISED_SCOPES=zoop.read zoop.write', args)
        self.assertIn('TEAM_MCP_ADVERTISED_SCOPES_RESOURCE=https://zhycit.com/mcp', args)
        self.assertIn('127.0.0.1:7705', args)
        self.assertNotIn('systemctl restart', unit)

    def receipt(self, approval):
        now = dt.datetime.now(dt.timezone.utc)
        return {'schema_version': 1, 'approval_id': approval, 'environment': 'zhycit-prod-01',
            'action': 'start-isolated-mcp', 'release_id': m.RELEASE,
            'source_commit': 'c81ba765e32292875b863a058a0ac34ef1ceaefe',
            'binary_sha256': m.BINARY_SHA, 'policy_sha256': m.POLICY_SHA,
            'gnas_canary_release_id': m.RID, 'gnas_canary_sha256': m.GNAS_SHA,
            'shared_gnas_release_id': m.OLD_GNAS_RID, 'shared_gnas_sha256': m.OLD_GNAS_SHA,
            'shared_mcp_sha256': m.OLD_MCP_SHA, 'listen': '127.0.0.1:7705', 'host': 'zhycit.com',
            'unit_sha256': hashlib.sha256(m.unit_content()).hexdigest(), 'controller_sha256': 'c'*64,
            'approved_at': now.isoformat(), 'expires_at': (now+dt.timedelta(minutes=5)).isoformat(), 'approved_by': 'delegated-root'}

    def test_receipt_exact_binding_and_expiry(self):
        approval = 'APR-20261106T203409Z-4e6b6a0e50e2'
        with tempfile.TemporaryDirectory() as tmp, patch.object(m, 'APPROVALS', Path(tmp)), \
             patch.object(m, 'trusted'), patch.object(m, 'sha', return_value='c'*64):
            path = Path(tmp)/(approval+'.json'); obj=self.receipt(approval)
            def write(): path.write_text(json.dumps(obj)); path.chmod(0o400)
            write(); self.assertEqual(m.check_receipt(approval)['host'], 'zhycit.com')
            path.chmod(0o600); obj['host']='other.example'; write()
            with self.assertRaisesRegex(ValueError, 'does not bind'): m.check_receipt(approval)
            path.chmod(0o600); obj['host']='zhycit.com'; obj['expires_at']=(dt.datetime.now(dt.timezone.utc)-dt.timedelta(seconds=1)).isoformat(); write()
            with self.assertRaisesRegex(ValueError, 'expired'): m.check_receipt(approval)

    def test_snapshot_rejects_shared_restart_and_tenant_regression(self):
        status = ('service_state=active\nrelease_id='+m.OLD_GNAS_RID+'\nruntime_sha256='+m.OLD_GNAS_SHA+
                  '\nrestarts=0\nmain_pid=123\n').encode()
        with patch.object(m, 'run', return_value=status), patch.object(m, 'sha', return_value=m.OLD_MCP_SHA), \
             patch.object(m, 'properties', return_value={'ActiveState':'active','MainPID':'456','NRestarts':'0'}), \
             patch.object(m, 'probe', side_effect=[(200,b''),(200,b''),(200,b''),(503,b'')]):
            self.assertEqual(m.snapshot(), ('123',456,(200,200,200,503)))
        with patch.object(m, 'run', return_value=status), patch.object(m, 'sha', return_value=m.OLD_MCP_SHA), \
             patch.object(m, 'properties', return_value={'ActiveState':'active','MainPID':'456','NRestarts':'1'}):
            with self.assertRaisesRegex(ValueError, 'shared MCP changed'): m.snapshot()
        with patch.object(m, 'run', return_value=status), patch.object(m, 'sha', return_value=m.OLD_MCP_SHA), \
             patch.object(m, 'properties', return_value={'ActiveState':'active','MainPID':'456','NRestarts':'0'}), \
             patch.object(m, 'probe', side_effect=[(200,b''),(500,b''),(200,b''),(503,b'')]):
            with self.assertRaisesRegex(ValueError, 'tenant health changed'): m.snapshot()


if __name__ == '__main__': unittest.main()
