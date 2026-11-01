#!/usr/bin/env python3
"""Exercise a shipped daemon and Skill in a new private directory, using public APIs."""
import argparse
import json
import os
from pathlib import Path
import re
import signal
import subprocess
import sys
import time
import urllib.request

BINDING = "ledger-http-draft-1"
PROTOCOL = "task-core-draft-1"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--release", type=Path, required=True)
    parser.add_argument("--dir", type=Path, required=True)
    args = parser.parse_args()
    release = args.release.resolve()
    target = args.dir.absolute()
    binary = release / "bin/taskledgerd"
    skill = release / "skills/task-ledger/scripts/ledger.py"
    os.umask(0o077)
    init = [sys.executable, str(release / "tools/init-config.py"), "--dir", str(target),
            "--authority", "release-smoke", "--port", "0"]
    subprocess.run(init, check=True, stdout=subprocess.DEVNULL)
    config = target / "server.json"
    original = config.read_bytes()
    rejected = subprocess.run(init, capture_output=True)
    assert rejected.returncode != 0 and config.read_bytes() == original, "existing bootstrap must not be replaced"
    assert target.stat().st_mode & 0o777 == 0o700
    for path in target.iterdir():
        assert path.stat().st_mode & 0o077 == 0, "bootstrap must remain private"
    subprocess.run([str(binary), "-config", str(config), "-init"], check=True, capture_output=True)
    log = (target / "daemon.log").open("w")
    daemon = None
    process_record = {"binary": str(binary), "config": str(config), "state": "not-started"}
    result = {"passed": False, "human_acceptance": False}
    completed = False
    try:
        daemon = subprocess.Popen([str(binary), "-config", str(config)], stdout=subprocess.DEVNULL, stderr=log)
        process_record.update(pid=daemon.pid, state="running")
        (target / "process.json").write_text(json.dumps(process_record) + "\n")
        url = None
        for _ in range(100):
            if daemon.poll() is not None:
                raise RuntimeError("daemon exited; inspect private daemon.log")
            match = re.search(r"ready address=(http://127\.0\.0\.1:\d+)", (target / "daemon.log").read_text())
            if match:
                url = match.group(1)
                break
            time.sleep(.05)
        assert url, "daemon readiness timeout"
        client_path = target / "owner-client.json"
        client = json.loads(client_path.read_text())
        client["service_url"] = url
        client_path.write_text(json.dumps(client) + "\n")
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        with opener.open(url + "/", timeout=5) as response:
            page = response.read().decode()
            assert response.status == 200 and "app.js" in page
            assert "default-src" in response.headers.get("Content-Security-Policy", "")
        for asset in ("/app.js", "/api.js", "/graph.js", "/history.js", "/journeys.js", "/ui.css"):
            with opener.open(url + asset, timeout=5) as response:
                assert response.status == 200 and response.read(), asset
        session_path = target / "session.json"
        session = "release-smoke-session"
        ref = {"authority_id": "release-smoke", "task_id": "release-smoke-task"}
        def invoke(kind, name, payload, proof=False):
            request = target / (name + ".json")
            raw = json.dumps(payload, ensure_ascii=False).encode() + b"\n"
            request.write_bytes(raw)
            command = [sys.executable, str(skill), kind, "--config", str(client_path), "--request", str(request)]
            if proof:
                command += ["--session-file", str(session_path)]
            result = subprocess.run(command, capture_output=True, timeout=15)
            (target / (name + ".response.json")).write_bytes(result.stdout)
            if result.returncode:
                raise RuntimeError("Skill operation failed: " + name + "; inspect private response")
            return json.loads(result.stdout)["result"]
        def query(name, operation, body=None):
            return invoke("query", name, {"binding_version": BINDING, "operation": operation, "body": body or {}})
        def action(name, operation, body, revision=0, proof=False):
            return invoke("action", name, {"binding_version": BINDING, "request_id": name,
                          "operation": operation, "body": body, "expected_revision": revision}, proof)
        def command(name, operation, body, revision, app=None, proof=False):
            return invoke("command", name, {"binding_version": BINDING, "command": {
                "protocol_version": PROTOCOL, "command_id": name, "task": ref,
                "expected_revision": revision, "operation": operation, "body": body},
                "application": app or {}}, proof)["task"]
        info = query("info", "service.info")
        assert info["protocol_version"] == PROTOCOL and info["authority_id"] == ref["authority_id"]
        action("project", "project.create", {"project_id": "smoke", "title": "Release smoke", "description": "Synthetic"})
        proof = subprocess.run([sys.executable, str(skill), "session-secret", "--session-id", session,
                                "--output", str(session_path)], capture_output=True, check=True)
        digest = json.loads(proof.stdout)["proof_digest"]
        action("session-begin", "session.begin", {"session_id": session, "project_id": "smoke",
               "goal": "Verify extracted release", "proof_digest": digest})
        task = command("create", "task.create", {"goal": "Calculate 6 times 7", "expected_result": "42"}, 0,
                       {"project_id": "smoke", "claim_policy": {"mode": "workspace"}})
        task = command("claim", "task.claim", {}, task["revision"], {"session_id": session}, True)
        assignment, generation, basis = task["current_assignment_id"], task["generation"], task["expectation_version"]
        task = command("start", "run.start", {"assignment_id": assignment, "generation": generation,
                       "basis_version": basis}, task["revision"], proof=True)
        run = task["current_run_id"]
        body = {"assignment_id": assignment, "generation": generation, "run_id": run}
        task = command("report", "run.report", dict(body, message="Computed result: 42"),
                       task["revision"], proof=True)
        task = command("submit", "run.submit", dict(body, basis_version=basis, summary=str(6 * 7)),
                       task["revision"], proof=True)
        assert task["state"] == "submitted", "submit must not complete task"
        task = command("accept", "task.decide", {"submission_id": task["pending_submission_id"],
                       "outcome": "accepted", "reason": "Synthetic issuer checked 6 * 7 == 42"}, task["revision"])
        assert task["state"] == "completed"
        session_result = query("session-status", "session.get", {"session_id": session})
        assert session_result["current_binding"] is None
        action("session-end", "session.end", {"session_id": session, "outcome": "done",
               "summary": "Synthetic release smoke complete"}, session_result["session"]["revision"], True)
        saved = query("task-status", "task.snapshot", {"task": ref})
        assert saved["bundle"]["snapshot"]["task"]["state"] == "completed"
        opened = query("events-open", "events.open")
        events = query("events-read", "events.read", {"cursor": opened["cursor"], "limit": 200})
        assert events["events"], "public event history must be readable"
        result = {"passed": False, "checks": ["private bootstrap; existing directory refused",
                  "extracted daemon and embedded frontend", "shipped Skill create/claim/start/report/submit/decide",
                  "submission remains pending until explicit synthetic issuer decision", "public snapshot and event history"],
                  "human_acceptance": False}
        completed = True
    finally:
        # Let the first signal enter cleanup; a second must not interrupt reaping.
        signal.signal(signal.SIGTERM, signal.SIG_IGN)
        signal.signal(signal.SIGINT, signal.SIG_IGN)
        if daemon is not None and daemon.poll() is None:
            daemon.terminate()
            try:
                daemon.wait(timeout=12)
            except subprocess.TimeoutExpired:
                daemon.kill()
                daemon.wait()
        log.close()
        stopped = daemon is None or daemon.poll() is not None
        returncode = daemon.returncode if daemon is not None else None
        process_record.update(state="stopped" if stopped else "cleanup-failed", returncode=returncode)
        (target / "process.json").write_text(json.dumps(process_record) + "\n")
        result.update(passed=completed and returncode == 0, daemon_stopped=stopped)
        (target / "smoke-results.json").write_text(json.dumps(result, indent=2) + "\n")
        print(json.dumps(result))
        if daemon is not None and daemon.returncode != 0:
            raise RuntimeError("daemon did not shut down cleanly; inspect private daemon.log")


def interrupted(_signum, _frame):
    raise KeyboardInterrupt("installation smoke interrupted")


if __name__ == "__main__":
    signal.signal(signal.SIGINT, interrupted)
    signal.signal(signal.SIGTERM, interrupted)
    main()
