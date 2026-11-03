"""Behavioral tests for the convenient client; real HTTP is covered by smoke tests."""
import base64
import http.client
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

import agent


BINDING = "ledger-http-draft-1"
PROTOCOL = "task-core-draft-1"


class Service:
    """A small transport double that records bytes and models independent commits."""

    def __init__(self):
        self.actor = {"kind": "agent", "id": "worker"}
        self.authority = "test-authority"
        self.calls = []
        self.failures = {}
        self.receipts = {}
        self.sessions = {}
        self.binding = None
        self.task = {
            "authority_id": self.authority, "task_id": "work", "issuer": self.actor,
            "state": "offered", "revision": 1, "expectation_version": 1,
            "generation": 0, "current_assignment_id": None, "current_run_id": None,
            "pending_submission_id": None, "relations": [],
            "expectations": [{"version": 1, "goal": "Work", "expected_result": "Proof"}],
        }
        self.records = []

    def bundle(self):
        return {"snapshot": {"task": self.task, "records": self.records},
                "application": {"project_id": "project", "claim_policy": {"mode": "workspace"},
                                "execution_binding": self.binding}}

    def reply(self, result):
        return 200, json.dumps({"binding_version": BINDING, "result": result}).encode()

    def send(self, config_path, route, request_path=None, session_path=None,
             target_path=None, timeout=10):
        if route == "identity":
            return 200, json.dumps({"actor": self.actor, "read": True,
                                    "write": True, "admin": False}).encode()
        raw = Path(request_path).read_bytes()
        request = json.loads(raw)
        operation = request.get("operation", request.get("command", {}).get("operation"))
        self.calls.append((route, operation, raw, session_path))
        if route == "queries":
            body = request["body"]
            if operation == "service.info":
                return self.reply({"authority_id": self.authority, "binding_version": BINDING,
                                   "protocol_version": PROTOCOL})
            if operation == "workspace.snapshot":
                return self.reply({"projects": [{"project_id": "project", "revision": 1}],
                                   "tasks": [self.bundle()], "participants": []})
            if operation == "task.snapshot":
                return self.reply({"bundle": self.bundle()})
            if operation == "session.get":
                return self.reply({"session": self.sessions[body["session_id"]],
                                   "current_binding": self.binding})
            raise AssertionError(operation)
        failure = self.failures.get(operation, [])
        if failure:
            outcome = failure.pop(0)
            if isinstance(outcome, BaseException):
                raise outcome
            return outcome
        envelope = request.get("command", request)
        key = envelope.get("command_id", request.get("request_id"))
        if key in self.receipts:
            return self.receipts[key]
        body = envelope["body"]
        if operation == "session.begin":
            session = {"session_id": body["session_id"], "actor": self.actor,
                       "project_id": body["project_id"], "goal": body["goal"],
                       "state": "active", "phase": "preparing", "revision": 1}
            self.sessions[session["session_id"]] = session
            result = session
        elif route == "commands":
            if operation == "task.create":
                self.task.update(envelope["task"])
            elif operation == "task.claim":
                self.task.update(state="claimed", current_assignment_id="assignment-1", generation=1)
                self.binding = {"task": envelope["task"], "assignment_id": "assignment-1",
                                "generation": 1, "session_id": request["application"]["session_id"],
                                "state": "current", "revision": 1, "run_id": None}
            elif operation == "run.start":
                self.task.update(state="active", current_run_id="run-1")
                self.binding["run_id"] = "run-1"
                self.records.append({"record_type": "Run", "record": {
                    "task": envelope["task"], "run_id": "run-1", "assignment_id": "assignment-1",
                    "assignee": self.actor, "basis_version": body["basis_version"], "state": "running"}})
            elif operation == "run.submit":
                self.task.update(state="submitted", pending_submission_id="submission-1",
                                 current_assignment_id=None, current_run_id=None)
                self.binding = None
            self.task["revision"] += 1
            result = {"protocol_version": PROTOCOL, "command_id": key, "replayed": False,
                      "task": self.task, "records": self.records, "event": {"event_id": "event-1"}}
        else:
            raise AssertionError(operation)
        if route == "actions":
            answer = 200, json.dumps({"binding_version": BINDING, "request_id": key,
                                      "result": result}).encode()
        else:
            answer = self.reply(result)
        self.receipts[key] = answer
        return answer

    def writes(self, operation=None):
        return [call for call in self.calls if call[0] != "queries"
                and (operation is None or call[1] == operation)]


class AgentTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.profile = self.root / "profile"
        self.credential = self.root / "credential"
        self.credential.write_text(base64.urlsafe_b64encode(b"t" * 32).decode().rstrip("="))
        self.credential.chmod(0o600)
        self.service = Service()
        self.transport = patch.object(agent.ledger, "send", self.service.send)
        self.transport.start()
        self.addCleanup(self.transport.stop)
        with agent.Agent(self.profile) as client:
            client.setup("http://127.0.0.1:8090", str(self.credential), "project")

    def envelope(self, command_id="write-one"):
        return {"binding_version": BINDING, "command": {
            "protocol_version": PROTOCOL, "command_id": command_id,
            "task": {"authority_id": self.service.authority, "task_id": "work"},
            "expected_revision": 1, "operation": "task.create",
            "body": {"goal": "Work", "expected_result": "Proof"}},
            "application": {"project_id": "project", "claim_policy": {"mode": "workspace"}}}

    def operations(self):
        return [(path, json.loads(path.read_text()))
                for path in (self.profile / "operations").glob("*/operation.json")]

    def execute(self, client, *arguments):
        return client.execute(agent.parser().parse_args(["--profile", str(self.profile), *arguments]))

    def test_setup_private_files_and_identity(self):
        with agent.Agent(self.profile) as client:
            result = client.check()
        self.assertIn("worker", json.dumps(result))
        self.assertIn(self.service.authority, json.dumps(result))
        for path in [self.profile, *self.profile.rglob("*")]:
            self.assertEqual(path.stat().st_mode & 0o777, 0o700 if path.is_dir() else 0o600,
                             str(path))
        self.assertEqual(self.service.writes(), [])

    def test_identity_drift_blocks_writes(self):
        self.service.actor = {"kind": "agent", "id": "different-worker"}
        with agent.Agent(self.profile) as client:
            with self.assertRaises((agent.ClientError, ValueError)):
                client.write("commands", self.envelope())
        self.assertEqual(self.service.writes(), [])

    def test_authority_drift_blocks_writes(self):
        self.service.authority = "different-authority"
        with agent.Agent(self.profile) as client:
            with self.assertRaises((agent.ClientError, ValueError)):
                client.write("commands", self.envelope())
        self.assertEqual(self.service.writes(), [])

    def test_unknown_blocks_new_writes_and_retry_preserves_bytes(self):
        self.service.failures["task.create"] = [TimeoutError("lost response")]
        with agent.Agent(self.profile) as client:
            with self.assertRaises(agent.ClientError):
                client.write("commands", self.envelope(), operation_id="op-one")
            with self.assertRaises(agent.ClientError):
                client.write("commands", self.envelope("different"), operation_id="op-two")
            self.assertEqual(len(self.service.writes()), 1)
            client.retry("op-one")
        writes = self.service.writes()
        self.assertEqual(len(writes), 2)
        self.assertEqual(writes[0][2], writes[1][2])
        self.assertEqual(json.loads(writes[1][2])["command"]["command_id"], "write-one")

    def test_truncated_response_reports_recoverable_unknown(self):
        self.service.failures["task.create"] = [http.client.IncompleteRead(b"partial", 100)]
        with agent.Agent(self.profile) as client:
            with self.assertRaises(agent.ClientError) as caught:
                client.write("commands", self.envelope(), operation_id="op-truncated")
            self.assertEqual(caught.exception.payload["status"], "unknown")
            self.assertEqual(caught.exception.payload["operation_id"], "op-truncated")
            self.assertEqual(client.retry("op-truncated")["status"], "succeeded")

    def test_crash_before_publish_does_not_block_unsent_operation(self):
        with agent.Agent(self.profile) as client:
            staging = self.profile / "operations" / ".prepared-interrupted"
            agent.private_directory(staging)
            agent.save(staging / "operation.json", {
                "operation_id": "op-never-published", "status": "prepared"})
            self.assertEqual(client.pending(), [])
            result = client.write("commands", self.envelope(), operation_id="op-published")
            self.assertEqual(result["status"], "succeeded")
            self.assertEqual(len(self.service.writes()), 1)

    def test_unknown_then_forbidden_stays_unresolved(self):
        self.service.failures["task.create"] = [TimeoutError("lost response"),
            (403, b'{"binding_version":"ledger-http-draft-1","error":{"scope":"binding","code":"FORBIDDEN"}}')]
        with agent.Agent(self.profile) as client:
            with self.assertRaises(agent.ClientError):
                client.write("commands", self.envelope(), operation_id="op-one")
            with self.assertRaises(agent.ClientError):
                client.retry("op-one")
            with self.assertRaises(agent.ClientError):
                client.write("commands", self.envelope("new-id"), operation_id="op-two")
        self.assertEqual(len(self.service.writes()), 2)
        operation = dict((path.parent.name, value) for path, value in self.operations())["op-one"]
        self.assertEqual(operation["status"], "unknown")

    def test_lost_success_response_replays_one_committed_fact(self):
        original_send = self.service.send
        lost = False

        def lose_response(config_path, route, request_path=None, **kwargs):
            nonlocal lost
            answer = original_send(config_path, route, request_path, **kwargs)
            if route == "commands" and not lost:
                lost = True
                raise ConnectionResetError("response lost after commit")
            return answer

        revision = self.service.task["revision"]
        with patch.object(agent.ledger, "send", lose_response):
            with agent.Agent(self.profile) as client:
                with self.assertRaises(agent.ClientError):
                    client.write("commands", self.envelope(), operation_id="op-one")
            with agent.Agent(self.profile) as client:
                client.retry("op-one")
        self.assertEqual(self.service.task["revision"], revision + 1)
        self.assertEqual(len(self.service.receipts), 1)
        self.assertEqual(self.service.writes()[0][2], self.service.writes()[1][2])

    def test_large_success_receipt_can_be_read_after_reopening_profile(self):
        self.service.records = [{"record_type": "Report", "record": {"message": "x" * (2 << 20)}}]
        with agent.Agent(self.profile) as client:
            client.write("commands", self.envelope(), operation_id="op-large")
        self.assertGreater((self.profile / "operations" / "op-large" / "response.json").stat().st_size, 1 << 20)
        with agent.Agent(self.profile) as client:
            result = client.retry("op-large")
        self.assertEqual(result["status"], "succeeded")
        self.assertEqual(len(self.service.writes()), 1)

    def test_mismatched_success_receipt_cannot_resolve_write(self):
        response = {"binding_version": BINDING, "result": {
            "command_id": "another-request", "task": self.service.task}}
        self.service.failures["task.create"] = [(200, json.dumps(response).encode())]
        with agent.Agent(self.profile) as client:
            with self.assertRaises(agent.ClientError):
                client.write("commands", self.envelope(), operation_id="op-one")
            with self.assertRaises(agent.ClientError):
                client.write("commands", self.envelope("other"), operation_id="op-two")
        self.assertEqual(len(self.service.writes()), 1)

    def test_profile_lock_rejects_another_writer(self):
        with agent.Agent(self.profile):
            with self.assertRaises((agent.ClientError, ValueError, BlockingIOError)):
                with agent.Agent(self.profile):
                    self.fail("second process must not acquire the same profile")

    def test_take_uses_one_session_and_independent_operation_ids(self):
        with agent.Agent(self.profile) as client:
            client.take("work")
        writes = self.service.writes()
        self.assertEqual([call[1] for call in writes], ["session.begin", "task.claim", "run.start"])
        ids = [json.loads(raw).get("request_id", json.loads(raw).get("command", {}).get("command_id"))
               for _, _, raw, _ in writes]
        self.assertEqual(len(set(ids)), 3)
        self.assertEqual(len(self.service.sessions), 1)
        self.assertEqual(self.service.task["state"], "active")
        self.assertEqual(len(self.operations()), 3)

    def test_take_after_rejected_start_preserves_successful_claim(self):
        self.service.failures["run.start"] = [(409, json.dumps({
            "binding_version": BINDING, "error": {"scope": "core", "error": {
                "protocol_version": PROTOCOL, "kind": "STALE_REVISION"}}}).encode())]
        with agent.Agent(self.profile) as client:
            with self.assertRaises(agent.ClientError):
                client.take("work")
        self.assertEqual(self.service.task["state"], "claimed")
        session_id = self.service.binding["session_id"]
        # Reopen the profile just as a fresh process would after interruption.
        with agent.Agent(self.profile) as client:
            client.take("work")
        self.assertEqual(len(self.service.writes("session.begin")), 1)
        self.assertEqual(len(self.service.writes("task.claim")), 1)
        self.assertEqual(len(self.service.writes("run.start")), 2)
        self.assertEqual(self.service.binding["session_id"], session_id)
        self.assertEqual(self.service.task["state"], "active")

    def test_write_intent_exists_before_transport(self):
        original_send = self.service.send

        def inspect_intent(config_path, route, request_path=None, **kwargs):
            if route == "commands":
                request_path = Path(request_path)
                self.assertEqual(request_path.stat().st_mode & 0o777, 0o600)
                self.assertTrue(request_path.is_relative_to(self.profile / "operations"))
                self.assertEqual(json.loads(request_path.read_text())["command"]["command_id"], "write-one")
                self.assertEqual(len(self.operations()), 1)
            return original_send(config_path, route, request_path, **kwargs)

        with patch.object(agent.ledger, "send", inspect_intent):
            with agent.Agent(self.profile) as client:
                client.write("commands", self.envelope(), operation_id="op-one")
        self.assertEqual(len(self.service.writes()), 1)

    def test_resume_is_read_only_after_take(self):
        with agent.Agent(self.profile) as client:
            client.take("work")
        writes = len(self.service.writes())
        with agent.Agent(self.profile) as client:
            result = client.resume("work")
        self.assertEqual(len(self.service.writes()), writes)
        self.assertIn("run-1", json.dumps(result))
        self.assertEqual(len(self.service.sessions), 1)

    def test_submit_uses_run_basis_and_latest_task_revision(self):
        with agent.Agent(self.profile) as client:
            client.take("work")
            self.service.task["expectation_version"] = 2
            self.service.task["revision"] += 1
            revision = self.service.task["revision"]
            self.execute(client, "submit", "--task", "work", "--summary", "Actual evidence",
                         "--artifact", "file:///tmp/evidence.txt")
        request = json.loads(self.service.writes("run.submit")[0][2])
        self.assertEqual(request["command"]["expected_revision"], revision)
        self.assertEqual(request["command"]["body"]["basis_version"], 1)
        self.assertEqual(request["command"]["body"]["run_id"], "run-1")
        self.assertEqual(request["command"]["body"]["artifacts"], [{"uri": "file:///tmp/evidence.txt"}])
        self.assertEqual(self.service.writes("session.end"), [])

    def test_same_actor_different_session_cannot_report(self):
        with agent.Agent(self.profile) as client:
            client.take("work")
            self.service.binding["session_id"] = "another-process-session"
            with self.assertRaisesRegex(ValueError, "owned"):
                self.execute(client, "report", "--task", "work", "--message", "Cannot impersonate")
        self.assertEqual(self.service.writes("run.report"), [])

    def test_create_projects_parent_and_target_without_claiming(self):
        with agent.Agent(self.profile) as client:
            self.execute(client, "create", "--task", "child", "--goal", "Child work",
                         "--expected-result", "Verified result", "--parent", "work", "--target", "child-agent")
        request = json.loads(self.service.writes("task.create")[0][2])
        self.assertEqual(request["command"]["body"]["relations"], [{"kind": "parent", "target": {
            "authority_id": self.service.authority, "task_id": "work"}}])
        self.assertEqual(request["application"]["claim_policy"], {
            "mode": "target", "target": {"kind": "agent", "id": "child-agent"}})
        self.assertEqual(self.service.writes("task.claim"), [])

    def test_decide_does_not_switch_to_a_newer_submission(self):
        self.service.task.update(state="submitted", pending_submission_id="new-submission")
        with agent.Agent(self.profile) as client:
            with self.assertRaisesRegex(ValueError, "submission"):
                self.execute(client, "decide", "--task", "work", "--submission", "old-submission",
                             "--outcome", "accepted", "--reason", "Checked old evidence")
        self.assertEqual(self.service.writes(), [])

    def test_decide_cli_requires_explicit_submission(self):
        result = subprocess.run([sys.executable, str(Path(agent.__file__)), "--profile", str(self.profile),
                                 "decide", "--task", "work", "--outcome", "accepted", "--reason", "checked"],
                                capture_output=True, text=True, timeout=10)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("--submission", result.stderr)


if __name__ == "__main__":
    unittest.main()
