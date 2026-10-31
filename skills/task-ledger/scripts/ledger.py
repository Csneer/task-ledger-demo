#!/usr/bin/env python3
"""Small protocol client: preserve request bytes and persist write intent first."""
import argparse
import base64
import decimal
import hashlib
import ipaddress
import json
import os
from pathlib import Path
import secrets
import ssl
import stat
import sys
import tempfile
import urllib.error
import urllib.parse
import urllib.request

MAX_REQUEST = 1 << 20
MAX_RESPONSE = 16 << 20


def strict_json(raw):
    def pairs(items):
        result = {}
        for key, value in items:
            if key in result:
                raise ValueError("duplicate JSON key")
            result[key] = value
        return result

    def bad_constant(_):
        raise ValueError("non-finite JSON number")

    value = json.loads(raw.decode("utf-8"), object_pairs_hook=pairs,
                       parse_float=decimal.Decimal, parse_constant=bad_constant)

    def strings(v):
        if isinstance(v, str):
            v.encode("utf-8")
        elif isinstance(v, list):
            for item in v:
                strings(item)
        elif isinstance(v, dict):
            for key, item in v.items():
                strings(key)
                strings(item)
    strings(value)
    if not isinstance(value, dict):
        raise ValueError("JSON object required")
    return value


def private_read(path):
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    try:
        info = os.fstat(fd)
        if not stat.S_ISREG(info.st_mode) or info.st_mode & 0o077 or info.st_uid != os.getuid():
            raise ValueError("private files must be owner-owned regular files with mode 0600")
        with os.fdopen(fd, "rb", closefd=False) as source:
            raw = source.read(MAX_REQUEST + 1)
        if len(raw) > MAX_REQUEST:
            raise ValueError("private file exceeds 1 MiB")
        return raw
    finally:
        os.close(fd)


def durable_new(path, raw):
    """Publish a complete file once; concurrent identical calls are safe."""
    path = Path(path)
    path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    info = path.parent.lstat()
    if not stat.S_ISDIR(info.st_mode) or info.st_mode & 0o077 or info.st_uid != os.getuid():
        raise ValueError("state directory must be owner-owned with mode 0700")
    fd, temporary = tempfile.mkstemp(prefix=".pending-", dir=path.parent)
    try:
        with os.fdopen(fd, "wb") as target:
            target.write(raw)
            target.flush()
            os.fsync(target.fileno())
        try:
            os.link(temporary, path)
        except FileExistsError:
            if private_read(path) != raw:
                raise ValueError("intent ID already has different request bytes; keep the original request")
        directory = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    finally:
        os.unlink(temporary)


def token_bytes(text):
    if not isinstance(text, str) or len(text) != 43:
        raise ValueError("credential/proof must encode exactly 32 random bytes")
    try:
        raw = base64.b64decode(text + "=", altchars=b"-_", validate=True)
    except (ValueError, base64.binascii.Error):
        raise ValueError("invalid credential/proof encoding") from None
    if len(raw) != 32 or base64.urlsafe_b64encode(raw).decode().rstrip("=") != text:
        raise ValueError("non-canonical credential/proof encoding")
    return raw


def session(path):
    value = strict_json(private_read(path))
    if set(value) != {"session_id", "secret"} or not isinstance(value["session_id"], str) or not value["session_id"]:
        raise ValueError("session file requires session_id and secret")
    token_bytes(value["secret"])
    return value


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def send(config_path, route, request_path, session_path=None, target_path=None, timeout=10):
    config_path = Path(config_path).resolve()
    config = strict_json(private_read(config_path))
    if set(config) - {"service_url", "credential_file", "ca_file", "intent_dir"}:
        raise ValueError("unknown config field")
    url = config.get("service_url", "")
    parsed = urllib.parse.urlsplit(url)
    if parsed.scheme not in {"http", "https"} or not parsed.hostname or parsed.username or parsed.password or parsed.query or parsed.fragment or parsed.path not in {"", "/"}:
        raise ValueError("service_url must be an HTTP(S) origin without userinfo, query or path")
    if parsed.scheme == "http":
        try:
            loopback = ipaddress.ip_address(parsed.hostname).is_loopback
        except ValueError:
            loopback = False
        if not loopback:
            raise ValueError("plain HTTP requires an explicit loopback IP; use HTTPS otherwise")
    url = url.rstrip("/")

    def referenced(name, default=None):
        value = config.get(name, default)
        if not isinstance(value, str) or not value:
            raise ValueError("missing configuration path: " + name)
        p = Path(value).expanduser()
        return p if p.is_absolute() else config_path.parent / p

    credential = private_read(referenced("credential_file")).decode("ascii").strip()
    token_bytes(credential)
    with open(request_path, "rb") as source:
        raw = source.read(MAX_REQUEST + 1)
    if len(raw) > MAX_REQUEST:
        raise ValueError("request exceeds 1 MiB")
    request = strict_json(raw)
    if request.get("binding_version") != "ledger-http-draft-1":
        raise ValueError("unsupported binding_version")
    if route != "queries":
        identity = request.get("command", {}).get("command_id") if route == "commands" else request.get("request_id")
        if not isinstance(identity, str) or not identity or len(identity) > 256:
            raise ValueError("write request requires a nonempty command_id/request_id")
        # A config path identifies the local installation through credential
        # rotation. The server still owns actor-scoped idempotency and authority.
        key = hashlib.sha256(json.dumps([str(config_path), url, route, identity]).encode()).hexdigest()
        durable_new(referenced("intent_dir", "intents") / (key + ".request.json"), raw)
    headers = {"Authorization": "Bearer " + credential, "Content-Type": "application/json"}
    hidden = [credential]
    if session_path:
        own = session(session_path)
        headers["X-Ledger-Session"] = own["session_id"]
        headers["X-Ledger-Session-Proof"] = own["secret"]
        hidden.append(own["secret"])
    if target_path:
        target = session(target_path)
        headers["X-Ledger-Target-Session-Proof"] = target["secret"]
        hidden.append(target["secret"])
    context = ssl.create_default_context(cafile=str(referenced("ca_file")) if "ca_file" in config else None)
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect(), urllib.request.HTTPSHandler(context=context))
    req = urllib.request.Request(url + "/v1/" + route, data=raw, headers=headers, method="POST")
    try:
        response = opener.open(req, timeout=timeout)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        body = response.read(MAX_RESPONSE + 1)
        code = response.code
    if len(body) > MAX_RESPONSE:
        raise ValueError("response exceeds 16 MiB; write outcome is unknown, retain original intent")
    for secret in hidden:
        body = body.replace(secret.encode(), b"[REDACTED]")
    return code, body


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="mode", required=True)
    for mode in ("query", "command", "action"):
        p = sub.add_parser(mode)
        p.add_argument("--config", required=True)
        p.add_argument("--request", required=True, help="complete JSON envelope; sent byte-for-byte")
        p.add_argument("--session-file")
        p.add_argument("--target-session-file")
        p.add_argument("--timeout", type=float, default=10)
    p = sub.add_parser("session-secret", help="persist a new proof locally; does not contact the service")
    p.add_argument("--session-id", required=True)
    p.add_argument("--output", required=True)
    args = parser.parse_args()
    try:
        if args.mode == "session-secret":
            if not args.session_id or len(args.session_id) > 256:
                raise ValueError("session_id must have 1..256 characters")
            if Path(args.output).exists():
                value = session(args.output)
                if value["session_id"] != args.session_id:
                    raise ValueError("existing session file belongs to another session_id")
                secret = token_bytes(value["secret"])
            else:
                secret = secrets.token_bytes(32)
                value = {"session_id": args.session_id, "secret": base64.urlsafe_b64encode(secret).decode().rstrip("=")}
                durable_new(args.output, json.dumps(value).encode())
            print(json.dumps({"session_id": args.session_id, "proof_digest": hashlib.sha256(secret).hexdigest()}))
            return 0
        if not 0 < args.timeout <= 120:
            raise ValueError("timeout must be in (0,120] seconds")
        route = {"query": "queries", "command": "commands", "action": "actions"}[args.mode]
        code, body = send(args.config, route, args.request, args.session_file, args.target_session_file, args.timeout)
        sys.stdout.buffer.write(body + b"\n")
        print("HTTP " + str(code), file=sys.stderr)
        return 0 if code == 200 else 2
    except (OSError, ValueError, TypeError, AttributeError, decimal.InvalidOperation) as error:
        # Do not stringify network exceptions: a remote server controls parts of
        # their text. The original request remains available for reconciliation.
        message = str(error) if isinstance(error, ValueError) else type(error).__name__
        print("ledger: " + message + "; no automatic retry", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
