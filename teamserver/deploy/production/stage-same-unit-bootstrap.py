#!/usr/bin/env python3
"""Send an exact verified package to the fixed production release directory."""
import argparse
import io
import json
import os
from pathlib import Path
import shlex
import subprocess
import tarfile

import same_unit_package as package
import same_unit_staging_remote as remote


def make_archive(directory):
    stream = io.BytesIO()
    with tarfile.open(fileobj=stream, mode='w') as archive:
        for name in sorted(package.FILES):
            data = (directory / name).read_bytes()
            info = tarfile.TarInfo(name)
            info.size = len(data)
            archive.addfile(info, io.BytesIO(data))
    return stream.getvalue()


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--candidate', required=True)
    args = parser.parse_args()
    directory = Path(args.candidate)
    manifest_sha = package.verify(directory, os.getuid())
    if manifest_sha != remote.MANIFEST_SHA:
        raise ValueError('unexpected candidate manifest')
    source = Path(remote.__file__)
    if source.is_symlink() or not source.is_file():
        raise ValueError('remote staging code unavailable')
    command = '/usr/bin/python3 -I -c ' + shlex.quote(source.read_text())
    try:
        result = subprocess.run(['ssh', '-o', 'BatchMode=yes', '-o', 'ConnectTimeout=8', 'zhycit.com', command],
                                input=make_archive(directory), check=True, stdout=subprocess.PIPE,
                                stderr=subprocess.PIPE, timeout=90)
    except (subprocess.CalledProcessError, subprocess.TimeoutExpired) as error:
        raise ValueError('staging result not confirmed; inspect the destination before retry') from error
    response = json.loads(result.stdout)
    if (response.get('state') != 'staged' or response.get('release_id') != remote.RID or
            response.get('manifest_sha256') != manifest_sha or response.get('services_restarted') is not False or
            response.get('routes_changed') is not False):
        raise ValueError('staging readback mismatch')
    print(json.dumps(response, sort_keys=True))


if __name__ == '__main__':
    main()
