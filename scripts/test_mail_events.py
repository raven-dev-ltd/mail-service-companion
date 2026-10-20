import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
import os

spec = importlib.util.spec_from_file_location('drain', Path(__file__).with_name('drain-mail-events.py'))
drain = importlib.util.module_from_spec(spec)
spec.loader.exec_module(drain)

class EventsTest(unittest.TestCase):
    event = {'mailbox': 'User@example.com', 'validity': 123, 'lastUid': 456}

    def test_stable_id(self):
        first = drain.payload(json.dumps(self.event))
        second = drain.payload(json.dumps(dict(reversed(list(self.event.items())))))
        self.assertEqual(first, second)
        self.assertEqual(len(json.loads(first)['id']), 64)

    def test_invalid_cursor_and_extra_data(self):
        for changes in ({'lastUid': 0}, {'validity': True}, {'password': 'not-allowed'}, {'lastUid': 2**32}, {'lastUid': 11.5}, {'lastUid': float('nan')}, {'lastUid': float('inf')}):
            with self.assertRaises(ValueError):
                drain.payload(json.dumps(self.event | changes))

    def test_dovecot_float_cursor_is_normalized_before_signing(self):
        actual = json.loads(drain.payload(json.dumps(self.event | {'lastUid': 11.0})))
        self.assertEqual(actual['lastUid'], 11)
        self.assertIs(type(actual['lastUid']), int)
        self.assertEqual(drain.payload(json.dumps(self.event | {'lastUid': 11})), drain.payload(json.dumps(self.event | {'lastUid': 11.0})))

    def test_retry_and_acceptance(self):
        with tempfile.TemporaryDirectory() as root:
            spool = Path(root) / 'spool'
            spool.mkdir()
            event = spool / 'delivery.json'
            event.write_text(json.dumps(self.event))
            outside = Path(root) / 'private'
            outside.write_text('untouched')
            (spool / 'link.json').symlink_to(outside)
            env = {'COMPANION_EVENT_SPOOL': str(spool), 'COMPANION_EVENT_LOCK': str(Path(root) / 'lock')}
            with patch.dict(os.environ, env), patch.object(drain.sender, 'submit', side_effect=RuntimeError):
                drain.main()
            self.assertTrue(event.exists())
            with patch.dict(os.environ, env), patch.object(drain.sender, 'submit') as submit:
                drain.main()
                submit.assert_called_once()
            self.assertFalse(event.exists())
            self.assertEqual(outside.read_text(), 'untouched')

if __name__ == '__main__':
    unittest.main()
