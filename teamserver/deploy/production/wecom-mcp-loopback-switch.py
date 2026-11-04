#!/usr/bin/python3 -I
"""Root-only atomic selector for a pre-provisioned loopback gateway.

It never reads or edits Nginx.  The gateway must already be configured to
consume this exact root-owned receipt before this tool is installed.
"""
import argparse
import json
import os
from pathlib import Path
import stat
import sys

COLORS = {'green': '127.0.0.1:7702', 'blue': '127.0.0.1:7703'}
DEFAULT = Path('/var/lib/wecom-mcp-release/entrypoint/active.json')


def require(ok, message):
    if not ok:
        raise ValueError(message)


def regular(path):
    value = path.lstat()
    require(stat.S_ISREG(value.st_mode) and path.resolve() == path and value.st_uid == 0 and not value.st_mode & 0o022, 'untrusted active receipt')


def receipt(path):
    regular(path)
    value = json.loads(path.read_text())
    require(value == {'version': 1, 'color': value.get('color'), 'loopback': COLORS.get(value.get('color'))}, 'invalid active receipt')
    return value


def atomic(path, value):
    temp = path.with_name(path.name + '.new')
    with temp.open('xb') as f:
        os.chmod(temp, 0o400)
        f.write(json.dumps(value, sort_keys=True, separators=(',', ':')).encode())
        f.flush()
        os.fsync(f.fileno())
    os.replace(temp, path)
    directory = os.open(str(path.parent), os.O_RDONLY)
    try:
        os.fsync(directory)
    finally:
        os.close(directory)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--active-file', required=True)
    parser.add_argument('--expected-color', required=True, choices=COLORS)
    parser.add_argument('--to', required=True, choices=COLORS)
    args = parser.parse_args()
    require(os.geteuid() == 0, 'root required')
    path = Path(args.active_file)
    require(path == DEFAULT and path.is_absolute() and path.parent.is_dir() and path.parent.resolve() == path.parent, 'fixed active receipt required')
    current = receipt(path)
    require(current['color'] == args.expected_color and args.expected_color != args.to, 'entrypoint changed concurrently or no-op')
    atomic(path, {'version': 1, 'color': args.to, 'loopback': COLORS[args.to]})
    require(receipt(path)['color'] == args.to, 'atomic switch readback failed')
    print(json.dumps({'state': 'switched', 'color': args.to}, sort_keys=True))


if __name__ == '__main__':
    try:
        main()
    except Exception as error:
        print('LOOPBACK_SWITCH_REFUSED: ' + str(error) if isinstance(error, ValueError) else 'LOOPBACK_SWITCH_REFUSED: controlled operation failed', file=sys.stderr)
        sys.exit(1)
