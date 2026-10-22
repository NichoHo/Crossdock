# ANCHOR: verify
import hashlib
import hmac
import json
import pathlib
import sys
import time


def verify(secrets, timestamp, signature_header, body, now=None, window_s=300):
    """secrets: every currently valid signing secret (two during a rotation).
    body: the raw request body as bytes or str, before any JSON parsing."""
    now = time.time() if now is None else now
    try:
        ts = int(timestamp)
    except ValueError:
        return False
    if abs(now - ts) > window_s:
        return False
    raw = body.encode() if isinstance(body, str) else body
    for secret in secrets:
        mac = hmac.new(secret.encode(), f"{timestamp}.".encode() + raw, hashlib.sha256).hexdigest()
        want = "v1=" + mac
        if any(hmac.compare_digest(got, want) for got in signature_header.split(",")):
            return True
    return False
# ANCHOR_END: verify


# Self-check against docs/snippets/vector.json (run: python docs/snippets/verify.py).
if __name__ == "__main__":
    v = json.loads((pathlib.Path(__file__).parent / "vector.json").read_text())
    ok = verify([v["secret"]], v["timestamp"], v["signature"], v["body"], v["now"])
    tampered = verify([v["secret"]], v["timestamp"], v["signature"], v["body"] + " ", v["now"])
    stale = verify([v["secret"]], v["timestamp"], v["signature"], v["body"], v["now"] + 3600)
    rotated = verify(["old", v["secret"]], v["timestamp"], "v1=00," + v["signature"], v["body"], v["now"])
    if not ok or tampered or stale or not rotated:
        sys.exit(f"python: FAIL {ok=} {tampered=} {stale=} {rotated=}")
    print("python: ok")
