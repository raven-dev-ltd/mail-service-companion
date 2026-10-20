#!/usr/bin/env python3
"""Submit one INBOX delivery event from stdin. No mailbox password is needed."""
import hashlib
import hmac
import json
import os
from pathlib import Path
import sys
import time
import urllib.parse
import urllib.request

class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args, **kwargs):
        return None

def submit(raw):
    endpoint = os.environ["COMPANION_EVENT_URL"]
    url = urllib.parse.urlsplit(endpoint)
    if url.scheme != "https" or not url.hostname or url.username or url.password or url.query or url.fragment:
        raise ValueError("Use a fixed HTTPS event endpoint without credentials")
    secret = Path(os.environ["COMPANION_EVENT_SECRET_FILE"]).read_bytes().strip()
    if len(secret) < 32:
        raise ValueError("Event secret must be at least 32 bytes")
    if len(raw) > 8192:
        raise ValueError("Event exceeds 8 KiB")
    json.loads(raw)
    timestamp = str(int(time.time()))
    signature = hmac.new(secret, timestamp.encode() + b"." + raw, hashlib.sha256).hexdigest()
    request = urllib.request.Request(endpoint, data=raw, method="POST", headers={
        "Content-Type": "application/json",
        "X-Companion-Timestamp": timestamp,
        "X-Companion-Signature": signature,
    })
    with urllib.request.build_opener(NoRedirect()).open(request, timeout=20) as response:
        if response.status != 202:
            raise RuntimeError("Event was not accepted")

def main():
    submit(sys.stdin.buffer.read(8193))
    print("Event accepted")

if __name__ == "__main__":
    try:
        main()
    except Exception:
        print("Event submission failed; check server configuration and retry with the same event ID.", file=sys.stderr)
        sys.exit(1)
