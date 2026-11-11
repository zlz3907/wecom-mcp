import unittest

import target_bootstrap_recovery as r


class TargetBootstrapRecoveryTest(unittest.TestCase):
    def setUp(self):
        self.old = b'location /mcp { ' + r.OLD_PORT + b' }\nlocation /auth { ' + r.OLD_PORT + b' }\n'
        self.new, self.closed = r.route_variants(self.old)
        self.intent = r.make_intent(self.old)

    def test_every_crash_route_never_restores_old_service(self):
        for route in (self.old, self.new, self.closed):
            with self.subTest(route=route):
                self.assertIn(r.recovery_action(self.intent, route, True), ('route_candidate', 'verify_candidate'))
                self.assertIn(r.recovery_action(self.intent, route, False), ('route_closed', 'verify_closed'))

    def test_unknown_route_is_not_overwritten(self):
        with self.assertRaises(ValueError):
            r.recovery_action(self.intent, b'other operator changed routing', True)
        with self.assertRaises(ValueError):
            r.recovery_action({'old': self.intent['old'], 'new': self.intent['old'], 'closed': self.intent['closed']}, self.old, True)

    def test_candidate_and_closed_routes_are_exact(self):
        self.assertEqual(self.new.count(r.NEW_PORT), 2)
        self.assertNotIn(r.OLD_PORT, self.new)
        self.assertNotIn(r.OLD_PORT, self.closed)
        self.assertNotIn(r.NEW_PORT, self.closed)

    def test_malformed_or_partial_routes_fail_closed(self):
        for route in (b'', r.OLD_PORT, r.OLD_PORT * 3, r.OLD_PORT * 2 + r.NEW_PORT):
            with self.subTest(route=route), self.assertRaises(ValueError):
                r.make_intent(route)


if __name__ == '__main__':
    unittest.main()
