#!/usr/bin/env python3
"""Bounded Dovecot spool adapter. Run periodically, with a protected secret file."""
import fcntl
import hashlib
import importlib.util
import json
import math
import os
from pathlib import Path
import stat
import time

spec = importlib.util.spec_from_file_location('sender', Path(__file__).with_name('send-mail-event.py'))
sender = importlib.util.module_from_spec(spec)
spec.loader.exec_module(sender)

def payload(raw):
    event = json.loads(raw)
    if not isinstance(event, dict) or set(event) != {'mailbox', 'validity', 'lastUid'}:
        raise ValueError('Invalid event fields')
    owner = event['mailbox']
    if not isinstance(owner, str) or len(owner) > 254 or '@' not in owner or any(ord(c) < 32 for c in owner):
        raise ValueError('Invalid mailbox')
    for field in ('validity', 'lastUid'):
        value = event[field]
        # Accept already-spooled whole-number doubles from Dovecot/Lua, but
        # reject booleans, fractional/nonfinite numbers and out-of-range UIDs.
        if type(value) is float and math.isfinite(value) and value.is_integer():
            value = int(value)
        if type(value) is not int or not 0 < value <= 4294967295:
            raise ValueError('Invalid cursor')
        event[field] = value
    event['mailbox'] = owner.lower()
    canonical = json.dumps(event, sort_keys=True, separators=(',', ':')).encode()
    event['id'] = hashlib.sha256(canonical).hexdigest()
    return json.dumps(event, sort_keys=True, separators=(',', ':')).encode()

def main():
    spool = Path(os.environ['COMPANION_EVENT_SPOOL'])
    # The lock lives outside the mail-process-writable spool.
    with open(os.environ['COMPANION_EVENT_LOCK'], 'a') as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            return
        deadline = time.monotonic() + 45
        sent = expired = invalid = 0
        with os.scandir(spool) as entries:
            for index, entry in enumerate(entries):
                if index >= 500 or time.monotonic() >= deadline:
                    break
                if not entry.name.endswith(('.json', '.tmp')) or not entry.is_file(follow_symlinks=False):
                    continue
                info = entry.stat(follow_symlinks=False)
                age = time.time() - info.st_mtime
                if age > 3600:
                    os.unlink(entry.path)
                    expired += 1
                    continue
                if not entry.name.endswith('.json'):
                    continue
                try:
                    fd = os.open(entry.path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
                    with os.fdopen(fd, 'rb') as file:
                        if not stat.S_ISREG(os.fstat(file.fileno()).st_mode):
                            continue
                        raw = file.read(8193)
                    if len(raw) > 8192:
                        raise ValueError('Oversized event')
                    body = payload(raw)
                except (ValueError, UnicodeError):
                    os.unlink(entry.path)
                    invalid += 1
                    continue
                try:
                    sender.submit(body)
                except Exception:
                    print('Event endpoint unavailable; remaining events retained for retry.')
                    break
                os.unlink(entry.path)
                sent += 1
        if sent or expired or invalid:
            print(f'Events accepted={sent}, expired={expired}, invalid={invalid}')

if __name__ == '__main__':
    main()
