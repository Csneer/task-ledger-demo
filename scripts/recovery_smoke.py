#!/usr/bin/env python3
"""Exercise latest stopped-state relocation through public HTTP, using synthetic data.

No production service, database internals, Agent CLI or external dependency is used.
The original and restored authority are never run concurrently. All private
artifacts are retained; a passed rehearsal is not permission to roll back history.
"""
import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import secrets
import shutil
import signal
import socket
import subprocess
import sys
import time
import urllib.error
import urllib.request

BINDING = "ledger-http-draft-1"
PROTOCOL = "task-core-draft-1"
AUTHORITY = "recovery-smoke"
PROJECT = "synthetic-recovery"


def write_private(path, value):
    raw = value if isinstance(value, str) else json.dumps(value, ensure_ascii=False, indent=2) + "\n"
    with path.open("x", encoding="utf-8") as stream:
        stream.write(raw)
        stream.flush()
        os.fsync(stream.fileno())


def digest_tree(directory):
    result = {}
    for path in sorted(directory.rglob("*")):
        if path.is_symlink():
            raise RuntimeError("backup must not contain symlinks")
        if path.is_file():
            result[str(path.relative_to(directory))] = hashlib.sha256(path.read_bytes()).hexdigest()
    return result


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, fp, code, message, headers, newurl):
        return None


class Rehearsal:
    def __init__(self, daemon, root):
        self.daemon, self.root = daemon, root
        self.process = None
        self.log = None
        self.resources = []
        self.current_resource = None
        self.tokens, self.proofs = {}, {}
        self.sequence = 0
        self.checks = []
        self.opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
        with socket.socket() as listener:
            listener.bind(("127.0.0.1", 0))
            self.port = listener.getsockname()[1]
        self.url = "http://127.0.0.1:" + str(self.port)
        self.source = root / "source"
        self.source.mkdir(mode=0o700)
        (root / "intents").mkdir(mode=0o700)
        credentials = []
        for who, kind in (("owner", "human"), ("worker", "agent")):
            raw = secrets.token_bytes(32)
            self.tokens[who] = base64.urlsafe_b64encode(raw).decode().rstrip("=")
            write_private(self.source / (who + "-token"), self.tokens[who] + "\n")
            credentials.append({"digest": hashlib.sha256(raw).hexdigest(),
                                "actor": {"kind": kind, "id": who},
                                "read": True, "write": True, "admin": who == "owner"})
        write_private(self.source / "server.json", {
            "authority": AUTHORITY, "database": "ledger.db", "listen": "127.0.0.1:" + str(self.port),
            "operators": [{"kind": "human", "id": "owner"}], "credentials": credentials})

    def exchange(self, who, route, payload, session=None, persist=True):
        raw = json.dumps(payload, separators=(",", ":")).encode()
        if route != "queries" and persist:
            self.sequence += 1
            write_private(self.root / "intents" / (str(self.sequence).zfill(3) + ".json"), raw.decode())
        headers = {"Content-Type": "application/json", "Authorization": "Bearer " + self.tokens[who]}
        if session:
            headers.update({"X-Ledger-Session": session, "X-Ledger-Session-Proof": self.proofs[session]})
        request = urllib.request.Request(self.url + "/v1/" + route, raw, headers)
        try:
            with self.opener.open(request, timeout=5) as response:
                if response.status != 200:
                    raise RuntimeError("unexpected HTTP status " + str(response.status))
                return json.load(response)
        except urllib.error.HTTPError as response:
            # A failed request may have committed; retain its original intent.
            error = json.load(response).get("error", {})
            raise RuntimeError("HTTP %s: %s/%s" % (response.code, error.get("scope"),
                               error.get("code", error.get("error", {}).get("kind")))) from None

    def query(self, operation, body=None, who="owner"):
        return self.exchange(who, "queries", {"binding_version": BINDING,
                             "operation": operation, "body": body or {}})["result"]

    def action(self, name, operation, body, revision=0, who="owner", session=None):
        request = {"binding_version": BINDING, "request_id": name, "operation": operation,
                   "expected_revision": revision, "body": body}
        return self.exchange(who, "actions", request, session)["result"]

    def command(self, name, task_id, operation, revision, body, app=None, who="owner", session=None):
        request = {"binding_version": BINDING, "command": {"protocol_version": PROTOCOL,
                   "command_id": name, "task": {"authority_id": AUTHORITY, "task_id": task_id},
                   "expected_revision": revision, "operation": operation, "body": body},
                   "application": app or {}}
        return self.exchange(who, "commands", request, session), request

    def save_resources(self):
        temporary = self.root / (".resources-" + secrets.token_hex(8))
        write_private(temporary, {"resources": self.resources})
        os.replace(temporary, self.root / "resources.json")
        descriptor = os.open(self.root, os.O_RDONLY | os.O_DIRECTORY)
        try:
            os.fsync(descriptor)
        finally:
            os.close(descriptor)

    def launch(self, args, role):
        resource = {"role": role, "argv": args, "pid": None, "proc_starttime": None,
                    "started_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
                    "owned": False, "cleanup": "not_started"}
        self.resources.append(resource)
        self.current_resource = resource
        self.save_resources()
        # Register the direct child before either termination signal can run
        # cleanup. Saved PIDs are evidence, never permission to signal a reused PID.
        previous = signal.pthread_sigmask(signal.SIG_BLOCK, {signal.SIGINT, signal.SIGTERM})
        try:
            self.process = subprocess.Popen(args, stdin=subprocess.DEVNULL, stdout=self.log, stderr=self.log)
            resource.update(pid=self.process.pid, owned=True, cleanup="pending")
            try:
                fields = Path("/proc/%s/stat" % self.process.pid).read_text().rsplit(")", 1)[1].split()
                resource["proc_starttime"] = fields[19]
            except FileNotFoundError:
                pass  # A fast initializer may already have exited.
            self.save_resources()
        finally:
            signal.pthread_sigmask(signal.SIG_SETMASK, previous)

    def start(self, directory, initialize=False):
        if self.process is not None:
            raise RuntimeError("previous child has not been stopped")
        self.log = (self.root / (directory.name + "-daemon.log")).open("ab")
        args = [str(self.daemon), "-config", str(directory / "server.json")]
        if initialize:
            self.launch(args + ["-init"], directory.name + "-init")
            code = self.process.wait(timeout=20)
            self.current_resource.update(cleanup="exited", exit_code=code)
            self.save_resources()
            self.process = None
            if code:
                raise RuntimeError("isolated initialization failed; inspect private daemon log")
        self.launch(args, directory.name + "-server")
        for _ in range(80):
            if self.process.poll() is not None:
                raise RuntimeError("isolated daemon exited before ready")
            try:
                info = self.query("service.info")
                assert (info["authority_id"], info["binding_version"], info["protocol_version"]) == (AUTHORITY, BINDING, PROTOCOL)
                return
            except (urllib.error.URLError, ConnectionError, TimeoutError):
                time.sleep(0.1)
        raise RuntimeError("isolated daemon readiness timeout")

    def stop(self, require_clean=True):
        failure = None
        if self.process is not None:
            child = self.process
            stop_method = "already_exited"
            if child.poll() is None:
                stop_method = "terminate"
                child.terminate()
                try:
                    child.wait(timeout=12)
                except subprocess.TimeoutExpired:
                    stop_method = "kill"
                    child.kill()
                    child.wait(timeout=5)
                    failure = "owned daemon did not stop gracefully; no cold backup allowed"
            if child.returncode != 0 and failure is None:
                failure = "owned daemon exit was not clean: " + str(child.returncode)
            self.current_resource.update(cleanup="exited", exit_code=child.returncode,
                                         stop_method=stop_method, graceful=child.returncode == 0)
            self.process = None
            self.save_resources()
        if self.log is not None:
            self.log.close()
            self.log = None
        if failure and require_clean:
            raise RuntimeError(failure)

    def session(self, task_id):
        session = "session-" + task_id
        raw = secrets.token_bytes(32)
        self.proofs[session] = base64.urlsafe_b64encode(raw).decode().rstrip("=")
        write_private(self.source / (session + ".json"), {"session_id": session, "proof": self.proofs[session]})
        self.action("begin-" + task_id, "session.begin", {"session_id": session, "project_id": PROJECT,
                    "goal": "Synthetic cold-state recovery test", "proof_digest": hashlib.sha256(raw).hexdigest()}, who="worker")
        return session

    def create_and_start(self, task_id):
        self.command("create-" + task_id, task_id, "task.create", 0,
                     {"goal": "Synthetic recovery " + task_id, "expected_result": "A preserved public task history"},
                     {"project_id": PROJECT, "claim_policy": {"mode": "workspace"}})
        session = self.session(task_id)
        claimed, request = self.command("claim-" + task_id, task_id, "task.claim", 1, {},
                                        {"session_id": session}, "worker", session)
        task = claimed["result"]["task"]
        started, _ = self.command("start-" + task_id, task_id, "run.start", task["revision"], {
            "assignment_id": task["current_assignment_id"], "generation": task["generation"],
            "basis_version": task["expectation_version"]}, who="worker", session=session)
        return started["result"]["task"], session, claimed, request

    def finish(self, task, session):
        tid = task["task_id"]
        submitted, _ = self.command("submit-" + tid, tid, "run.submit", task["revision"], {
            "assignment_id": task["current_assignment_id"], "generation": task["generation"],
            "run_id": task["current_run_id"], "basis_version": task["expectation_version"],
            "summary": "Synthetic recovery evidence; this is automated fixture acceptance"}, who="worker", session=session)
        current = submitted["result"]["task"]
        decided, _ = self.command("accept-" + tid, tid, "task.decide", current["revision"], {
            "submission_id": current["pending_submission_id"], "outcome": "accepted",
            "reason": "Isolated harness checked the synthetic result; not human product acceptance"})
        assert decided["result"]["task"]["state"] == "completed"
        value = self.query("session.get", {"session_id": session})
        assert value["current_binding"] is None
        self.action("end-" + tid, "session.end", {"session_id": session, "outcome": "done",
                    "summary": "Synthetic recovery check ended"}, value["session"]["revision"], "worker", session)

    def run(self):
        self.start(self.source, initialize=True)
        self.action("worker", "participant.create", {"actor": {"kind": "agent", "id": "worker"},
                    "display_name": "Recovery fixture", "state": "enabled", "profile": {}})
        self.action("project", "project.create", {"project_id": PROJECT, "title": "Recovery fixture", "description": "Synthetic public HTTP evidence"})
        finished, done_session, old_claim, claim_request = self.create_and_start("finished")
        self.finish(finished, done_session)
        active, active_session, _, _ = self.create_and_start("continuation")
        before = self.query("workspace.snapshot")
        session_before = self.query("session.get", {"session_id": active_session})
        cursor = self.query("events.open")["cursor"]
        first_page = self.query("events.read", {"cursor": cursor, "limit": 2})
        self.stop()
        self.checks.append("source exited cleanly before any file copy")

        cold, restored = self.root / "cold-backup", self.root / "restored"
        manifest = digest_tree(self.source)
        shutil.copytree(self.source, cold)
        assert digest_tree(cold) == manifest, "cold backup checksum mismatch"
        write_private(self.root / "cold-manifest.json", manifest)
        shutil.copytree(cold, restored)
        assert digest_tree(restored) == manifest, "restored checksum mismatch"
        self.checks.append("complete private stopped directory copied twice with exact SHA-256 verification")
        self.start(restored)
        assert self.query("workspace.snapshot") == before, "public snapshot or watermark changed on relocation"
        assert self.query("session.get", {"session_id": active_session}) == session_before
        assert self.query("events.read", {"cursor": cursor, "limit": 2}) == first_page
        self.checks.append("tasks, records, sessions, bindings, watermark and saved event page survive relocation")

        replay = self.exchange("worker", "commands", claim_request, done_session)
        assert replay["result"]["replayed"] is True
        expected = json.loads(json.dumps(old_claim))
        expected["result"]["replayed"] = True
        assert replay == expected, "historical claim receipt was not preserved"
        assert self.query("workspace.snapshot") == before, "receipt replay changed public facts"
        self.checks.append("original claim ID and request recover the historical receipt without new facts")

        self.finish(active, active_session)
        after = self.query("workspace.snapshot")
        assert len(after["tasks"]) == 2
        assert all(bundle["snapshot"]["task"]["state"] == "completed" for bundle in after["tasks"])
        for sid in (done_session, active_session):
            status = self.query("session.get", {"session_id": sid})
            assert status["session"]["state"] == "ended" and status["current_binding"] is None
        follow = self.query("events.read", {"cursor": before["event_cursor"], "limit": 200})
        assert follow["events"] and follow["done"], "saved follow cursor did not observe new commits"
        self.checks.append("restored active execution submits and issuer accepts; saved follow cursor sees new commits")
        self.stop()
        assert digest_tree(self.source) == manifest, "stopped source was modified"
        assert digest_tree(cold) == manifest, "cold backup was modified"
        self.checks.append("both owned daemons exited; original and backup remained stopped and unchanged")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--daemon", required=True, type=Path)
    parser.add_argument("--dir", required=True, type=Path, help="absolute, nonexistent private evidence directory")
    args = parser.parse_args()
    if not args.daemon.is_absolute() or not args.daemon.is_file() or not os.access(args.daemon, os.X_OK):
        parser.error("--daemon must name an absolute executable file")
    if not args.dir.is_absolute() or args.dir.exists() or args.dir.is_symlink():
        parser.error("--dir must be an absolute nonexistent directory; existing data is never overwritten")
    os.umask(0o077)
    args.dir.mkdir(mode=0o700)
    runner = None
    report = {"passed": False, "scope": "synthetic latest stopped-state relocation; no historical rollback or disaster recovery claim"}
    try:
        runner = Rehearsal(args.daemon.resolve(), args.dir)
        runner.run()
        report.update(passed=True, checks=runner.checks,
                      daemon_sha256=hashlib.sha256(args.daemon.read_bytes()).hexdigest())
    except (Exception, KeyboardInterrupt) as error:
        report["error"] = str(error)
        raise
    finally:
        try:
            if runner:
                runner.stop(require_clean=False)
        except Exception as error:
            report.update(passed=False, cleanup_error=str(error))
            raise
        finally:
            write_private(args.dir / "recovery-results.json", report)
            print(json.dumps(report, ensure_ascii=False, indent=2))


def interrupted(_signum, _frame):
    raise KeyboardInterrupt("recovery rehearsal interrupted")


if __name__ == "__main__":
    signal.signal(signal.SIGINT, interrupted)
    signal.signal(signal.SIGTERM, interrupted)
    main()
