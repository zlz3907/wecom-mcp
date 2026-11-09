#!/usr/bin/python3 -I
"""Stage a separately named verified controller; never restart a service."""
import hashlib
import json
import os
from pathlib import Path
import secrets
import stat
import subprocess
import sys

ROOT=Path('/usr/local/lib/wecom-mcp-open-bootstrap')
OLD=ROOT/'same-unit-execution.pyz'
NEW=ROOT/'same-unit-execution-v2.pyz'
OLD_SHA='1372378cc8446074bd65a5cf5679a9d9f4dba1688a1c2c2cfa307e085fd01764'
NEW_SHA='104e3d838978012ed8d56284d6bd550767e549b33f9a1d8e697c956c4ff74508'
EXPECTED={
 'wecom-mcp-zhycit-migration.service':(652135,'c8af1b1dd27f4dd0323eee95bd16258441dd0de7d016000f724b6e0a81ff0005'),
 'wecom-mcp-discovery-21462d2.service':(121481,'988055b38a6963afe1f6cfe3b2bdb9b691483cb599974a450efae535d356ad22'),
 'gnas-server.service':(487816,'a655935e3ab5cef0fb2aff9e2af770c0457c136cb77d3e0bee0f9de5a579553e'),
 'gnas-oauth-zhycit-next.service':(553837,'cc1a536c23715162b9c622a91fba1da0a4b36d3ac529d8982cc86b0347210ce1'),
}


def digest(data):return hashlib.sha256(data).hexdigest()


def trusted(path):
    while True:
        s=path.lstat()
        if not stat.S_ISDIR(s.st_mode) or s.st_uid!=0 or s.st_mode&0o022:raise ValueError('untrusted directory')
        if path==Path('/'):return
        path=path.parent


def fsyncdir(path):
    fd=os.open(path,os.O_RDONLY|os.O_DIRECTORY|os.O_NOFOLLOW)
    try:os.fsync(fd)
    finally:os.close(fd)


def snapshot():
    out={}
    for name,(expected_pid,expected_sha) in EXPECTED.items():
        raw=subprocess.check_output(['/usr/bin/systemctl','show',name,'-p','ActiveState','-p','MainPID','-p','NRestarts']).decode()
        v=dict(x.split('=',1) for x in raw.splitlines());pid=int(v['MainPID'])
        if v['ActiveState']!='active' or v['NRestarts']!='0' or pid!=expected_pid or digest((Path('/proc')/str(pid)/'exe').read_bytes())!=expected_sha:
            raise ValueError('production baseline changed')
        out[name]=pid
    return out


def main():
    if os.geteuid()!=0 or len(sys.argv)!=1:raise ValueError('root required')
    trusted(ROOT)
    fd=os.open(OLD,os.O_RDONLY|os.O_NOFOLLOW)
    with os.fdopen(fd,'rb') as f:
        s=os.fstat(f.fileno());named=OLD.lstat();data=f.read()
        if not stat.S_ISREG(s.st_mode) or s.st_uid!=0 or stat.S_IMODE(s.st_mode)!=0o555 or (s.st_dev,s.st_ino)!=(named.st_dev,named.st_ino) or digest(data)!=OLD_SHA:raise ValueError('installed controller changed')
    sys.path.insert(0,str(OLD));import same_unit_locks as locks, same_unit_plan as contract
    data=sys.stdin.buffer.read(1024*1024)
    if digest(data)!=NEW_SHA:raise ValueError('new controller digest mismatch')
    with locks.acquire(contract.RELEASE_LOCKS,bootstrap_legacy=True) as verify:
        before=snapshot()
        if NEW.exists() or NEW.is_symlink():
            s=NEW.lstat()
            if not stat.S_ISREG(s.st_mode) or s.st_uid!=0 or stat.S_IMODE(s.st_mode)!=0o555 or digest(NEW.read_bytes())!=NEW_SHA:raise ValueError('v2 file differs')
        else:
            tmp=ROOT/('.mcp-v2-'+secrets.token_hex(12));fd=os.open(tmp,os.O_WRONLY|os.O_CREAT|os.O_EXCL|os.O_NOFOLLOW,0o600)
            try:
                with os.fdopen(fd,'wb') as f:f.write(data);f.flush();os.fchmod(f.fileno(),0o555);os.fsync(f.fileno())
                try:os.link(tmp,NEW,follow_symlinks=False)
                except FileExistsError:
                    s=NEW.lstat()
                    if not stat.S_ISREG(s.st_mode) or s.st_uid!=0 or stat.S_IMODE(s.st_mode)!=0o555 or digest(NEW.read_bytes())!=NEW_SHA:raise ValueError('v2 file race')
                fsyncdir(ROOT)
            finally:tmp.unlink(missing_ok=True);fsyncdir(ROOT)
        if snapshot()!=before:raise ValueError('production changed during stage')
        verify()
    print(json.dumps({'state':'v2_controller_staged','sha256':NEW_SHA,'services_restarted':False,'routes_changed':False},sort_keys=True))


if __name__=='__main__':
    try:main()
    except Exception as error:
        print('MCP_V2_STAGE_REFUSED: '+type(error).__name__,file=sys.stderr);sys.exit(1)
