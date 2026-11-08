"""Strict, read-only validation of an externally issued release receipt."""
import datetime as dt
import json
import re
import stat

ID = re.compile(r'^APR-[0-9]{8}T[0-9]{6}Z-[A-Za-z0-9._-]{6,64}$')


def validate(path, approval_id, expected, *, now=None):
    if not ID.fullmatch(approval_id) or path.name != approval_id + '.json':
        raise ValueError('invalid approval id')
    if set(expected) & {'schema_version','approval_id','environment','approved_at','expires_at','approved_by'}:
        raise ValueError('invalid expected approval fields')
    info=path.lstat()
    if path.resolve()!=path or not stat.S_ISREG(info.st_mode) or info.st_uid!=0 or stat.S_IMODE(info.st_mode)!=0o400:
        raise ValueError('untrusted approval')
    value=json.loads(path.read_bytes())
    wanted={'schema_version':1,'approval_id':approval_id,'environment':'zhycit-prod-01',**expected}
    if set(value)!=set(wanted)|{'approved_at','expires_at','approved_by'} or any(value.get(k)!=v for k,v in wanted.items()):
        raise ValueError('approval does not bind action')
    try:
        start=dt.datetime.fromisoformat(value['approved_at'].replace('Z','+00:00'))
        end=dt.datetime.fromisoformat(value['expires_at'].replace('Z','+00:00'))
    except (ValueError,TypeError,AttributeError):
        raise ValueError('invalid approval time') from None
    now=now or dt.datetime.now(dt.timezone.utc)
    if not (start.tzinfo and end.tzinfo and start<=now<end and end-start<=dt.timedelta(hours=4)
            and isinstance(value['approved_by'],str) and value['approved_by'].strip()):
        raise ValueError('approval expired or invalid')
    return value
