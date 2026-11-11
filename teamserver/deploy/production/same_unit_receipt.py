"""Validate and consume an exact, independently issued release receipt."""
import datetime as dt
import hashlib
import json
import os
from pathlib import Path
import re
import stat


def _trusted_directory(path, root, owner):
    if (not path.is_absolute() or not root.is_absolute() or
            os.path.normpath(path) != str(path) or os.path.normpath(root) != str(root)):
        raise ValueError('relative approval directory')
    current = path
    while True:
        info = current.lstat()
        if not stat.S_ISDIR(info.st_mode) or info.st_uid != owner or info.st_mode & 0o022:
            raise ValueError('untrusted approval directory')
        if current == root:
            return
        if current == current.parent:
            raise ValueError('approval outside trusted root')
        current = current.parent


def _read_regular(path, root, owner):
    _trusted_directory(path.parent, root, owner)
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    with os.fdopen(fd, 'rb') as stream:
        info = os.fstat(stream.fileno())
        named = path.lstat()
        if (not stat.S_ISREG(info.st_mode) or info.st_uid != owner or
                stat.S_IMODE(info.st_mode) != 0o400 or
                (info.st_dev, info.st_ino) != (named.st_dev, named.st_ino)):
            raise ValueError('untrusted approval file')
        data = stream.read(65537)
        after = os.fstat(stream.fileno())
        if len(data) > 65536 or (info.st_size, info.st_mtime_ns) != (after.st_size, after.st_mtime_ns):
            raise ValueError('approval file changed')
    _trusted_directory(path.parent, root, owner)
    return data


def _json(data):
    def unique(pairs):
        result = {}
        for key, value in pairs:
            if key in result:
                raise ValueError('duplicate approval field')
            result[key] = value
        return result
    def nonfinite(_value):
        raise ValueError('non-finite approval value')
    return json.loads(data, object_pairs_hook=unique, parse_constant=nonfinite)


def validate(receipt_path, plan_path, approval_id, action, root=Path('/'), owner=0, now=None):
    if (not re.fullmatch(r'APR-[0-9]{8}T[0-9]{6}Z-[A-Za-z0-9._-]{6,64}', approval_id)
            or receipt_path.name != approval_id + '.json'
            or action not in ('deploy-mcp-open-bootstrap', 'recover-mcp-open-bootstrap')):
        raise ValueError('invalid approval identity')
    plan_data = _read_regular(plan_path, root, owner)
    receipt_data = _read_regular(receipt_path, root, owner)
    plan = _json(plan_data)
    receipt = _json(receipt_data)
    expected = {'schema_version', 'environment', 'action', 'approval_id', 'plan_sha256',
                'approved_by', 'approved_at', 'expires_at'}
    if (not isinstance(plan, dict) or not isinstance(receipt, dict) or set(receipt) != expected or
            receipt['schema_version'] != 1 or receipt['environment'] != 'zhycit-prod-01' or
            receipt['action'] != action or receipt['approval_id'] != approval_id or
            receipt['plan_sha256'] != hashlib.sha256(plan_data).hexdigest() or
            not isinstance(receipt['approved_by'], str) or not receipt['approved_by'].strip()):
        raise ValueError('approval binding mismatch')
    try:
        start = dt.datetime.fromisoformat(receipt['approved_at'].replace('Z', '+00:00'))
        end = dt.datetime.fromisoformat(receipt['expires_at'].replace('Z', '+00:00'))
        instant = now or dt.datetime.now(dt.timezone.utc)
        if not start.tzinfo or not end.tzinfo or not instant.tzinfo or not start <= instant < end or not dt.timedelta(0) < end - start <= dt.timedelta(hours=4):
            raise ValueError('approval expired or invalid')
    except (TypeError, AttributeError) as error:
        raise ValueError('invalid approval time') from error
    return plan, receipt['plan_sha256']


def consume(used_directory, approval_id, plan_sha, root=Path('/'), owner=0):
    if (not re.fullmatch(r'APR-[0-9]{8}T[0-9]{6}Z-[A-Za-z0-9._-]{6,64}', approval_id)
            or not re.fullmatch(r'[0-9a-f]{64}', plan_sha)):
        raise ValueError('invalid consumption binding')
    _trusted_directory(used_directory, root, owner)
    directory = os.open(used_directory, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        opened, named = os.fstat(directory), used_directory.lstat()
        if (opened.st_dev, opened.st_ino) != (named.st_dev, named.st_ino):
            raise ValueError('used directory changed')
        fd = os.open(approval_id, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o400, dir_fd=directory)
        with os.fdopen(fd, 'wb') as marker:
            marker.write((plan_sha + '\n').encode())
            marker.flush()
            os.fsync(marker.fileno())
        os.fsync(directory)
    finally:
        os.close(directory)


def verify_consumed(receipt_path, plan_path, used_directory, approval_id, root=Path('/'), owner=0):
    """Permit bounded recovery of an already consumed deployment after expiry."""
    original = _json(_read_regular(receipt_path, root, owner))
    try:
        approved_at = dt.datetime.fromisoformat(original['approved_at'].replace('Z', '+00:00'))
    except (KeyError, TypeError, AttributeError, ValueError) as error:
        raise ValueError('invalid original approval time') from error
    plan, plan_sha = validate(receipt_path, plan_path, approval_id, 'deploy-mcp-open-bootstrap',
                              root=root, owner=owner, now=approved_at)
    if _read_regular(used_directory / approval_id, root, owner) != (plan_sha + '\n').encode():
        raise ValueError('consumed approval binding mismatch')
    return plan, plan_sha
