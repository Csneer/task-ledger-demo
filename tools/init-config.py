#!/usr/bin/env python3
"""Create owner-private bootstrap files in a NEW directory; no daemon is started."""
import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import secrets


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--dir", type=Path, required=True)
    parser.add_argument("--authority", default="personal")
    parser.add_argument("--port", type=int, default=8090)
    args = parser.parse_args()
    if not args.authority or len(args.authority) > 256:
        parser.error("authority must contain 1..256 characters")
    if not 0 <= args.port <= 65535:
        parser.error("port must be between 0 and 65535")
    target = args.dir.absolute()
    os.umask(0o077)
    target.mkdir(mode=0o700)  # Exclusive: reject existing paths, including symlinks.
    secret = secrets.token_bytes(32)
    owner = {"kind": "human", "id": "owner"}
    files = {
        "server.json": {"authority": args.authority, "database": "ledger.db",
                        "listen": f"127.0.0.1:{args.port}", "operators": [owner],
                        "credentials": [{"digest": hashlib.sha256(secret).hexdigest(),
                                         "actor": owner, "read": True, "write": True, "admin": True}]},
        "owner-client.json": {"service_url": f"http://127.0.0.1:{args.port}",
                              "credential_file": "owner-token", "intent_dir": "intents"},
    }
    for name, body in files.items():
        with (target / name).open("x") as stream:
            stream.write(json.dumps(body, indent=2) + "\n")
            stream.flush()
            os.fsync(stream.fileno())
    with (target / "owner-token").open("x") as stream:
        stream.write(base64.urlsafe_b64encode(secret).decode().rstrip("=") + "\n")
        stream.flush()
        os.fsync(stream.fileno())
    (target / "intents").mkdir(mode=0o700)
    descriptor = os.open(target, os.O_RDONLY | os.O_DIRECTORY)
    try:
        os.fsync(descriptor)
    finally:
        os.close(descriptor)
    print(json.dumps({"directory": str(target), "config": str(target / "server.json"),
                      "credential_file": str(target / "owner-token"), "initialized": False,
                      "note": "Initialize once with taskledgerd -config CONFIG -init. Port 0 is for smoke tests; configure the assigned port before client use."}))


if __name__ == "__main__":
    main()
