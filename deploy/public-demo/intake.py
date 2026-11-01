#!/usr/bin/env python3
"""Bounded anonymous task intake; run behind the public-demo nginx allowlist."""
import argparse
import http.client
import json
import os
from pathlib import Path
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlsplit
import uuid

MAX_BODY = 16384


def strict_json(raw):
    def pairs(items):
        result = {}
        for key, value in items:
            if key in result:
                raise ValueError("duplicate key")
            result[key] = value
        return result
    def constant(value):
        raise ValueError("nonfinite number")
    return json.loads(raw.decode("utf-8"), object_pairs_hook=pairs, parse_constant=constant)


def error(code, uncertain=False):
    return {"error": code, "may_have_committed": uncertain}


class Intake:
    def __init__(self, config):
        self.config = config
        url = urlsplit(config["upstream_url"])
        if url.scheme != "http" or url.hostname != "127.0.0.1" or url.username or url.password or url.path not in ("", "/") or url.query or url.fragment:
            raise ValueError("upstream must be loopback HTTP without path or credentials")
        self.upstream_port = url.port or 80
        self.token = Path(config["issuer_credential_file"]).read_text().strip()
        if not self.token or any(c.isspace() for c in self.token):
            raise ValueError("invalid credential file")
        if not isinstance(config["max_submissions"], int) or isinstance(config["max_submissions"], bool) or not 1 <= config["max_submissions"] <= 200:
            raise ValueError("max_submissions must be 1..200")
        self.directory = Path(config["intent_dir"])
        self.directory.mkdir(mode=0o700, parents=True, exist_ok=True)
        self.lock = threading.Lock()

    def persist(self, path, record):
        temporary = path.with_suffix(".tmp")
        with temporary.open("w", encoding="utf-8") as output:
            json.dump(record, output, ensure_ascii=False, separators=(",", ":"))
            output.flush()
            os.fsync(output.fileno())
        os.replace(temporary, path)
        descriptor = os.open(self.directory, os.O_RDONLY | os.O_DIRECTORY)
        try:
            os.fsync(descriptor)
        finally:
            os.close(descriptor)

    def submit(self, raw):
        if len(raw) > MAX_BODY:
            return 413, error("REQUEST_TOO_LARGE")
        try:
            data = strict_json(raw)
            if not isinstance(data, dict) or set(data) != {"id", "goal", "expected_result", "public"}:
                raise ValueError("unexpected fields")
            if not isinstance(data["id"], str) or str(uuid.UUID(data["id"])) != data["id"]:
                raise ValueError("invalid id")
            if data["public"] is not True:
                raise ValueError("public consent required")
            for key, minimum, maximum in [("goal", 4, 200), ("expected_result", 8, 2000)]:
                value = data[key]
                if not isinstance(value, str) or not minimum <= len(value.strip()) <= maximum:
                    raise ValueError("invalid text")
                if any(ord(c) < 32 and c not in "\n\t" for c in value):
                    raise ValueError("control character")
                value.encode("utf-8")
        except (ValueError, TypeError, UnicodeError, RecursionError):
            return 400, error("INVALID_REQUEST")
        ref = {"authority_id": self.config["authority"], "task_id": "guest-" + data["id"]}
        envelope = {
            "binding_version": "ledger-http-draft-1",
            "command": {"protocol_version": "task-core-draft-1", "command_id": "guest-" + data["id"],
                        "task": ref, "expected_revision": 0, "operation": "task.create",
                        "body": {"goal": "[游客投稿] " + data["goal"], "expected_result": data["expected_result"],
                                 "extensions": {"io.taskledger.demo": {"source": "guest", "public": True}}}},
            "application": {"project_id": self.config["project_id"], "claim_policy": {"mode": "workspace"}},
        }
        with self.lock:
            path = self.directory / (data["id"] + ".intent.json")
            if path.exists():
                try:
                    record = json.loads(path.read_text())
                except (OSError, ValueError):
                    return 503, error("INTENT_UNAVAILABLE", True)
                if record["raw"] != raw.decode("utf-8"):
                    return 409, error("ID_REUSED", record.get("uncertain", True) or "result" in record)
                if "result" in record:
                    return 200, dict(record["result"], replayed=True)
            else:
                if len(list(self.directory.glob("*.intent.json"))) >= self.config["max_submissions"]:
                    return 429, error("INTAKE_FULL")
                record = {"raw": raw.decode("utf-8"), "envelope": envelope, "uncertain": False}
            previously_uncertain = record["uncertain"]
            record["uncertain"] = True
            try:
                self.persist(path, record)  # Durable original intent before any upstream write.
            except OSError:
                return 503, error("INTENT_UNAVAILABLE", previously_uncertain)
            connection = http.client.HTTPConnection("127.0.0.1", self.upstream_port, timeout=10)
            try:
                payload = json.dumps(record["envelope"], ensure_ascii=False, separators=(",", ":")).encode()
                connection.request("POST", "/v1/commands", payload,
                                   {"Authorization": "Bearer " + self.token, "Content-Type": "application/json"})
                response = connection.getresponse()
                body = response.read(1048577)
                if 400 <= response.status < 500:
                    record["uncertain"] = previously_uncertain
                    self.persist(path, record)
                    return 503 if previously_uncertain else 400, error("UPSTREAM_REJECTED", previously_uncertain)
                if response.status != 200 or len(body) > 1048576:
                    return 503, error("RESULT_UNKNOWN", True)
                result = strict_json(body)["result"]
                if (result["task"]["state"] != "offered" or
                    any(result["task"].get(key) != value for key, value in ref.items())):
                    return 503, error("RESULT_UNKNOWN", True)
                public = {"task": ref, "state": "offered", "replayed": bool(result.get("replayed", False))}
                record.update(uncertain=False, result=public)
                self.persist(path, record)
                return 200, public
            except (OSError, ValueError, KeyError, TypeError, RecursionError, http.client.HTTPException):
                return 503, error("RESULT_UNKNOWN", True)
            finally:
                connection.close()


class Handler(BaseHTTPRequestHandler):
    server_version = "TaskLedgerIntake"
    sys_version = ""

    def setup(self):
        super().setup()
        self.connection.settimeout(10)

    def log_message(self, *_args):
        pass  # Do not put visitor text, tokens or request paths in journals.

    def respond(self, status, body):
        content = json.dumps(body, ensure_ascii=False).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Cache-Control", "no-store")
        self.send_header("X-Content-Type-Options", "nosniff")
        self.send_header("Content-Length", str(len(content)))
        self.send_header("Connection", "close")
        self.end_headers()
        self.close_connection = True
        self.wfile.write(content)

    def do_POST(self):
        if self.path != "/demo/tasks":
            return self.respond(404, error("NOT_FOUND"))
        if self.headers.get_all("Origin") != [self.server.intake.config["allowed_origin"]]:
            return self.respond(403, error("ORIGIN_REJECTED"))
        if self.headers.get("Content-Type", "").split(";", 1)[0].strip().lower() != "application/json":
            return self.respond(415, error("JSON_REQUIRED"))
        lengths = self.headers.get_all("Content-Length") or []
        if self.headers.get("Transfer-Encoding") or len(lengths) != 1 or not lengths[0].isdigit():
            return self.respond(400, error("INVALID_LENGTH"))
        length = int(lengths[0])
        if length > MAX_BODY:
            return self.respond(413, error("REQUEST_TOO_LARGE"))
        try:
            raw = self.rfile.read(length)
        except OSError:
            return self.respond(408, error("REQUEST_TIMEOUT"))
        if len(raw) != length:
            return self.respond(400, error("INVALID_LENGTH"))
        status, result = self.server.intake.submit(raw)
        self.respond(status, result)

    def do_GET(self):
        self.respond(405, error("METHOD_NOT_ALLOWED"))

    do_HEAD = do_GET
    do_PUT = do_GET
    do_DELETE = do_GET
    do_PATCH = do_GET
    do_OPTIONS = do_GET


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config", type=Path, required=True)
    args = parser.parse_args()
    os.umask(0o077)
    config = strict_json(args.config.read_bytes())
    for name in ("issuer_credential_file", "intent_dir"):
        config[name] = str((args.config.parent / config[name]).resolve())
    server = ThreadingHTTPServer(("127.0.0.1", config["listen_port"]), Handler)
    server.daemon_threads = True
    server.intake = Intake(config)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        server.server_close()


if __name__ == "__main__":
    main()
