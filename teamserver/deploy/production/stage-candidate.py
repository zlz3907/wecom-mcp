#!/usr/bin/env python3
"""Upload only after controller installation; delegate promotion to that controller."""
import argparse
import hashlib
import json
from pathlib import Path
import re
import subprocess
import tarfile
import tempfile


def require(ok, message):
    if not ok: raise ValueError(message)

def main():
    p=argparse.ArgumentParser()
    p.add_argument('--candidate',required=True)
    a=p.parse_args();directory=Path(a.candidate).resolve()
    names=('wecom-mcp-team','discovery-policy.json','service.conf','recovery.conf','manifest.json','SHA256SUMS')
    require({f.name for f in directory.iterdir()}==set(names),'unexpected candidate contents')
    require(all((directory/n).is_file() and not (directory/n).is_symlink() for n in names),'regular artifacts only')
    def sha(p):return hashlib.sha256(p.read_bytes()).hexdigest()
    require((directory/'SHA256SUMS').read_text()==''.join(sha(directory/n)+'  '+n+'\n' for n in sorted(names[:-1])),'checksum mismatch')
    m=json.loads((directory/'manifest.json').read_text());rid=m['release_id']
    require(re.fullmatch(r'\d{8}T\d{6}Z-[a-f0-9]{12}',rid),'invalid release ID')
    require(m.get('schema_version')==2 and m.get('ci_url','').startswith('https://'),'schema v2 and successful same-commit CI required before upload')
    require(subprocess.check_output(['ssh','-o','BatchMode=yes','zhycit.com','id -u'],text=True).strip()=='0','fixed staging SSH account must be root')
    raw=subprocess.check_output(['ssh','-o','BatchMode=yes','zhycit.com','sudo /usr/local/sbin/wecom-mcp-release-controller status'],text=True)
    current=json.loads(raw)
    require(all(current[k]==m['expected_'+k] for k in ('runtime_path','binary_sha256','unit_fingerprint','runtime_config_fingerprint')),'live baseline drift')
    require(current.get('runtime_verified') is True and current['active']=='active' and current['restarts']==0,'unverified or unhealthy baseline')
    with tempfile.TemporaryFile() as archive:
        with tarfile.open(fileobj=archive,mode='w') as tar:
            for name in names:tar.add(directory/name,arcname=name,recursive=False)
        archive.seek(0)
        base='/var/lib/wecom-mcp-release/incoming/'+rid
        command="set -eu; umask 077; test ! -e '{0}'; test ! -e '{0}.uploading'; mkdir '{0}.uploading'; tar -xf - --no-same-owner -C '{0}.uploading'; cd '{0}.uploading'; sha256sum --check --status SHA256SUMS; chmod 0500 wecom-mcp-team; chmod 0400 manifest.json discovery-policy.json service.conf recovery.conf SHA256SUMS; chmod 0500 .; mv '{0}.uploading' '{0}'".format(base)
        subprocess.run(['ssh','-o','BatchMode=yes','zhycit.com',command],stdin=archive,check=True)
    subprocess.run(['ssh','-o','BatchMode=yes','zhycit.com','sudo /usr/local/sbin/wecom-mcp-release-controller stage '+rid],check=True)


if __name__=='__main__':main()
