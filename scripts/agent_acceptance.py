#!/usr/bin/env python3
"""Opt-in, real CLI Agent acceptance against an isolated taskledgerd process.

Requires already authenticated codex, pi and claude CLIs; --remote additionally
uses an explicitly authorized SSH host. Nothing runs as part of ordinary CI.
"""
import argparse
import base64
import concurrent.futures
import hashlib
import json
import os
import re
from pathlib import Path
import secrets
import shutil
import signal
import socket
import subprocess
import sys
import tempfile
import threading
import time
import urllib.error
import urllib.request

from http_checks import before_agents, after_agents

BINDING = "ledger-http-draft-1"
PROTOCOL = "task-core-draft-1"
AUTHORITY = "acceptance"
PROJECT = "synthetic"
REPO = Path(__file__).resolve().parents[1]


def private_write(path, value):
    path.write_text(value)
    path.chmod(0o600)


def free_port():
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


GUIDE = """# Real task-ledger client acceptance
Use only the supplied HTTP service and this isolated directory. Do not read the
server repo, database, other client directories, global credentials or private
configuration. Do not call p-drive or other services, register external work,
spawn agents, change global configuration, install dependencies or deploy code.
The outer acceptance runner owns orchestration and cleanup. Use your configured
model and tools to implement the requested HTTP client yourself unless given
the task-ledger skill. Python3 stdlib or curl is enough. Work autonomously.

Read client.json for service_url, actor, task_id and children. Your Bearer token
is in LEDGER_AUTH: read it in code from the environment, never echo/log it or
place it literally in source. A real credential cannot be obtained from an
ActorRef. Use Authorization: Bearer <token> and Content-Type: application/json.

POST /v1/queries: {binding_version:"ledger-http-draft-1",operation,body}.
service.info body={}; workspace.snapshot body={}; task.snapshot body={task:{
authority_id:"acceptance",task_id:...}} returns result.bundle.snapshot.task and
result.bundle.snapshot.records; result.bundle.application is host metadata.
session.get body={session_id}; result={session,current_binding}.

POST /v1/actions: {binding_version,request_id,operation,expected_revision,body}.
session.begin expected_revision=0, body={session_id,project_id:"synthetic",
goal,proof_digest}. Generate 32 random bytes; digest is SHA256 lowercase hex of
raw bytes; proof is unpadded base64url of those bytes. Store your session ID and
proof only in a private file chmod0600 so a later invocation can continue it.
For session.update/end and execution requests send X-Ledger-Session and
X-Ledger-Session-Proof. session.end body={session_id,outcome:"done",summary},
expected_revision from session.get; ending the session does NOT complete task.

POST /v1/commands: {binding_version,command:{protocol_version:"task-core-draft-1",
command_id,task:{authority_id:"acceptance",task_id},expected_revision,operation,
body},application:{...}}. A fresh command ID per intent, same full request and ID
for unknown-result retry. Persist each full original request before sending it.
Never put headers/secrets in the request log. Response result.task is current
at that commit. Query current state when resuming; replay is historical.

task.create expected_revision=0, body={goal,expected_result,relations?:[{kind:
"parent",target:{authority_id:"acceptance",task_id:"parent"}}]}, application={
project_id:"synthetic",claim_policy:{mode:"target",target:{kind:"agent",id:...}}}.
All subsequent commands use expected_revision of the latest Task.
task.claim body={}, application={session_id}; successful Task contains
current_assignment_id and generation. Other commands application={}.
run.start body={assignment_id,generation,basis_version:task.expectation_version};
successful Task contains current_run_id. run.report body={assignment_id,
generation,run_id,message}. run.submit body={assignment_id,generation,run_id,
basis_version,summary}. Submission is not completion: only the task's immutable
issuer can task.decide body={submission_id:task.pending_submission_id,
outcome:"accepted"|"rejected",reason}. A parent relationship grants no rights.

On an unexpected HTTP error inspect the status and JSON error without printing
credentials. Preserve original intent, query state, and repair the client if
its request violates the supplied schema. Never edit the server or fake success.
Do not accept your own assigned task. Report task ID, final state and result.
"""


class Acceptance:
    def __init__(self, args):
        self.args = args
        self.root = Path(tempfile.mkdtemp(prefix="task-ledger-e2e-"))
        self.resources = []
        self.processes = []
        self.save_lock = threading.Lock()
        self.results = {"status": "running", "scenarios": [], "agents": {}, "resources": self.resources,
                        "source_commit": subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=REPO, text=True).strip(),
                        "source_dirty": bool(subprocess.check_output(["git", "status", "--porcelain"], cwd=REPO)),
                        "started_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
                        "transport": "loopback HTTP; remote client over encrypted SSH reverse forwarding"}
        self.tokens = {}
        self.port = free_port()
        self.url = f"http://127.0.0.1:{self.port}"
        self.children = [{"id": "pi", "task_id": "child-pi", "goal": "Compute lowercase SHA256 of the exact UTF-8 text ledger-pi-v1 (no newline)",
                          "expected": hashlib.sha256(b"ledger-pi-v1").hexdigest()},
                         {"id": "claude", "task_id": "child-claude", "goal": "Sort and deduplicate gamma, alpha, gamma, beta; output comma-separated names without spaces", "expected": "alpha,beta,gamma"}]
        if args.remote:
            self.children.append({"id": "remote", "task_id": "child-remote", "goal": "Calculate the sum of squares of integers from 1 through 12 inclusive", "expected": "650"})

    def save(self):
        with self.save_lock:
            private_write(self.root / "report.json", json.dumps(self.results, indent=2, ensure_ascii=False))

    def note(self, name, **detail):
        item = {"scenario": name, **detail}
        self.results["scenarios"].append(item)
        self.save()
        print(json.dumps(item, ensure_ascii=False), flush=True)

    def spawn(self, argv, name, **kwargs):
        p = subprocess.Popen(argv, start_new_session=True, **kwargs)
        resource = {"name": name, "pid": p.pid, "pgid": p.pid, "ownership": "owned", "cleanup": "pending", "started_at": time.time()}
        self.resources.append(resource)
        self.processes.append((p, resource))
        self.save()
        return p, resource

    def stop(self, p, resource):
        if p.poll() is None:
            # p is this runner's unreaped direct child, so its PID cannot have
            # been reused. Signal only its separately created process group.
            os.killpg(p.pid, signal.SIGTERM)
            try:
                p.wait(timeout=10)
            except subprocess.TimeoutExpired:
                os.killpg(p.pid, signal.SIGKILL)
                p.wait(timeout=5)
        resource["cleanup"] = "stopped"
        self.save()

    def http(self, who, route, request, expected=200, headers=None):
        raw = request if isinstance(request, bytes) else json.dumps(request, separators=(",", ":")).encode()
        h = {"Content-Type": "application/json", "Authorization": "Bearer " + self.tokens[who]}
        h.update(headers or {})
        req = urllib.request.Request(self.url + "/v1/" + route, raw, h)
        try:
            with urllib.request.urlopen(req, timeout=10) as response:
                status, result = response.status, json.load(response)
        except urllib.error.HTTPError as e:
            status, result = e.code, json.load(e)
        if status != expected:
            raise AssertionError(f"{who} {route}: HTTP {status}, expected {expected}: {result.get('error')}")
        return result

    def query(self, who, op, body):
        return self.http(who, "queries", {"binding_version": BINDING, "operation": op, "body": body})["result"]

    def action(self, who, op, rev, body, rid=None):
        return self.http(who, "actions", {"binding_version": BINDING, "operation": op,
                         "request_id": rid or secrets.token_hex(12), "expected_revision": rev, "body": body})

    def command(self, who, tid, op, rev, body, app=None, cid=None, expected=200):
        return self.http(who, "commands", {"binding_version": BINDING, "command": {"protocol_version": PROTOCOL,
                         "operation": op, "command_id": cid or secrets.token_hex(12), "task": {"authority_id": AUTHORITY, "task_id": tid},
                         "expected_revision": rev, "body": body}, "application": app or {}}, expected=expected)

    def task(self, tid):
        return self.query("human", "task.snapshot", {"task": {"authority_id": AUTHORITY, "task_id": tid}})["bundle"]["snapshot"]

    def prepare(self):
        credentials = []
        for who in ["human", "codex"] + [c["id"] for c in self.children]:
            raw = secrets.token_bytes(32)
            self.tokens[who] = base64.urlsafe_b64encode(raw).decode().rstrip("=")
            credentials.append({"digest": hashlib.sha256(raw).hexdigest(), "actor": {"kind": "human" if who == "human" else "agent", "id": who},
                                "read": True, "write": True, "admin": who == "human"})
        cfg = {"authority": AUTHORITY, "database": str(self.root / "ledger.db"), "listen": f"127.0.0.1:{self.port}",
               "operators": [{"kind": "human", "id": "human"}], "credentials": credentials}
        self.cfg_path = self.root / "server.json"
        private_write(self.cfg_path, json.dumps(cfg))
        self.binary = str(Path(self.args.binary).resolve())
        self.results["binary_sha256"] = hashlib.sha256(Path(self.binary).read_bytes()).hexdigest()
        subprocess.run([self.binary, "-config", str(self.cfg_path), "-init"], check=True, timeout=20, capture_output=True)
        self.save()

    def start_server(self):
        log = open(self.root / "server.log", "ab")
        p, resource = self.spawn([self.binary, "-config", str(self.cfg_path)], "taskledgerd", stdout=log, stderr=log)
        resource.update({"port": self.port, "cwd": str(REPO), "config": str(self.cfg_path)})
        self.save()
        deadline = time.monotonic() + 30
        while time.monotonic() < deadline:
            if p.poll() is not None:
                raise RuntimeError("server exited before readiness; inspect private server.log")
            try:
                info = self.query("human", "service.info", {})
                assert info["authority_id"] == AUTHORITY and info["protocol_version"] == PROTOCOL
                return p, resource
            except urllib.error.URLError:
                time.sleep(0.1)
        self.stop(p, resource)
        raise TimeoutError("readiness deadline")

    def client_dir(self, who):
        d = self.root / who
        d.mkdir(mode=0o700, exist_ok=True)
        private_write(d / "PROTOCOL.md", GUIDE)
        cfg = {"service_url": self.url, "actor": {"kind": "agent", "id": who}, "task_id": "parent" if who == "codex" else "child-" + who,
               "children": self.children}
        private_write(d / "client.json", json.dumps(cfg, indent=2))
        if who == "pi":
            shutil.copytree(REPO / "skills/task-ledger", d / "skill", dirs_exist_ok=True)
            private_write(d / "credential", self.tokens[who])
            private_write(d / "skill-config.json", json.dumps({"service_url": self.url, "credential_file": "credential", "intent_dir": "intents"}))
        return d

    def run_agent(self, who, phase):
        d = self.client_dir(who)
        if phase == "delegate":
            job = "Claim and start parent. Publish all children in client.json as targeted tasks with parent relation to parent and expected_result from each child's expected field. Report that delegation is ready, leave parent active and your session active, then exit. Do not execute or accept the children's work."
        elif phase == "aggregate":
            job = "Resume your existing parent session using your private saved session file. Read each child's actual Submission, independently check its summary against the expected result. Only if correct decide accepted as that child's issuer. Query again to verify child completed. Then report and submit parent with a summary containing every child's result and TaskRef; end your session. Do not decide parent (you are not its issuer)."
        else:
            job = "Discover your targeted task from workspace.snapshot, then register your own session, claim, start, do the requested calculation, report progress, and submit. Submission summary must equal the computed answer exactly (no surrounding prose). End your session, then query task to confirm it remains submitted awaiting issuer. Do not decide it."
        if who == "pi":
            job += " Use skill/SKILL.md and its Python helper for HTTP calls, with skill-config.json. Generate your session proof using the helper."
        prompt = "Read PROTOCOL.md and client.json. This is an authorized isolated acceptance test with synthetic data. " + job
        if who == "codex":
            argv = ["codex", "--no-daemon", "-a", "never", "--disable", "hooks", "--disable", "plugins", "--disable", "multi_agent", "--disable", "apps",
                    "-c", "shell_environment_policy.inherit=\"all\"", "exec", "--ephemeral", "--skip-git-repo-check", "--sandbox", "danger-full-access", "--json", "-"]
        elif who == "pi":
            argv = ["pi", "-p", "--mode", "json", "--no-session", "--no-context-files", "--no-extensions", "--no-skills", "--no-prompt-templates", "--no-themes", "--tools", "read,bash,write,edit", prompt]
            if self.args.pi_provider:
                argv[1:1] = ["--provider", self.args.pi_provider]
            if self.args.pi_model:
                argv[1:1] = ["--model", self.args.pi_model]
        else:
            argv = ["claude", "-p", "--safe-mode", "--no-session-persistence", "--output-format", "json", "--permission-mode", "dontAsk",
                    "--tools", "Bash,Read,Write,Edit", "--allowedTools", "Bash,Read,Write,Edit"]
        env = dict(os.environ, LEDGER_AUTH=self.tokens[who])
        if who == "claude" and self.args.claude_direct:
            for name in ("HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"):
                env.pop(name, None)
        log = open(d / (phase + ".log"), "wb")
        p, resource = self.spawn(argv, who + "-" + phase, cwd=d, env=env, stdin=subprocess.PIPE, stdout=log, stderr=log)
        begin = time.monotonic()
        try:
            p.communicate(input=prompt.encode() if who in {"codex", "claude"} else b"", timeout=self.args.agent_timeout)
        except subprocess.TimeoutExpired:
            self.stop(p, resource)
            raise TimeoutError(who + " agent deadline; inspect private client log")
        finally:
            log.close()
            self.stop(p, resource)
        self.results["agents"][who + "-" + phase] = {"exit_code": p.returncode, "elapsed_seconds": round(time.monotonic() - begin, 3)}
        self.save()
        if p.returncode != 0:
            raise RuntimeError(who + " CLI failed; inspect private client log")
        if who == "pi":
            for line in (d / (phase + ".log")).read_text(errors="replace").splitlines():
                try:
                    event = json.loads(line)
                except ValueError:
                    continue
                if event.get("type") == "auto_retry_end" and event.get("success") is False:
                    raise RuntimeError("Pi model connection failed despite CLI exit 0")
        if phase == "work":
            if self.task("child-" + who)["task"]["state"] != "submitted":
                raise RuntimeError(who + " CLI exited without submitted server fact")
        self.note(who + "-" + phase, cli_exit=0, server_fact_checked=phase == "work")

    def run_remote(self):
        # The wrapper keeps the raw Bearer in memory, creates only private client
        # state, and removes its own remote directory even on model failure.
        wrapper = """import json,os,pathlib,shutil,subprocess,sys,tempfile,signal
p=json.load(sys.stdin); d=pathlib.Path(tempfile.mkdtemp(prefix='task-ledger-client-')); os.chmod(d,0o700)
child=None
try:
 for name,value in p['files'].items():
  f=d/name; f.write_text(value); f.chmod(0o600)
 env=dict(os.environ,LEDGER_AUTH=p['token']); env['PATH']=p['bin_dir']+':'+env.get('PATH','')
 child=subprocess.Popen(p['argv'],cwd=d,env=env,stdin=subprocess.PIPE,start_new_session=True)
 try: child.communicate(p['prompt'].encode(),timeout=p['timeout'])
 except subprocess.TimeoutExpired:
  os.killpg(child.pid,signal.SIGTERM)
  try: child.wait(timeout=10)
  except subprocess.TimeoutExpired: os.killpg(child.pid,signal.SIGKILL); child.wait()
  raise
 sys.exit(child.returncode)
finally:
 if child is not None and child.poll() is None: os.killpg(child.pid,signal.SIGTERM); child.wait(timeout=10)
 shutil.rmtree(d)
 print('REMOTE_PRIVATE_DIRECTORY_REMOVED',flush=True)
"""
        remote_port = self.args.remote_port
        tunnel_log = open(self.root / "ssh-tunnel.log", "wb")
        tunnel, resource = self.spawn(["ssh", "-oBatchMode=yes", "-oConnectTimeout=5", "-oExitOnForwardFailure=yes", "-oServerAliveInterval=15", "-N", "-R",
            f"127.0.0.1:{remote_port}:127.0.0.1:{self.port}", self.args.remote], "ssh-reverse-forward", stdout=tunnel_log, stderr=tunnel_log)
        try:
            time.sleep(1)
            if tunnel.poll() is not None:
                raise RuntimeError("SSH reverse forward failed")
            cfg = {"service_url": f"http://127.0.0.1:{remote_port}", "actor": {"kind": "agent", "id": "remote"}, "task_id": "child-remote", "children": self.children}
            prompt = "Read PROTOCOL.md and client.json. Register a private session, discover and claim your targeted task, start, compute the requested result, report, submit with summary equal to the numeric answer only, and end session. Verify task remains submitted. Write your own HTTP client; do not decide the task. This authorized test uses synthetic data only."
            argv = [self.args.remote_codex, "--no-daemon", "-a", "never", "--disable", "hooks", "--disable", "plugins", "--disable", "multi_agent", "--disable", "apps", "-c", "shell_environment_policy.inherit=\"all\"",
                    "exec", "--ephemeral", "--skip-git-repo-check", "--sandbox", "danger-full-access", "--json", "-"]
            payload = {"files": {"PROTOCOL.md": GUIDE, "client.json": json.dumps(cfg)}, "token": self.tokens["remote"], "prompt": prompt,
                       "bin_dir": str(Path(self.args.remote_codex).parent), "argv": argv, "timeout": self.args.agent_timeout}
            import shlex
            log = open(self.root / "remote.log", "wb")
            p, proc = self.spawn(["ssh", "-oBatchMode=yes", "-oConnectTimeout=5", self.args.remote, "python3 -c " + shlex.quote(wrapper)], "remote-agent-ssh", stdin=subprocess.PIPE, stdout=log, stderr=log)
            try:
                p.communicate(json.dumps(payload).encode(), timeout=self.args.agent_timeout + 30)
            finally:
                self.stop(p, proc)
                log.close()
            cleaned = "REMOTE_PRIVATE_DIRECTORY_REMOVED" in (self.root / "remote.log").read_text(errors="replace")
            self.results["agents"]["remote-work"] = {"exit_code": p.returncode, "remote_directory_removed": cleaned}
            if p.returncode != 0 or not cleaned:
                raise RuntimeError("remote CLI failed or cleanup unconfirmed; inspect private remote.log")
            self.note("remote-work", cli_exit=0, remote_directory_removed=True)
        finally:
            self.stop(tunnel, resource)
            tunnel_log.close()

    def execute(self):
        self.prepare()
        server, server_resource = self.start_server()
        try:
            for who in ["codex"] + [c["id"] for c in self.children]:
                self.action("human", "participant.create", 0, {"actor": {"kind": "agent", "id": who}, "display_name": who, "state": "enabled", "profile": {}})
            self.action("human", "project.create", 0, {"project_id": PROJECT, "title": "Synthetic acceptance", "description": "Isolated CLI interoperability"})
            before_agents(self)
            if self.args.http_only:
                after_agents(self)
                self.results["status"] = "passed"
                return
            self.command("human", "parent", "task.create", 0, {"goal": "Delegate and verify small calculations", "expected_result": "Verified child results, then issuer acceptance"},
                         {"project_id": PROJECT, "claim_policy": {"mode": "target", "target": {"kind": "agent", "id": "codex"}}}, cid="parent-create")
            self.note("readiness-and-bootstrap", authority=AUTHORITY, actors=list(self.tokens))
            self.run_agent("codex", "delegate")
            assert self.task("parent")["task"]["state"] == "active"
            for child in self.children:
                t = self.task(child["task_id"])["task"]
                assert t["state"] == "offered" and t["issuer"] == {"kind": "agent", "id": "codex"}
            with concurrent.futures.ThreadPoolExecutor(max_workers=3) as pool:
                futures = [pool.submit(self.run_agent, "pi", "work"), pool.submit(self.run_agent, "claude", "work")]
                if self.args.remote:
                    futures.append(pool.submit(self.run_remote))
                failures = []
                for future in futures:
                    try:
                        future.result()
                    except Exception as e:
                        failures.append(str(e))
                if failures:
                    raise RuntimeError("; ".join(failures))
            for child in self.children:
                snapshot = self.task(child["task_id"])
                assert snapshot["task"]["state"] == "submitted"
                submission = next(r["record"] for r in snapshot["records"] if r["record_type"] == "Submission")
                assert submission["summary"] == child["expected"], (child["id"], "wrong computed result")
            self.note("independent-deliveries", submitted=len(self.children), parent_state=self.task("parent")["task"]["state"])
            # The human parent issuer has no decision right on agent-issued children.
            child = self.task(self.children[0]["task_id"])["task"]
            rejected = self.command("human", child["task_id"], "task.decide", child["revision"],
                {"submission_id": child["pending_submission_id"], "outcome": "accepted", "reason": "Synthetic negative test"}, expected=403)
            assert rejected["error"]["error"]["kind"] == "ROLE_MISMATCH"
            self.note("parent-relation-does-not-authorize-child-decision", status=403)
            self.run_agent("codex", "aggregate")
            parent = self.task("parent")["task"]
            assert parent["state"] == "submitted"
            for c in self.children:
                assert self.task(c["task_id"])["task"]["state"] == "completed"
            self.command("human", "parent", "task.decide", parent["revision"], {"submission_id": parent["pending_submission_id"], "outcome": "accepted", "reason": "Synthetic human-role acceptance: all independently checked results match"})
            assert self.task("parent")["task"]["state"] == "completed"
            self.note("issuer-acceptance", children="completed", parent="completed", human_role="automated test actor, not live human review")
            cursor = self.query("human", "events.open", {})["cursor"]
            first = self.query("human", "events.read", {"cursor": cursor, "limit": 2})
            self.stop(server, server_resource)
            server, server_resource = self.start_server()
            assert self.query("human", "events.read", {"cursor": cursor, "limit": 2}) == first
            assert self.task("parent")["task"]["state"] == "completed"
            cross = self.http("pi", "queries", {"binding_version": BINDING, "operation": "events.read", "body": {"cursor": cursor, "limit": 2}}, expected=403)
            assert cross["error"]["code"] == "FORBIDDEN"
            self.note("restart-and-cursor-ownership", fixed_page_survived=True, different_actor_status=403)
            after_agents(self)
            self.results["status"] = "passed"
        finally:
            try:
                workspace = self.query("human", "workspace.snapshot", {})
                self.results["observed_tasks"] = [{"task_id": b["snapshot"]["task"]["task_id"], "state": b["snapshot"]["task"]["state"],
                    "revision": b["snapshot"]["task"]["revision"], "records": len(b["snapshot"]["records"])} for b in workspace["tasks"]]
                self.results["database_modes"] = {p.name: oct(p.stat().st_mode & 0o777) for p in self.root.glob("ledger.db*")}
                assert all(value == "0o600" for value in self.results["database_modes"].values())
            except Exception as e:
                self.results["final_observation_error"] = type(e).__name__
                if self.results["status"] == "passed":
                    self.results["status"] = "failed"
                    self.results["failure"] = "final public-state or file-permission verification failed"
            self.stop(server, server_resource)

    def cleanup(self):
        # Only files created by this isolated run; preserve report and private
        # diagnostic logs, remove database, credentials and client session state.
        for p, resource in self.processes:
            self.stop(p, resource)
        for who in ["codex", "pi", "claude"]:
            d = self.root / who
            if d.exists():
                for log in d.glob("*.log"):
                    shutil.copyfile(log, self.root / (who + "-" + log.name))
                shutil.rmtree(d)
        for name in ["ledger.db", "ledger.db-wal", "ledger.db-shm", "server.json"]:
            (self.root / name).unlink(missing_ok=True)
        for log in self.root.glob("*.log"):
            text = log.read_text(errors="replace")
            for token in self.tokens.values():
                text = text.replace(token, "[redacted-credential]")
            text = re.sub(r"(?<![A-Za-z0-9_-])[A-Za-z0-9_-]{43}(?![A-Za-z0-9_-])", "[redacted-possible-proof]", text)
            private_write(log, text)
        with socket.socket() as probe:
            probe.settimeout(1)
            self.results["service_port_released"] = probe.connect_ex(("127.0.0.1", self.port)) != 0
        if not self.results["service_port_released"]:
            self.results["status"] = "failed"
            self.results["failure"] = "owned service port still listening after cleanup"
        self.results["temporary_data_removed"] = True
        self.results["finished_at"] = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
        self.save()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True)
    parser.add_argument("--remote", help="Explicitly authorized user@host (optional)")
    parser.add_argument("--remote-port", type=int, default=18976)
    parser.add_argument("--remote-codex", help="Absolute Codex executable path on the authorized remote host")
    parser.add_argument("--agent-timeout", type=int, default=360)
    parser.add_argument("--pi-provider", help="Use an already configured Pi provider for this run only")
    parser.add_argument("--pi-model", help="Use an already configured Pi model for this run only")
    parser.add_argument("--claude-direct", action="store_true", help="Remove proxy environment variables only for the Claude subprocess")
    parser.add_argument("--http-only", action="store_true", help="Run deterministic real HTTP checks without model clients")
    args = parser.parse_args()
    if args.remote and (not args.remote_codex or not Path(args.remote_codex).is_absolute()):
        parser.error("--remote requires an explicit absolute --remote-codex path")
    os.umask(0o077)
    run = Acceptance(args)
    print("run_directory=" + str(run.root), flush=True)
    def interrupted(signum, frame):
        raise KeyboardInterrupt("acceptance interrupted")
    for sig in [signal.SIGINT, signal.SIGTERM, signal.SIGHUP]:
        signal.signal(sig, interrupted)
    try:
        run.execute()
    except BaseException as e:
        run.results["status"] = "failed"
        run.results["failure"] = str(e)
        raise
    finally:
        run.cleanup()
    if run.results["status"] != "passed":
        raise SystemExit(1)


if __name__ == "__main__":
    main()
