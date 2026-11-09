#!/usr/bin/python3 -I
"""Recover only the approved Zhonghe MCP candidate after a closed attempt."""
import hashlib
import json
import os
from pathlib import Path
import re
import socket
import stat
import sys

ROOT=Path('/usr/local/lib/wecom-mcp-open-bootstrap')
SELF=ROOT/'recover-closed-target.py'
ARCHIVE=ROOT/'same-unit-execution.pyz'
ARCHIVE_SHA='1372378cc8446074bd65a5cf5679a9d9f4dba1688a1c2c2cfa307e085fd01764'
ORIGINAL_ID='APR-20261109T082343Z-mcpopentarget01'
ORIGINAL_PLAN_SHA='5bdab7eef0c63b0aed4e6fbbe0b0a23ee40a98f33b8c7af8682ddbf6c76ccc3a'
RELEASE_ID='20261109T074119Z-eab7c778c79f'
CANDIDATE_SHA='c8af1b1dd27f4dd0323eee95bd16258441dd0de7d016000f724b6e0a81ff0005'


def trusted_file(path, mode):
    if path.is_symlink(): raise ValueError('untrusted controller path')
    s=path.lstat()
    if not stat.S_ISREG(s.st_mode) or s.st_uid!=0 or stat.S_IMODE(s.st_mode)!=mode: raise ValueError('untrusted controller file')
    parent=path.parent
    while True:
        s=parent.lstat()
        if not stat.S_ISDIR(s.st_mode) or s.st_uid!=0 or s.st_mode&0o022: raise ValueError('untrusted controller parent')
        if parent==Path('/'):break
        parent=parent.parent


def sha(path): return hashlib.sha256(path.read_bytes()).hexdigest()


def no_listening_socket(port, proc=Path('/proc'), socket_factory=socket.socket):
    if (proc/'self/ns/net').stat().st_ino!=(proc/'1/ns/net').stat().st_ino:raise ValueError('network namespace changed')
    for table in ('tcp','tcp6'):
        rows=(proc/'net'/table).read_text().splitlines()[1:]
        for row in rows:
            columns=row.split()
            if len(columns)<10:raise ValueError('invalid socket table')
            if columns[3]=='0A' and columns[1].rsplit(':',1)[-1]==format(port,'04X'):
                return False
    with socket_factory(socket.AF_INET,socket.SOCK_STREAM) as sock:
        sock.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1)
        try:sock.bind(('127.0.0.1',port))
        except OSError:return False
    return True


def validate_recovery_plan(plan, script_sha):
    required={'schema_version','environment','action','original_approval_id','original_plan_sha256','release_id','candidate_sha256','unit','recovery_script_sha256','controller_archive_sha256'}
    expected={'schema_version':1,'environment':'zhycit-prod-01','action':'recover-closed-target','original_approval_id':ORIGINAL_ID,
              'original_plan_sha256':ORIGINAL_PLAN_SHA,'release_id':RELEASE_ID,'candidate_sha256':CANDIDATE_SHA,
              'unit':'wecom-mcp-zhycit-migration.service','recovery_script_sha256':script_sha,'controller_archive_sha256':ARCHIVE_SHA}
    if not isinstance(plan,dict) or set(plan)!=required or plan!=expected:raise ValueError('recovery plan mismatch')


def consume_after_recheck(check,consume):
    check()
    consume()


def main():
    if os.geteuid()!=0 or len(sys.argv)!=3 or sys.argv[1] not in ('switch','recover') or Path(sys.argv[0])!=SELF or not re.fullmatch(r'APR-[0-9]{8}T[0-9]{6}Z-[A-Za-z0-9._-]{6,64}',sys.argv[2]):
        raise ValueError('root, fixed controller and approval required')
    action,aid=sys.argv[1],sys.argv[2]
    trusted_file(SELF,0o555);trusted_file(ARCHIVE,0o555)
    if sha(ARCHIVE)!=ARCHIVE_SHA:raise ValueError('controller archive changed')
    sys.path.insert(0,str(ARCHIVE))
    import same_unit_plan as contract
    import same_unit_receipt as receipt
    import same_unit_journal as journal
    import same_unit_guarded_ops as guarded
    import same_unit_bootstrap_transaction as transaction
    import same_unit_live_gates as gates
    from same_unit_systemd import Unit
    from target_bootstrap_journal import Journal
    approvals=Path('/etc/gnas/release-approvals');plans=Path('/etc/gnas/mcp-bootstrap-plans')
    root=Path('/var/lib/wecom-mcp-isolated');used=root/'bootstrap-used';journals=root/'bootstrap-transactions';evidence=root/'bootstrap-evidence'
    original_path=plans/(ORIGINAL_ID+'.json');original_receipt=approvals/(ORIGINAL_ID+'.json')
    recover_path=plans/(aid+'.json');recover_receipt=approvals/(aid+'.json')
    original,original_sha=receipt.verify_consumed(original_receipt,original_path,used,ORIGINAL_ID)
    if original_sha!=ORIGINAL_PLAN_SHA or original.get('profile')!='target':raise ValueError('original approval mismatch')
    candidate=contract.validate(original)
    if action=='switch':
        new,plan_sha=receipt.validate(recover_receipt,recover_path,aid,'recover-mcp-open-bootstrap')
    else:
        import datetime as dt
        stored=receipt._json(receipt._read_regular(recover_receipt,Path('/'),0))
        approved=dt.datetime.fromisoformat(stored['approved_at'].replace('Z','+00:00'))
        new,plan_sha=receipt.validate(recover_receipt,recover_path,aid,'recover-mcp-open-bootstrap',now=approved)
    validate_recovery_plan(new,sha(SELF))
    receipt._trusted_directory(used,Path('/'),0);receipt._trusted_directory(journals,Path('/'),0)
    original_records=journal.Records(Journal(journals/ORIGINAL_ID,0),ORIGINAL_PLAN_SHA)
    new_records=journal.Records(Journal(journals/aid,0),plan_sha)

    class RecoveryUnit(Unit):
        def no_listener(self):return no_listening_socket(self.port)

    u=RecoveryUnit(original['unit'],original['fragment_sha256'],CANDIDATE_SHA)
    def check_receipt():
        current,current_sha=receipt.validate(recover_receipt,recover_path,aid,'recover-mcp-open-bootstrap')
        if current!=new or current_sha!=plan_sha:raise ValueError('recovery approval changed')
    def consume():
        consume_after_recheck(check_receipt,lambda:receipt.consume(used,aid,plan_sha))
    def check_recovery():
        import datetime as dt
        stored=receipt._json(receipt._read_regular(recover_receipt,Path('/'),0))
        approved=dt.datetime.fromisoformat(stored['approved_at'].replace('Z','+00:00'))
        current,current_sha=receipt.validate(recover_receipt,recover_path,aid,'recover-mcp-open-bootstrap',now=approved)
        marker=receipt._read_regular(used/aid,Path('/'),0)
        if current!=new or current_sha!=plan_sha or marker!=(plan_sha+'\n').encode():raise ValueError('recovery approval changed')
    def closed_baseline():
        receipt.verify_consumed(original_receipt,original_path,used,ORIGINAL_ID)
        if original_records.terminal_state()!='closed':raise ValueError('original terminal is not closed')
        props=u.show();u.verify_fragment(props)
        if (props['ActiveState'] not in ('inactive','failed') or props['MainPID']!='0' or props['Job'] or
                props['UnitFileState']!='disabled' or props['DropInPaths'] or not u.no_processes() or not u.no_listener()):
            raise ValueError('closed target baseline changed')
        link=u.wants/u.name
        if link.exists() or link.is_symlink():raise ValueError('target boot link remains')
    def config_check():
        closed_baseline();data=gates.candidate(original)
        u.check_candidate_config(data,original['dropin_sha256'],candidate,original['candidate_policy_sha256'],original['environment_file_sha256'])
        fleet_content=receipt._read_regular(evidence/(original['fleet_snapshot_sha256']+'.json'),Path('/'),0)
        if hashlib.sha256(fleet_content).hexdigest()!=original['fleet_snapshot_sha256']:raise ValueError('fleet evidence changed')
        fleet=receipt._json(fleet_content)
        if fleet.get('authority')!='gnas-service-discovery' or fleet.get('probe_sha256')!='0f427eef348b5e34fdb62fe476213cf9770ccda8a37443d1f82a7e93fc8279d4':raise ValueError('fleet evidence invalid')
        gates.fleet_probe(fleet['result'],('zhycit.com',));closed_baseline()
    def recovery_check():
        data=gates.candidate(original)
        u.check_candidate_config(data,original['dropin_sha256'],candidate,original['candidate_policy_sha256'],original['environment_file_sha256'])
    ops=guarded.GuardedOps(original,u,new_records,check_receipt,consume,check_recovery,closed_baseline,config_check,None,
                           lambda:gates.health_probe(original),recovery_check=recovery_check,
                           read_candidate=lambda:gates.candidate(original),verify_untouched=closed_baseline)
    (transaction.switch if action=='switch' else transaction.recover)(ops)
    print(json.dumps({'state':new_records.terminal_state(),'profile':'target','release_id':RELEASE_ID,'recovery_plan_sha256':plan_sha},sort_keys=True))


if __name__=='__main__':
    try:main()
    except Exception as error:
        print('MCP_TARGET_RECOVERY_REFUSED: '+type(error).__name__,file=sys.stderr);sys.exit(1)
