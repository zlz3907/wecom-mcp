"""Immutable plan-bound records for same-unit bootstrap and recovery."""
import json
import re
import secrets


def record(plan_sha, state):
    if not re.fullmatch(r'[0-9a-f]{64}', plan_sha) or state not in ('prepared', 'committed', 'closed', 'incident'):
        raise ValueError('invalid transaction record')
    return (json.dumps({'schema_version': 1, 'plan_sha256': plan_sha, 'state': state},
                       sort_keys=True, separators=(',', ':')) + '\n').encode()


class Records:
    def __init__(self, journal, plan_sha):
        self.journal, self.plan_sha = journal, plan_sha
        record(plan_sha, 'prepared')

    def prepare(self):
        self.journal.write_once('prepare.json', record(self.plan_sha, 'prepared'))

    def verify_prepared(self):
        if self.journal.read('prepare.json') != record(self.plan_sha, 'prepared'):
            raise ValueError('prepared transaction missing or mismatched')

    def terminal_state(self):
        terminal = self.journal.read('terminal.json')
        if terminal is None:
            return 'none'
        self.verify_prepared()
        for state in ('committed', 'closed'):
            if terminal == record(self.plan_sha, state):
                return state
        raise ValueError('terminal transaction record mismatched')

    def terminal(self, state):
        if state not in ('committed', 'closed'):
            raise ValueError('invalid terminal state')
        self.verify_prepared()
        self.journal.write_once('terminal.json', record(self.plan_sha, state))

    def incident(self):
        self.verify_prepared()
        self.journal.write_once('incident-' + secrets.token_hex(12) + '.json', record(self.plan_sha, 'incident'))
