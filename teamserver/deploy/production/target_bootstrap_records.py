"""Bind bootstrap recovery records to one exact original route.

The caller must hold the production release locks and use a trusted Journal.
These helpers do not authorize or perform a production switch.
"""
import json

import target_bootstrap_recovery as routes


def _record(intent, state):
    if state not in ('prepared', 'committed', 'closed'):
        raise ValueError('invalid record state')
    return (json.dumps({'intent': intent, 'state': state}, sort_keys=True,
                       separators=(',', ':')) + '\n').encode()


class Records:
    def __init__(self, journal):
        self.journal = journal

    def prepare(self, old, intent):
        if routes.make_intent(old) != intent:
            raise ValueError('intent does not bind original route')
        # The route must be durable before the prepare record can be visible.
        self.journal.write_once('old-route.conf', old)
        self.journal.write_once('prepare.json', _record(intent, 'prepared'))

    def read_prepared(self):
        old = self.journal.read('old-route.conf')
        prepared = self.journal.read('prepare.json')
        if old is None and prepared is None:
            return None
        if old is None or prepared is None:
            raise ValueError('incomplete prepare record')
        intent = routes.make_intent(old)
        if prepared != _record(intent, 'prepared'):
            raise ValueError('prepare record does not bind original route')
        return old, intent

    def terminal_state(self, intent):
        terminal = self.journal.read('terminal.json')
        if terminal is None:
            return 'none'
        prepared = self.read_prepared()
        if prepared is None or prepared[1] != intent:
            raise ValueError('terminal has no matching prepare record')
        for state in ('committed', 'closed'):
            if terminal == _record(intent, state):
                return state
        raise ValueError('terminal record conflicts or is malformed')

    def write_terminal(self, intent, state):
        if state not in ('committed', 'closed'):
            raise ValueError('invalid terminal state')
        prepared = self.read_prepared()
        if prepared is None or prepared[1] != intent:
            raise ValueError('missing matching prepare record')
        self.journal.write_once('terminal.json', _record(intent, state))
