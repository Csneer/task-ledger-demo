import concurrent.futures
import http.client
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
from pathlib import Path
import tempfile
import threading
import unittest
import uuid

from intake import Handler, Intake, MAX_BODY


class Backend(BaseHTTPRequestHandler):
    def log_message(self, *_args):
        pass

    def do_POST(self):
        data = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        self.server.calls.append(data)
        if self.server.fail_once:
            self.server.fail_once = False
            self.close_connection = True
            return
        status = self.server.status
        task = dict(data["command"]["task"], state="offered")
        if self.server.wrong_reference:
            task["task_id"] = "wrong-reference"
        body = json.dumps({"result": {"task": task, "replayed": False}}).encode()
        self.send_response(status)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


class IntakeTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.directory = Path(self.temp.name)
        (self.directory / "credential").write_text("test-server-only-credential")
        self.backend = ThreadingHTTPServer(("127.0.0.1", 0), Backend)
        self.backend.calls = []
        self.backend.fail_once = False
        self.backend.status = 200
        self.backend.wrong_reference = False
        self.thread = threading.Thread(target=self.backend.serve_forever, daemon=True)
        self.thread.start()
        self.config = {"listen_port": 0, "upstream_url": "http://127.0.0.1:" + str(self.backend.server_port),
                       "issuer_credential_file": str(self.directory / "credential"),
                       "authority": "test-demo", "project_id": "guest-requests",
                       "allowed_origin": "https://task.pokison.cn", "intent_dir": str(self.directory / "intents"),
                       "max_submissions": 2}
        self.intake = Intake(self.config)

    def tearDown(self):
        self.backend.shutdown()
        self.backend.server_close()
        self.thread.join()
        self.temp.cleanup()

    def body(self, **changes):
        data = {"id": str(uuid.uuid4()), "goal": "Write an onboarding guide", "expected_result": "A clear guide for new users.", "public": True}
        data.update(changes)
        return json.dumps(data).encode()

    def test_create_is_fixed_and_does_not_expose_credential(self):
        status, result = self.intake.submit(self.body())
        self.assertEqual(status, 200)
        command = self.backend.calls[0]
        self.assertEqual(command["command"]["operation"], "task.create")
        self.assertEqual(command["application"], {"project_id": "guest-requests", "claim_policy": {"mode": "workspace"}})
        self.assertTrue(command["command"]["body"]["goal"].startswith("[游客投稿] "))
        self.assertEqual(set(result), {"task", "state", "replayed"})
        self.assertNotIn("credential", json.dumps(result))
        self.assertEqual(len(list(self.intake.directory.glob("*.intent.json"))), 1)

    def test_forbidden_fields_consent_and_text_are_rejected(self):
        for changes in [{"actor": "owner"}, {"application": {}}, {"public": False}, {"goal": "x"}, {"expected_result": "short"}, {"id": "../../bad"}, {"goal": "a\x00bc"}, {"goal": "x" * 201}]:
            self.assertEqual(self.intake.submit(self.body(**changes))[0], 400)
        self.assertEqual(self.backend.calls, [])

    def test_duplicate_keys_nonfinite_and_surrogate_are_rejected(self):
        for raw in [b'{"id":"a","id":"b"}', b'{"x":NaN}', b'{"x":Infinity}', self.body(goal="valid\ud800")]:
            self.assertEqual(self.intake.submit(raw)[0], 400)
        self.assertEqual(self.backend.calls, [])

    def test_deep_json_is_rejected_without_upstream_write(self):
        raw = b"[" * 1200 + b"0" + b"]" * 1200
        self.assertLess(len(raw), MAX_BODY)
        self.assertEqual(self.intake.submit(raw), (400, {"error": "INVALID_REQUEST", "may_have_committed": False}))
        self.assertEqual(self.backend.calls, [])

    def test_wrong_upstream_task_reference_keeps_result_unknown(self):
        self.backend.wrong_reference = True
        status, result = self.intake.submit(self.body())
        self.assertEqual(status, 503)
        self.assertTrue(result["may_have_committed"])

    def test_body_limit(self):
        self.assertEqual(self.intake.submit(b" " * (MAX_BODY + 1))[0], 413)

    def test_replay_remains_available_at_capacity_and_after_restart(self):
        self.config["max_submissions"] = 1
        raw = self.body()
        self.assertEqual(self.intake.submit(raw)[0], 200)
        self.assertEqual(self.intake.submit(self.body())[0], 429)
        restarted = Intake(self.config)
        status, result = restarted.submit(raw)
        self.assertEqual(status, 200)
        self.assertTrue(result["replayed"])
        self.assertEqual(len(self.backend.calls), 1)

    def test_same_id_changed_body_rejected(self):
        raw = self.body()
        self.intake.submit(raw)
        data = json.loads(raw)
        data["goal"] = "A different request"
        status, response = self.intake.submit(json.dumps(data).encode())
        self.assertEqual(status, 409)
        self.assertTrue(response["may_have_committed"])
        self.assertEqual(len(self.backend.calls), 1)

    def test_unknown_result_retains_exact_original_intent(self):
        raw = self.body()
        self.backend.fail_once = True
        status, response = self.intake.submit(raw)
        self.assertEqual(status, 503)
        self.assertTrue(response["may_have_committed"])
        restarted = Intake(self.config)
        self.assertEqual(restarted.submit(raw)[0], 200)
        self.assertEqual(self.backend.calls[0], self.backend.calls[1])

    def test_later_auth_rejection_does_not_clear_unknown_result(self):
        raw = self.body()
        self.backend.fail_once = True
        self.intake.submit(raw)
        self.backend.status = 403
        status, result = self.intake.submit(raw)
        self.assertEqual(status, 503)
        self.assertTrue(result["may_have_committed"])

    def test_concurrent_new_requests_cannot_exceed_capacity(self):
        self.config["max_submissions"] = 1
        with concurrent.futures.ThreadPoolExecutor(max_workers=5) as pool:
            statuses = list(pool.map(lambda raw: self.intake.submit(raw)[0], [self.body() for _ in range(5)]))
        self.assertEqual(statuses.count(200), 1)
        self.assertEqual(statuses.count(429), 4)
        self.assertEqual(len(self.backend.calls), 1)

    def test_http_origin_path_type_and_size(self):
        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        server.intake = self.intake
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            cases = [("/demo/tasks", "https://evil.example", "application/json", self.body(), 403),
                     ("/v1/commands", self.config["allowed_origin"], "application/json", self.body(), 404),
                     ("/demo/tasks", self.config["allowed_origin"], "text/plain", self.body(), 415),
                     ("/demo/tasks", self.config["allowed_origin"], "application/json", b"x" * (MAX_BODY + 1), 413),
                     ("/demo/tasks", self.config["allowed_origin"], "application/json", self.body(), 200)]
            for path, origin, content_type, raw, expected in cases:
                connection = http.client.HTTPConnection("127.0.0.1", server.server_port, timeout=3)
                connection.request("POST", path, raw, {"Origin": origin, "Content-Type": content_type})
                response = connection.getresponse()
                self.assertEqual(response.status, expected)
                self.assertEqual(response.getheader("Cache-Control"), "no-store")
                response.read()
                connection.close()
        finally:
            server.shutdown()
            server.server_close()
            thread.join()


if __name__ == "__main__":
    unittest.main()
