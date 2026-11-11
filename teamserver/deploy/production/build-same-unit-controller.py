#!/usr/bin/env python3
"""Create a deterministic isolated-mode controller archive; never installs it."""
import argparse
import hashlib
from pathlib import Path
import zipfile

HERE = Path(__file__).resolve().parent
FILES = ('same_unit_bootstrap_transaction.py', 'same_unit_guarded_ops.py', 'same_unit_journal.py',
         'same_unit_live_gates.py', 'same_unit_plan.py', 'same_unit_receipt.py', 'same_unit_systemd.py',
         'same_unit_authority.py', 'same_unit_locks.py', 'target_bootstrap_journal.py')


def build(path):
    path = Path(path)
    with path.open('xb') as output:
        with zipfile.ZipFile(output, 'w', compression=zipfile.ZIP_STORED) as archive:
            for name in ('same_unit_execution.py', *FILES):
                data = (HERE / name).read_bytes()
                info = zipfile.ZipInfo('__main__.py' if name == 'same_unit_execution.py' else name, (1980, 1, 1, 0, 0, 0))
                info.external_attr = 0o444 << 16
                archive.writestr(info, data)
    return hashlib.sha256(path.read_bytes()).hexdigest()


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('--output', required=True)
    args = parser.parse_args()
    print(build(args.output))
