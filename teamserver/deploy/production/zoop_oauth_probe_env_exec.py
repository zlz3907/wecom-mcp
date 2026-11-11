#!/usr/bin/python3 -I
"""Start an unexposed OAuth recovery probe using the existing vetted loader."""
import hashlib
import importlib.util
import os
from pathlib import Path
import re
import stat
import sys

SELF = Path('/usr/local/sbin/zoop-oauth-probe-env-exec.py')
BASE = Path('/usr/local/sbin/gnas-isolated-env-exec.py')
GRANT = Path('/etc/gnas/oauth21/isolated-target-grant-v2.json')


def require(ok):
    if not ok: raise ValueError('invalid probe')


def trusted(path, directory=False):
    value = path.lstat()
    require(path.resolve() == path and value.st_uid == 0 and not value.st_mode & 0o022)
    require(stat.S_ISDIR(value.st_mode) if directory else stat.S_ISREG(value.st_mode))


def prepare(argv):
    require(len(argv) == 6 and re.fullmatch(r'[a-f0-9]{64}', argv[4]) is not None)
    require(argv[5] == '7505')
    for p in (Path('/usr'), Path('/usr/local'), SELF.parent, Path('/etc'), Path('/etc/gnas'), GRANT.parent): trusted(p, True)
    for p in (SELF, BASE, GRANT): trusted(p)
    require(stat.S_IMODE(GRANT.stat().st_mode) == 0o400)
    raw = GRANT.read_bytes()
    require(len(raw) <= 16384 and hashlib.sha256(raw).hexdigest() == argv[4])
    spec = importlib.util.spec_from_file_location('isolated_env', BASE)
    helper = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(helper)
    binary, env = helper.prepare(*argv[:4])
    helper.add_target_grant(env, raw)
    env['GNAS_OAUTH21_ISOLATED_LISTEN'] = '127.0.0.1:' + argv[5]
    return binary, env


def main():
    require(os.geteuid() == 0 and Path(__file__).resolve() == SELF)
    binary, env = prepare(sys.argv[1:])
    os.execve(binary, [str(binary)], env)


if __name__ == '__main__':
    try: main()
    except Exception:
        print('isolated OAuth probe refused', file=sys.stderr)
        sys.exit(1)
