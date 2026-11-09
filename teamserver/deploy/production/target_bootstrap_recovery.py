"""Recovery policy for the target-only open-bootstrap switch.

This module performs no I/O and grants no production authority. A production
controller must persist its intent durably and hold its release locks before
using these decisions. The old target must never be re-enabled automatically
after a switch intent because it does not reject paused users on every tool.
"""
import hashlib

OLD_PORT = b'proxy_pass http://127.0.0.1:7709;'
NEW_PORT = b'proxy_pass http://127.0.0.1:7710;'


def route_variants(old):
    if not isinstance(old, bytes) or old.count(OLD_PORT) != 2 or NEW_PORT in old:
        raise ValueError('unrecognized target route')
    new = old.replace(OLD_PORT, NEW_PORT)
    closed = old.replace(OLD_PORT, b'return 503;')
    if new.count(NEW_PORT) != 2 or closed.count(b'return 503;') < 2:
        raise ValueError('invalid route transformation')
    return new, closed


def digest(data):
    return hashlib.sha256(data).hexdigest()


def recovery_action(intent, current_route, candidate_ready):
    """Return the only permitted action after an already durable intent.

    The caller must stop/disable the old service before executing the action,
    revalidate the route under the lock, and never restore old traffic.
    Unknown route content fails without overwriting another operator's changes.
    """
    if not isinstance(intent, dict) or set(intent) != {'old', 'new', 'closed'}:
        raise ValueError('invalid durable intent')
    if not isinstance(current_route, bytes) or not isinstance(candidate_ready, bool):
        raise ValueError('invalid recovery input')
    if any(not isinstance(value, str) or len(value) != 64 or any(c not in '0123456789abcdef' for c in value)
           for value in intent.values()):
        raise ValueError('invalid route fingerprint')
    if len(set(intent.values())) != 3:
        raise ValueError('ambiguous route fingerprints')
    current = digest(current_route)
    if current not in intent.values():
        raise ValueError('route changed outside this switch')
    if candidate_ready:
        return 'verify_candidate' if current == intent['new'] else 'route_candidate'
    return 'verify_closed' if current == intent['closed'] else 'route_closed'


def make_intent(old):
    new, closed = route_variants(old)
    return {'old': digest(old), 'new': digest(new), 'closed': digest(closed)}
