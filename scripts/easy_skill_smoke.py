#!/usr/bin/env python3
"""Exercise the convenient Skill CLI against an isolated, real Go service.

Only service initialization and participant/project registration use raw HTTP.
All task execution and acceptance use agent.py. This deterministic harness does
not launch language models; --serve supplies the same private fixture for a
separate native-Agent acceptance. Private state is retained for diagnosis.
"""
import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import secrets
import signal
import socket
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request


REPO = Path(__file__).resolve().parents[1]
CLIENT = REPO / "skills/task-ledger/scripts/agent.py"
BINDING = "ledger-http-draft-1"
PROTOCOL = "task-core-draft-1"
AUTHORITY = "easy-skill-smoke"
PROJECT = "easy-skill"
ACTORS = ("owner", "lead", "child-a", "child-b")


def private_write(path, value):
    raw = json.dumps(value, ensure_ascii=False, indent=2) + "\n"
    descriptor, name = tempfile.mkstemp(prefix=".write-", dir=path.parent)
    try:
        with os.fdopen(descriptor, "w", encoding="utf-8") as stream:
            stream.write(raw)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(name, path)
    finally:
        if os.path.exists(name):
            os.unlink(name)


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, fp, code, message, headers, newurl):
        return None


class Fixture:
    def __init__(self, daemon, root):
        self.daemon = daemon
        self.root = root
        self.process = None
        self.log = None
        self.tokens = {}
        self.profiles = {who: root / (who + "-profile") for who in ACTORS}
        self.opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
        with socket.socket() as listener:
            listener.bind(("127.0.0.1", 0))
            self.port = listener.getsockname()[1]
        self.url = "http://127.0.0.1:" + str(self.port)
        self.report = {
            "status": "preparing", "mode": "deterministic CLI smoke",
            "authority_id": AUTHORITY, "project_id": PROJECT,
            "binding_version": BINDING, "protocol_version": PROTOCOL,
            "binary_sha256": hashlib.sha256(daemon.read_bytes()).hexdigest(),
            "client_sha256": hashlib.sha256(CLIENT.read_bytes()).hexdigest(),
            "source_commit": subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=REPO, text=True).strip(),
            "source_dirty": bool(subprocess.check_output(["git", "status", "--porcelain"], cwd=REPO)),
            "started_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
            "checks": [], "tasks": {}, "resources": [],
            "boundary": "Synthetic local fixture; no model execution or production traffic.",
        }
        self.resource = None
        self.save()

    def save(self):
        private_write(self.root / "report.json", self.report)

    def note(self, name, **detail):
        self.report["checks"].append({"name": name, **detail})
        self.save()

    def bootstrap_http(self, operation, body=None, action=False):
        payload = {"binding_version": BINDING, "operation": operation, "body": body or {}}
        if action:
            payload.update(request_id="bootstrap-" + secrets.token_hex(12), expected_revision=0)
        request = urllib.request.Request(
            self.url + ("/v1/actions" if action else "/v1/queries"),
            json.dumps(payload).encode(),
            {"Content-Type": "application/json", "Authorization": "Bearer " + self.tokens["owner"]},
        )
        try:
            with self.opener.open(request, timeout=2) as response:
                require(response.status == 200, "bootstrap returned unexpected HTTP status")
                return json.load(response)["result"]
        except urllib.error.HTTPError as response:
            # Never put response bodies or credentials in the public report.
            raise RuntimeError("bootstrap %s failed: HTTP %s" % (operation, response.code)) from None

    def prepare(self):
        credentials = []
        for who in ACTORS:
            raw = secrets.token_bytes(32)
            token = base64.urlsafe_b64encode(raw).decode().rstrip("=")
            self.tokens[who] = token
            token_path = self.root / (who + "-token")
            descriptor = os.open(token_path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
            with os.fdopen(descriptor, "w", encoding="utf-8") as stream:
                stream.write(token + "\n")
            credentials.append({"digest": hashlib.sha256(raw).hexdigest(),
                                "actor": {"kind": "human" if who == "owner" else "agent", "id": who},
                                "read": True, "write": True, "admin": who == "owner"})
        private_write(self.root / "server.json", {
            "authority": AUTHORITY, "database": str(self.root / "ledger.db"),
            "listen": "127.0.0.1:" + str(self.port),
            "operators": [{"kind": "human", "id": "owner"}], "credentials": credentials,
        })
        with (self.root / "server.log").open("ab") as log:
            completed = subprocess.run([str(self.daemon), "-config", str(self.root / "server.json"), "-init"],
                                       stdout=log, stderr=log, stdin=subprocess.DEVNULL, timeout=20)
        require(completed.returncode == 0, "isolated initialization failed; inspect private server.log")
        self.start()
        for who in ACTORS[1:]:
            self.bootstrap_http("participant.create", {
                "actor": {"kind": "agent", "id": who}, "display_name": "Synthetic " + who,
                "state": "enabled", "profile": {},
            }, action=True)
        self.bootstrap_http("project.create", {"project_id": PROJECT, "title": "Convenient Skill fixture",
                                              "description": "Synthetic isolated Agent collaboration"}, action=True)
        for who in ACTORS:
            self.cli(who, "setup", "--url", self.url, "--credential-file", str(self.root / (who + "-token")),
                     "--project", PROJECT)
            self.cli(who, "check")
        self.note("four independent credential/profile identities configured")

    def start(self):
        require(self.process is None, "owned daemon is already running")
        self.log = (self.root / "server.log").open("ab")
        # Retain the Popen object: only this runner's unreaped child is signalled.
        previous = signal.pthread_sigmask(signal.SIG_BLOCK, {signal.SIGINT, signal.SIGTERM})
        try:
            self.process = subprocess.Popen([str(self.daemon), "-config", str(self.root / "server.json")],
                                            stdin=subprocess.DEVNULL, stdout=self.log, stderr=self.log)
            self.resource = {"pid": self.process.pid, "ownership": "direct child", "cleanup": "pending"}
            self.report["resources"].append(self.resource)
            self.save()
        finally:
            signal.pthread_sigmask(signal.SIG_SETMASK, previous)
        deadline = time.monotonic() + 15
        while time.monotonic() < deadline:
            require(self.process.poll() is None, "isolated daemon exited; inspect private server.log")
            try:
                info = self.bootstrap_http("service.info")
                require((info["authority_id"], info["binding_version"], info["protocol_version"]) ==
                        (AUTHORITY, BINDING, PROTOCOL), "isolated service version mismatch")
                self.report["upstream_commit"] = info["upstream_commit"]
                return
            except (urllib.error.URLError, ConnectionError, TimeoutError):
                time.sleep(0.1)
        raise RuntimeError("isolated daemon readiness timeout")

    def stop(self):
        if self.process is not None:
            child = self.process
            if child.poll() is None:
                child.terminate()
                try:
                    child.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    child.kill()
                    child.wait(timeout=5)
            self.resource.update(cleanup="stopped", exit_code=child.returncode)
            self.process = None
            self.save()
        if self.log is not None:
            self.log.close()
            self.log = None

    def cli(self, who, command, *args, expect_success=True):
        argv = [sys.executable, str(CLIENT), "--profile", str(self.profiles[who]), command, *map(str, args)]
        completed = subprocess.run(argv, stdin=subprocess.DEVNULL, capture_output=True, text=True, timeout=25)
        if expect_success:
            if completed.returncode != 0:
                # Output may be useful privately, but never copy untrusted client
                # output into the sanitized report or terminal.
                private_write(self.root / "client-failure.json", {
                    "actor": who, "command": command, "exit_code": completed.returncode,
                    "stdout": completed.stdout, "stderr": completed.stderr,
                })
                raise RuntimeError("Skill CLI %s/%s exited %s; inspect private client-failure.json" %
                                   (who, command, completed.returncode))
            try:
                value = json.loads(completed.stdout)
            except (json.JSONDecodeError, UnicodeError):
                raise RuntimeError("Skill CLI %s/%s did not return JSON" % (who, command)) from None
            require(isinstance(value, dict), "Skill CLI response must be an object")
            return value
        require(completed.returncode != 0, "forbidden CLI operation unexpectedly succeeded")
        return None

    def bundle(self, task_id, who="owner"):
        value = self.cli(who, "show", "--task", task_id)
        bundle = value.get("bundle", value)
        require("snapshot" in bundle and "application" in bundle, "show must expose the full TaskBundle")
        require(bundle["snapshot"]["task"]["task_id"] == task_id, "show returned another task")
        return bundle

    def task(self, task_id, state=None, who="owner"):
        task = self.bundle(task_id, who)["snapshot"]["task"]
        if state is not None:
            require(task["state"] == state, task_id + " expected state " + state)
        return task

    def records(self, task_id, record_type):
        return [item["record"] for item in self.bundle(task_id)["snapshot"]["records"]
                if item["record_type"] == record_type]

    def create(self, who, task_id, target, parent=None):
        args = ["--task", task_id, "--goal", "Synthetic collaboration " + task_id,
                "--expected-result", "Explicit reviewed result for " + task_id, "--target", target]
        if parent:
            args.extend(["--parent", parent])
        self.cli(who, "create", *args)
        task = self.task(task_id, "offered")
        require(task["issuer"]["id"] == who, "task issuer differs from creating profile")
        if parent:
            require({"kind": "parent", "target": {"authority_id": AUTHORITY, "task_id": parent}}
                    in task["relations"], "child is missing parent relation")

    def take(self, who, task_id):
        taken = self.cli(who, "take", "--task", task_id)
        require(taken.get("stage") == "started", "take did not finish the start stage")
        task = self.task(task_id, "active")
        binding = self.bundle(task_id)["application"]["execution_binding"]
        require(binding and binding["session_id"] == taken["session_id"], "take binding does not match own session")
        require(binding["run_id"] == task["current_run_id"] == taken["run_id"], "take Run does not match binding")
        return taken, task

    def submit(self, who, task_id, summary):
        result = self.cli(who, "submit", "--task", task_id, "--summary", summary)
        task = self.task(task_id, "submitted")
        require(result["submission_id"] == task["pending_submission_id"], "submit returned another Submission")
        require(self.bundle(task_id)["application"]["execution_binding"] is None,
                "submit must end the execution binding")
        ended = self.cli(who, "finish", "--task", task_id, "--summary", "Synthetic execution submitted")
        require(ended.get("state") == "ended" and ended.get("outcome") == "done",
                "finish must explicitly end the local session")
        self.task(task_id, "submitted")
        return task["pending_submission_id"]

    def decide(self, who, task_id, submission, outcome):
        self.cli(who, "decide", "--task", task_id, "--submission", submission,
                 "--outcome", outcome, "--reason", "Harness checked the synthetic result: " + outcome)
        return self.task(task_id, "completed" if outcome == "accepted" else "rejected")

    def run(self):
        self.report["status"] = "running"
        self.create("owner", "parent", "lead")
        lead, parent = self.take("lead", "parent")
        self.cli("lead", "report", "--task", "parent", "--message", "Splitting into two independently reviewed children")
        self.create("lead", "child-a-task", "child-a", "parent")
        self.create("lead", "child-b-task", "child-b", "parent")
        self.cli("child-a", "tasks", "--state", "offered", "--target", "child-a")
        self.take("child-a", "child-a-task")
        self.cli("child-a", "report", "--task", "child-a-task", "--message", "Synthetic child A result checked")
        a_submission = self.submit("child-a", "child-a-task", "Child A: verified synthetic result")
        self.decide("lead", "child-a-task", a_submission, "accepted")
        require(self.task("parent", "active")["current_run_id"] == parent["current_run_id"],
                "child acceptance must not finish or replace the parent Run")
        self.note("lead owns parent while creating and accepting directed child A",
                  parent_run_id=lead["run_id"], child_submission_id=a_submission)

        first, first_task = self.take("child-b", "child-b-task")
        rejected = self.submit("child-b", "child-b-task", "Child B: intentionally incomplete first result")
        self.decide("lead", "child-b-task", rejected, "rejected")
        self.cli("lead", "reopen", "--task", "child-b-task", "--reason", "Complete missing synthetic acceptance evidence")
        self.task("child-b-task", "offered")
        second, second_task = self.take("child-b", "child-b-task")
        require(second_task["generation"] > first_task["generation"], "reopened task must use a new generation")
        require(second["run_id"] != first["run_id"], "reopened execution reused an old Run")
        require(second["session_id"] != first["session_id"], "new execution reused an ended session")
        self.cli("child-b", "report", "--task", "child-b-task", "--message", "Second Run corrected the requested evidence")
        before_restart = self.bundle("child-b-task")
        self.stop()
        require(self.resource["exit_code"] == 0, "daemon restart requires a clean stop")
        self.start()
        recovery = self.cli("child-b", "resume", "--task", "child-b-task")
        require(recovery["bundle"] == before_restart, "resume did not return the current task facts")
        require(recovery["session"]["session"]["session_id"] == second["session_id"],
                "resume did not identify the local session")
        require(recovery["session"]["current_binding"] == before_restart["application"]["execution_binding"],
                "resume session binding differs from task binding")
        require(recovery["pending_operations"] == [], "normal restart unexpectedly has pending writes")
        resumed, _ = self.take("child-b", "child-b-task")
        require(resumed["session_id"] == second["session_id"] and resumed["run_id"] == second["run_id"],
                "restart continuation replaced the session or Run")
        require(self.bundle("child-b-task") == before_restart, "read/resume/take changed current execution facts")
        self.cli("lead", "resume", "--task", "parent")
        require(self.task("parent", "active")["current_run_id"] == lead["run_id"], "restart lost parent execution")
        self.note("daemon restart preserves parent and child execution; resume and take do not duplicate facts",
                  resumed_session_id=second["session_id"], resumed_run_id=second["run_id"])

        b_submission = self.submit("child-b", "child-b-task", "Child B: corrected and verified second result")
        self.decide("lead", "child-b-task", b_submission, "accepted")
        submissions = self.records("child-b-task", "Submission")
        decisions = self.records("child-b-task", "Decision")
        runs = self.records("child-b-task", "Run")
        require(len(submissions) == 2 and len(decisions) == 2 and len(runs) == 2,
                "rework must retain both Runs, Submissions and Decisions")
        require({item["outcome"] for item in decisions} == {"accepted", "rejected"},
                "rework history lost an acceptance decision")
        self.note("explicit rejection/reopen retains both generations and reviewed submissions",
                  rejected_submission_id=rejected, accepted_submission_id=b_submission,
                  first_generation=first_task["generation"], second_generation=second_task["generation"])

        self.task("parent", "active")
        self.cli("lead", "report", "--task", "parent", "--message", "Both child submissions accepted; integration checked")
        parent_submission = self.submit("lead", "parent", "Integrated accepted child-a-task and child-b-task results")
        self.decide("owner", "parent", parent_submission, "accepted")
        for task_id in ("parent", "child-a-task", "child-b-task"):
            task = self.task(task_id, "completed")
            self.report["tasks"][task_id] = {key: task[key] for key in ("task_id", "state", "revision", "generation")}
        self.note("parent completes only after original owner explicitly accepts the lead submission",
                  submission_id=parent_submission)
        self.report["status"] = "passed"
        self.save()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--daemon", required=True, help="path to the built taskledgerd executable")
    parser.add_argument("--dir", type=Path, help="new private output directory (default: fresh temporary directory)")
    parser.add_argument("--serve", action="store_true", help="keep the bootstrapped fixture running for native Agent acceptance")
    args = parser.parse_args()
    os.umask(0o077)
    daemon = Path(args.daemon).resolve()
    if not daemon.is_file() or not os.access(daemon, os.X_OK):
        parser.error("--daemon must be an executable file")
    if not CLIENT.is_file():
        parser.error("the high-level Skill agent.py is missing")
    root = args.dir.resolve() if args.dir else Path(tempfile.mkdtemp(prefix="task-ledger-easy-"))
    if args.dir:
        root.mkdir(mode=0o700, parents=False, exist_ok=False)
    fixture = Fixture(daemon, root)

    def interrupted(signum, frame):
        raise KeyboardInterrupt

    signal.signal(signal.SIGINT, interrupted)
    signal.signal(signal.SIGTERM, interrupted)
    exit_code = 0
    try:
        fixture.prepare()
        if args.serve:
            fixture.report.update(status="serving", mode="fixture for separately conducted native Agent acceptance",
                                  boundary="Bootstrap only; task and model acceptance must be recorded separately.")
            fixture.save()
            print(json.dumps({"root": str(root), "url": fixture.url,
                              "profiles": {who: str(path) for who, path in fixture.profiles.items()}}), flush=True)
            while True:
                require(fixture.process.poll() is None, "owned daemon exited while fixture was serving")
                time.sleep(0.5)
        else:
            fixture.run()
    except KeyboardInterrupt:
        fixture.report["status"] = "stopped" if args.serve else "interrupted"
        exit_code = 0 if args.serve else 130
    except Exception as error:
        fixture.report.update(status="failed", error=str(error))
        exit_code = 1
    finally:
        fixture.stop()
        fixture.report["ended_at"] = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
        fixture.save()
    if not args.serve or exit_code:
        print(json.dumps({"status": fixture.report["status"], "report": str(root / "report.json"),
                          "checks": len(fixture.report["checks"]),
                          **({"error": fixture.report["error"]} if "error" in fixture.report else {})}), flush=True)
    return exit_code


if __name__ == "__main__":
    sys.exit(main())
