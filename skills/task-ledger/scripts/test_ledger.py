import base64
import contextlib
import concurrent.futures
import http.server
import io
import json
import os
from pathlib import Path
import tempfile
import threading
import unittest
from unittest.mock import patch

import ledger


class LedgerTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.calls = []
        self.status = 200
        self.response = b'{"result":"ok"}'
        owner = self

        class Handler(http.server.BaseHTTPRequestHandler):
            def do_POST(self):
                raw = self.rfile.read(int(self.headers["Content-Length"]))
                owner.calls.append((self.path, dict(self.headers), raw))
                self.send_response(owner.status)
                if owner.status == 302:
                    self.send_header("Location", "/redirect-target")
                self.end_headers()
                self.wfile.write(owner.response)

            def log_message(self, *_):
                pass

        self.server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        self.addCleanup(self.stop_server)
        self.token = base64.urlsafe_b64encode(b"t" * 32).decode().rstrip("=")
        self.write_private("token", self.token.encode())
        self.config = self.root / "config.json"
        self.write_private("config.json", json.dumps({"service_url": "http://127.0.0.1:" + str(self.server.server_port), "credential_file": "token"}).encode())
        self.raw = b'{"binding_version":"ledger-http-draft-1","command":{"command_id":"one","number":1.00000000000000001}}'
        self.request = self.root / "request.json"
        self.request.write_bytes(self.raw)

    def stop_server(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join()

    def write_private(self, name, raw):
        target = self.root / name
        target.write_bytes(raw)
        target.chmod(0o600)
        return target

    def send(self, **kwargs):
        return ledger.send(self.config, "commands", self.request, **kwargs)

    def test_exact_bytes_intent_and_retry_conflict(self):
        self.assertEqual(self.send()[0], 200)
        self.assertEqual(self.send()[0], 200)
        self.assertEqual([c[2] for c in self.calls], [self.raw, self.raw])
        files = list((self.root / "intents").glob("*.request.json"))
        self.assertEqual(len(files), 1)
        self.assertEqual(files[0].read_bytes(), self.raw)
        self.assertEqual(files[0].stat().st_mode & 0o777, 0o600)
        self.assertEqual(files[0].parent.stat().st_mode & 0o777, 0o700)
        self.request.write_bytes(self.raw.replace(b"1.00000000000000001", b"1.0"))
        with self.assertRaisesRegex(ValueError, "different request bytes"):
            self.send()
        self.assertEqual(len(self.calls), 2)

    def test_http_failure_retains_intent_and_never_follows_redirect(self):
        self.status = 503
        self.assertEqual(self.send()[0], 503)
        self.assertEqual(len(list((self.root / "intents").glob("*.request.json"))), 1)
        self.status = 302
        self.assertEqual(self.send()[0], 302)
        self.assertEqual(len(self.calls), 2)

    def test_private_files_and_proof_never_enter_intent_or_output(self):
        proof = base64.urlsafe_b64encode(b"s" * 32).decode().rstrip("=")
        session_path = self.write_private("session.json", json.dumps({"session_id": "session-one", "secret": proof}).encode())
        self.response = (self.token + " " + proof).encode()
        _, response = self.send(session_path=session_path)
        self.assertNotIn(self.token.encode(), response)
        self.assertNotIn(proof.encode(), response)
        self.assertEqual(self.calls[0][1]["X-Ledger-Session-Proof"], proof)
        for f in (self.root / "intents").iterdir():
            self.assertNotIn(proof.encode(), f.read_bytes())
            self.assertNotIn(self.token.encode(), f.read_bytes())
        (self.root / "token").chmod(0o644)
        with self.assertRaisesRegex(ValueError, "private files"):
            self.send()
        self.assertEqual(len(self.calls), 1)

    def test_queries_do_not_create_intent(self):
        self.request.write_bytes(b'{"binding_version":"ledger-http-draft-1","operation":"service.info","body":{}}')
        self.assertEqual(ledger.send(self.config, "queries", self.request)[0], 200)
        self.assertFalse((self.root / "intents").exists())

    def test_strict_input_and_transport_origin(self):
        for raw in [b'{"x":1,"x":2}', b'{"x":"\\ud800"}', b'{"x":NaN}']:
            with self.assertRaises(ValueError):
                ledger.strict_json(raw)
        config = json.loads(self.config.read_text())
        for url in ["http://example.com", "https://user:password@example.com", "http://127.0.0.1/base", "http://127.0.0.1?token=secret"]:
            config["service_url"] = url
            self.config.write_text(json.dumps(config))
            with self.assertRaises(ValueError):
                self.send()
        self.assertEqual(self.calls, [])

    def test_session_secret_is_recoverable_without_printing_secret(self):
        target = self.root / "session.json"
        outputs = []
        for _ in range(2):
            output = io.StringIO()
            with patch("sys.argv", ["ledger", "session-secret", "--session-id", "s", "--output", str(target)]), contextlib.redirect_stdout(output):
                self.assertEqual(ledger.main(), 0)
            outputs.append(json.loads(output.getvalue()))
        self.assertEqual(outputs[0], outputs[1])
        secret = json.loads(target.read_text())["secret"]
        self.assertNotIn(secret, json.dumps(outputs))
        self.assertEqual(len(outputs[0]["proof_digest"]), 64)

    def test_concurrent_publication_never_overwrites_original(self):
        target = self.root / "intents" / "same.json"

        def save(raw):
            try:
                ledger.durable_new(target, raw)
                return "saved"
            except ValueError:
                return "conflict"

        with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
            outcomes = list(pool.map(save, [b'{"value":1}', b'{"value":2}']))
        self.assertCountEqual(outcomes, ["saved", "conflict"])
        original = target.read_bytes()
        ledger.durable_new(target, original)
        self.assertEqual(target.read_bytes(), original)


if __name__ == "__main__":
    unittest.main()
