#!/usr/bin/env python3
"""Task Ledger's everyday agent client: plain arguments, durable protocol writes."""
import argparse
import base64
import decimal
import fcntl
import hashlib
import http.client
import json
import os
from pathlib import Path
import secrets
import stat
import sys
import tempfile
import uuid

import ledger

BINDING = "ledger-http-draft-1"
PROTOCOL = "task-core-draft-1"


def encoded(value):
    """Preserve exact JSON numbers in public snapshots, including extensions."""
    if isinstance(value, decimal.Decimal):
        if not value.is_finite():
            raise ValueError("non-finite JSON number")
        return str(value).encode()
    if isinstance(value, dict):
        return b"{" + b",".join(encoded(k) + b":" + encoded(v) for k, v in value.items()) + b"}"
    if isinstance(value, list):
        return b"[" + b",".join(encoded(v) for v in value) + b"]"
    return json.dumps(value, ensure_ascii=False, allow_nan=False).encode("utf-8")


def private_directory(path):
    path = Path(path)
    path.mkdir(mode=0o700, parents=True, exist_ok=True)
    info = path.lstat()
    if not stat.S_ISDIR(info.st_mode) or info.st_mode & 0o077 or info.st_uid != os.getuid():
        raise ValueError("profile directories must be owner-owned with mode 0700")
    return path


def read_json(path, limit=ledger.MAX_RESPONSE):
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    try:
        info = os.fstat(fd)
        if not stat.S_ISREG(info.st_mode) or info.st_mode & 0o077 or info.st_uid != os.getuid():
            raise ValueError("profile files must be owner-owned with mode 0600")
        with os.fdopen(fd, "rb", closefd=False) as source:
            raw = source.read(limit + 1)
        if len(raw) > limit:
            raise ValueError("profile file exceeds size limit")
        return ledger.strict_json(raw)
    finally:
        os.close(fd)


def save(path, value):
    """Atomic durable replacement, only inside a validated private directory."""
    path = Path(path)
    private_directory(path.parent)
    if path.exists() or path.is_symlink():
        read_json(path)
    descriptor, temporary = tempfile.mkstemp(prefix=".pending-", dir=path.parent)
    try:
        with os.fdopen(descriptor, "wb") as stream:
            stream.write(encoded(value))
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temporary, path)
        descriptor = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY)
        try:
            os.fsync(descriptor)
        finally:
            os.close(descriptor)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


def identifier(prefix):
    return prefix + "-" + uuid.uuid4().hex


class ClientError(Exception):
    def __init__(self, payload, exit_code=2):
        self.payload, self.exit_code = payload, exit_code
        super().__init__(payload.get("error", "operation failed"))


class Agent:
    def __init__(self, profile, timeout=10):
        self.root = Path(profile).expanduser().absolute()
        self.timeout = timeout
        self.config_path = self.root / "client.json"
        self.profile_path = self.root / "profile.json"
        self.lock_fd = None

    def __enter__(self):
        private_directory(self.root)
        self.lock_fd = os.open(self.root / "lock", os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
        info = os.fstat(self.lock_fd)
        if not stat.S_ISREG(info.st_mode) or info.st_mode & 0o077 or info.st_uid != os.getuid():
            os.close(self.lock_fd)
            self.lock_fd = None
            raise ValueError("lock must be an owner-owned private regular file")
        try:
            fcntl.flock(self.lock_fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            os.close(self.lock_fd)
            self.lock_fd = None
            raise ValueError("profile is busy; parallel agents need separate profiles") from None
        for name in ("operations", "sessions", "checkpoints", "intents"):
            private_directory(self.root / name)
        return self

    def __exit__(self, *_):
        if self.lock_fd is not None:
            os.close(self.lock_fd)
            self.lock_fd = None

    @property
    def profile(self):
        return read_json(self.profile_path, ledger.MAX_REQUEST)

    def query(self, operation, body=None):
        envelope = {"binding_version": BINDING, "operation": operation, "body": body or {}}
        fd, name = tempfile.mkstemp(prefix=".query-", dir=self.root)
        try:
            with os.fdopen(fd, "wb") as stream:
                stream.write(encoded(envelope))
            code, raw = ledger.send(self.config_path, "queries", name, timeout=self.timeout)
        finally:
            os.unlink(name)
        reply = ledger.strict_json(raw)
        if code != 200 or reply.get("binding_version") != BINDING or not isinstance(reply.get("result"), dict):
            raise ClientError({"error": "query failed", "http_status": code, "response": reply})
        return reply["result"]

    def identity(self):
        code, raw = ledger.send(self.config_path, "identity", timeout=self.timeout)
        reply = ledger.strict_json(raw)
        if code != 200 or not isinstance(reply.get("actor"), dict):
            raise ClientError({"error": "identity check failed", "http_status": code, "response": reply})
        return reply

    def setup(self, url, credential_file, project, ca_file=None):
        credential = Path(credential_file).expanduser().absolute()
        ledger.token_bytes(ledger.private_read(credential).decode("ascii").strip())
        config = {"service_url": url.rstrip("/"), "credential_file": str(credential), "intent_dir": "intents"}
        if ca_file:
            config["ca_file"] = str(Path(ca_file).expanduser().absolute())
        if self.profile_path.exists():
            if read_json(self.config_path) != config or self.profile["project_id"] != project:
                raise ValueError("profile already configured; use a separate profile for another connection")
            return self.check()
        # A failed setup can be repeated against the same connection. Do not
        # repoint any existing write intents to a different authority.
        if self.config_path.exists() and read_json(self.config_path) != config:
            raise ValueError("incomplete profile has a different connection; use a new profile")
        save(self.config_path, config)
        identity, info = self.identity(), self.query("service.info")
        if info.get("binding_version") != BINDING or info.get("protocol_version") != PROTOCOL:
            raise ValueError("unsupported service protocol or binding")
        projects = self.query("workspace.snapshot").get("projects", [])
        if not any(p.get("project_id") == project for p in projects):
            raise ValueError("project not found; ask the administrator to configure it")
        save(self.profile_path, {"authority_id": info["authority_id"], "actor": identity["actor"],
                                "binding_version": BINDING, "protocol_version": PROTOCOL,
                                "project_id": project, "service_url": config["service_url"]})
        return {"status": "ready", "profile": str(self.root), **self.profile, "identity": identity}

    def check(self):
        profile = self.profile
        config = read_json(self.config_path)
        identity, info = self.identity(), self.query("service.info")
        if config["service_url"] != profile["service_url"] or identity["actor"] != profile["actor"]:
            raise ValueError("profile connection or actor changed; refusing identity drift")
        for key in ("authority_id", "binding_version", "protocol_version"):
            if info.get(key) != profile[key]:
                raise ValueError("service identity changed: " + key)
        return {"status": "ready", **profile, "identity": identity}

    def task_ref(self, task_id):
        return {"authority_id": self.profile["authority_id"], "task_id": task_id}

    def snapshot(self, task_id):
        result = self.query("task.snapshot", {"task": self.task_ref(task_id)})
        if result["bundle"]["application"]["project_id"] != self.profile["project_id"]:
            raise ValueError("task belongs to another project; use its configured profile")
        return result

    def operations(self):
        return [read_json(path) for path in sorted((self.root / "operations").glob("*/operation.json"))
                if not path.parent.name.startswith(".")]

    def pending(self):
        return [op for op in self.operations() if op["status"] in {"prepared", "unknown"}]

    def ensure_writable(self):
        pending = self.pending()
        if pending:
            raise ClientError({"error": "unresolved writes block new writes; run resume then retry",
                               "status": "blocked", "pending_operations": pending}, 3)
        return self.check()

    def write(self, route, envelope, session_path=None, operation_id=None):
        self.ensure_writable()
        operation_id = operation_id or identifier("op")
        if Path(operation_id).name != operation_id or operation_id.startswith("."):
            raise ValueError("invalid operation ID")
        directory = self.root / "operations" / operation_id
        if directory.exists():
            raise ValueError("operation already exists; use retry with its original ID")
        raw = encoded(envelope)
        if len(raw) > ledger.MAX_REQUEST:
            raise ValueError("request exceeds 1 MiB")
        request = envelope.get("command", envelope)
        metadata = {"operation_id": operation_id, "route": route, "status": "prepared",
                    "operation": request["operation"], "request_id": request.get("command_id", request.get("request_id")),
                    "session_file": str(session_path) if session_path else None,
                    "actor": self.profile["actor"], "authority_id": self.profile["authority_id"]}
        # Publish request and metadata together. An interruption before rename
        # leaves only an unsent staging directory; after rename retry has both.
        staging = Path(tempfile.mkdtemp(prefix=".prepared-", dir=directory.parent))
        ledger.durable_new(staging / "request.json", raw)
        save(staging / "operation.json", metadata)
        os.rename(staging, directory)
        descriptor = os.open(directory.parent, os.O_RDONLY | os.O_DIRECTORY)
        try:
            os.fsync(descriptor)
        finally:
            os.close(descriptor)
        return self._send(metadata, replay=False)

    def _send(self, metadata, replay):
        directory = self.root / "operations" / metadata["operation_id"]
        # Before network I/O, an interruption must be recoverable as unknown.
        metadata["status"] = "unknown"
        save(directory / "operation.json", metadata)
        try:
            code, raw = ledger.send(self.config_path, metadata["route"], directory / "request.json",
                                    session_path=metadata["session_file"], timeout=self.timeout)
            response = ledger.strict_json(raw)
            request = read_json(directory / "request.json", ledger.MAX_REQUEST)
            expected_id = "command_id" if metadata["route"] == "commands" else "request_id"
            receipt = response.get("result", {}) if metadata["route"] == "commands" else response
            success = (code == 200 and response.get("binding_version") == BINDING
                       and isinstance(response.get("result"), dict)
                       and isinstance(receipt, dict)
                       and receipt.get(expected_id) == metadata["request_id"])
            if success and metadata["route"] == "commands":
                task = receipt.get("task")
                success = (receipt.get("protocol_version") == PROTOCOL and isinstance(task, dict)
                           and isinstance(receipt.get("records"), list)
                           and all(task.get(key) == value for key, value in request["command"]["task"].items())
                           and isinstance(task.get("revision"), int))
            elif success:
                success = response["result"].get("session_id") == request["body"].get("session_id")
            rejected = (not replay and 400 <= code < 500 and response.get("binding_version") == BINDING
                        and isinstance(response.get("error"), dict))
            # Store full responses separately; their budget is 16 MiB, not the
            # 1 MiB request budget. Only validated success resolves a replay.
            save(directory / "response.json", response)
            metadata["status"] = "succeeded" if success else "rejected" if rejected else "unknown"
            metadata["http_status"] = code
            save(directory / "operation.json", metadata)
        except (OSError, ValueError, http.client.HTTPException) as error:
            raise ClientError({"error": "write outcome unknown; retain operation and retry original request",
                               "operation_id": metadata["operation_id"], "status": "unknown",
                               "detail": type(error).__name__}, 3) from None
        if metadata["status"] != "succeeded":
            raise ClientError({"error": "write " + metadata["status"], "status": metadata["status"],
                               "operation_id": metadata["operation_id"], "http_status": code,
                               "response": response}, 3 if metadata["status"] == "unknown" else 2)
        return {"operation_id": metadata["operation_id"], "status": "succeeded", "response": response}

    def retry(self, operation_id):
        if not operation_id or Path(operation_id).name != operation_id or operation_id.startswith("."):
            raise ValueError("invalid operation ID")
        metadata = read_json(self.root / "operations" / operation_id / "operation.json")
        self.check()
        if metadata["actor"] != self.profile["actor"] or metadata["authority_id"] != self.profile["authority_id"]:
            raise ValueError("operation identity does not match profile")
        if metadata["status"] == "succeeded":
            return {"operation_id": operation_id, "status": "succeeded", "historical": True,
                    "response": read_json(self.root / "operations" / operation_id / "response.json")}
        if metadata["status"] == "rejected":
            raise ValueError("operation was explicitly rejected; read current facts before a new decision")
        return self._send(metadata, replay=True)

    def command(self, task, operation, body, application=None, session_path=None, operation_id=None):
        task_id = task if isinstance(task, str) else task["task_id"]
        envelope = {"binding_version": BINDING, "command": {
            "protocol_version": PROTOCOL, "command_id": identifier("cmd"), "task": self.task_ref(task_id),
            "expected_revision": 0 if isinstance(task, str) else task["revision"],
            "operation": operation, "body": body}, "application": application or {}}
        result = self.write("commands", envelope, session_path, operation_id)
        updated = result["response"]["result"]["task"]
        return {"operation_id": result["operation_id"], **self.task_summary(updated),
                "records": result["response"]["result"].get("records", [])}

    def action(self, operation, body, revision=0, session_path=None, operation_id=None):
        result = self.write("actions", {"binding_version": BINDING, "request_id": identifier("req"),
                            "operation": operation, "expected_revision": revision, "body": body},
                            session_path, operation_id)
        return {"operation_id": result["operation_id"], **result["response"]["result"]}

    @staticmethod
    def task_summary(task):
        return {"task_id": task["task_id"], "state": task["state"], "revision": task["revision"],
                "generation": task["generation"], "assignment_id": task.get("current_assignment_id"),
                "run_id": task.get("current_run_id"), "submission_id": task.get("pending_submission_id")}

    def checkpoint_path(self, task_id):
        digest = hashlib.sha256(encoded(self.task_ref(task_id))).hexdigest()
        return self.root / "checkpoints" / (digest + ".json")

    def checkpoint(self, task_id):
        path = self.checkpoint_path(task_id)
        return read_json(path) if path.exists() else None

    def owned_execution(self, task_id, bundle):
        checkpoint = self.checkpoint(task_id)
        binding = bundle["application"].get("execution_binding")
        task = bundle["snapshot"]["task"]
        if not checkpoint or not binding or binding.get("session_id") != checkpoint["session_id"]:
            raise ValueError("current execution is not owned by this profile; resume to inspect")
        if (binding.get("state") != "current" or binding.get("task") != self.task_ref(task_id)
                or binding.get("assignment_id") != task.get("current_assignment_id")
                or binding.get("generation") != task.get("generation")
                or checkpoint["generation"] != task.get("generation")
                or binding.get("run_id") != task.get("current_run_id")):
            raise ValueError("execution binding no longer matches this checkpoint")
        session = self.query("session.get", {"session_id": checkpoint["session_id"]})
        if (session["session"].get("actor") != self.profile["actor"]
                or session["session"].get("state") != "active"
                or session["session"].get("project_id") != self.profile["project_id"]
                or session.get("current_binding") != binding):
            raise ValueError("session is ended, changed, or no longer owns this execution")
        return checkpoint, self.root / "sessions" / (checkpoint["session_id"] + ".json")

    def take(self, task_id):
        self.ensure_writable()
        bundle = self.snapshot(task_id)["bundle"]
        task = bundle["snapshot"]["task"]
        checkpoint = self.checkpoint(task_id)
        if task["state"] not in {"offered", "claimed", "active"}:
            raise ValueError("task is not available for execution: " + task["state"])
        if task["state"] == "offered":
            if not checkpoint or checkpoint["generation"] != task["generation"] + 1:
                sid = identifier("session")
                secret = secrets.token_bytes(32)
                ledger.durable_new(self.root / "sessions" / (sid + ".json"), encoded({
                    "session_id": sid, "secret": base64.urlsafe_b64encode(secret).decode().rstrip("=")}))
                checkpoint = {"task": self.task_ref(task_id), "session_id": sid,
                              "generation": task["generation"] + 1, "stage": "session_saved",
                              "begin_operation": identifier("op"), "goal": task["expectations"][-1]["goal"]}
                save(self.checkpoint_path(task_id), checkpoint)
            session_path = self.root / "sessions" / (checkpoint["session_id"] + ".json")
            begin_path = self.root / "operations" / checkpoint["begin_operation"] / "operation.json"
            if checkpoint["stage"] == "session_saved":
                begun = begin_path.exists() and read_json(begin_path)["status"] == "succeeded"
                if not begun:
                    if begin_path.exists():
                        # A confirmed rejection permits a new explicit begin
                        # attempt, retaining this session's proof and identity.
                        checkpoint["begin_operation"] = identifier("op")
                        save(self.checkpoint_path(task_id), checkpoint)
                    own = ledger.session(session_path)
                    self.action("session.begin", {"session_id": own["session_id"],
                        "project_id": self.profile["project_id"], "goal": checkpoint["goal"],
                        "proof_digest": hashlib.sha256(ledger.token_bytes(own["secret"])).hexdigest()},
                        operation_id=checkpoint["begin_operation"])
                checkpoint["stage"] = "session_begun"
                save(self.checkpoint_path(task_id), checkpoint)
            self.command(task, "task.claim", {}, {"session_id": checkpoint["session_id"]}, session_path)
            checkpoint["stage"] = "claimed"
            save(self.checkpoint_path(task_id), checkpoint)
            bundle = self.snapshot(task_id)["bundle"]
            task = bundle["snapshot"]["task"]
        checkpoint, session_path = self.owned_execution(task_id, bundle)
        operation_id = None
        if task["state"] == "claimed":
            result = self.command(task, "run.start", {"assignment_id": task["current_assignment_id"],
                "generation": task["generation"], "basis_version": task["expectation_version"]},
                session_path=session_path)
            operation_id = result["operation_id"]
            task = self.snapshot(task_id)["bundle"]["snapshot"]["task"]
        checkpoint.update(stage="started", assignment_id=task["current_assignment_id"], run_id=task["current_run_id"])
        save(self.checkpoint_path(task_id), checkpoint)
        return {**self.task_summary(task), "session_id": checkpoint["session_id"], "stage": "started",
                "operation_id": operation_id}

    def resume(self, task_id=None):
        self.check()
        pending = self.pending()
        if task_id is None:
            return {"pending_operations": pending,
                    "checkpoints": [read_json(p) for p in sorted((self.root / "checkpoints").glob("*.json"))],
                    "next_action": "retry --operation ID" if pending else "resume --task TASK_ID"}
        bundle = self.snapshot(task_id)["bundle"]
        checkpoint = self.checkpoint(task_id)
        session = None
        if checkpoint:
            try:
                session = self.query("session.get", {"session_id": checkpoint["session_id"]})
            except ClientError as error:
                session = {"unavailable": error.payload}
        return {"task_id": task_id, "bundle": bundle, "checkpoint": checkpoint, "session": session,
                "pending_operations": pending,
                "next_action": "retry --operation ID" if pending else "inspect task, session and binding before continuing"}

    def execute(self, args):
        mode = args.mode
        if mode == "setup":
            return self.setup(args.url, args.credential_file, args.project, args.ca_file)
        if mode == "check":
            return self.check()
        if mode == "retry":
            return self.retry(args.operation)
        if mode == "resume":
            return self.resume(args.task)
        if mode == "take":
            return self.take(args.task)
        if mode in {"tasks", "show"}:
            self.check()
            if mode == "show":
                return self.snapshot(args.task)
            tasks = []
            for bundle in self.query("workspace.snapshot")["tasks"]:
                task, app = bundle["snapshot"]["task"], bundle["application"]
                if app["project_id"] != self.profile["project_id"] or args.state and task["state"] != args.state:
                    continue
                if args.target and app["claim_policy"].get("target") != {"kind": "agent", "id": args.target}:
                    continue
                tasks.append({**self.task_summary(task), "goal": task["expectations"][-1]["goal"],
                              "issuer": task["issuer"], "claim_policy": app["claim_policy"]})
            return {"tasks": tasks, "project_id": self.profile["project_id"]}
        self.ensure_writable()
        if mode == "create":
            body = {"goal": args.goal, "expected_result": args.expected_result}
            if args.constraint:
                body["constraints"] = args.constraint
            if args.parent:
                self.snapshot(args.parent)
                body["relations"] = [{"kind": "parent", "target": self.task_ref(args.parent)}]
            policy = {"mode": "target", "target": {"kind": "agent", "id": args.target}} if args.target else {"mode": "workspace"}
            return self.command(args.task or identifier("task"), "task.create", body,
                                {"project_id": self.profile["project_id"], "claim_policy": policy})
        bundle = self.snapshot(args.task)["bundle"]
        task = bundle["snapshot"]["task"]
        if mode in {"report", "submit"}:
            _, session_path = self.owned_execution(args.task, bundle)
            body = {"assignment_id": task["current_assignment_id"], "generation": task["generation"],
                    "run_id": task["current_run_id"]}
            if mode == "report":
                body["message"] = args.message
            else:
                runs = [r["record"] for r in bundle["snapshot"]["records"]
                        if r["record_type"] == "Run" and r["record"]["run_id"] == task["current_run_id"]]
                if len(runs) != 1:
                    raise ValueError("current Run record missing or ambiguous")
                body.update(basis_version=runs[0]["basis_version"], summary=args.summary,
                            artifacts=[{"uri": uri} for uri in args.artifact])
            return self.command(task, "run." + mode, body, session_path=session_path)
        if mode == "decide":
            if task.get("pending_submission_id") != args.submission:
                raise ValueError("explicit submission is not the current pending submission")
            return self.command(task, "task.decide", {"submission_id": args.submission,
                                "outcome": args.outcome, "reason": args.reason})
        if mode == "reopen":
            return self.command(task, "task.reopen", {"reason": args.reason})
        if mode == "release":
            session_path = None
            if task["issuer"] != self.profile["actor"]:
                _, session_path = self.owned_execution(args.task, bundle)
            return self.command(task, "assignment.release", {"assignment_id": task["current_assignment_id"],
                                "generation": task["generation"], "reason": args.reason}, session_path=session_path)
        if mode == "finish":
            checkpoint = self.checkpoint(args.task)
            if not checkpoint:
                raise ValueError("no local session for this task")
            session = self.query("session.get", {"session_id": checkpoint["session_id"]})["session"]
            if session["actor"] != self.profile["actor"]:
                raise ValueError("session belongs to another actor")
            if session["state"] == "ended":
                return {"session_id": session["session_id"], "state": "ended", "historical": True,
                        "outcome": session.get("outcome")}
            return self.action("session.end", {"session_id": session["session_id"], "outcome": args.outcome,
                               "summary": args.summary}, session["revision"],
                               self.root / "sessions" / (session["session_id"] + ".json"))
        raise ValueError("unsupported command")


def parser():
    result = argparse.ArgumentParser(description=__doc__)
    result.add_argument("--profile", required=True, help="private profile directory, one per agent")
    result.add_argument("--timeout", type=float, default=10)
    modes = result.add_subparsers(dest="mode", required=True)
    setup = modes.add_parser("setup", help="verify an existing service, credential and project")
    setup.add_argument("--url", required=True)
    setup.add_argument("--credential-file", required=True)
    setup.add_argument("--project", required=True)
    setup.add_argument("--ca-file")
    modes.add_parser("check")
    tasks = modes.add_parser("tasks")
    tasks.add_argument("--state")
    tasks.add_argument("--target")
    for name in ("show", "take", "report", "submit", "decide", "reopen", "release", "finish", "resume", "create"):
        command = modes.add_parser(name)
        command.add_argument("--task", required=name not in {"create", "resume"})
        if name == "create":
            command.add_argument("--goal", required=True)
            command.add_argument("--expected-result", required=True)
            command.add_argument("--parent")
            command.add_argument("--target")
            command.add_argument("--constraint", action="append", default=[])
        if name == "report":
            command.add_argument("--message", required=True)
        if name in {"submit", "finish"}:
            command.add_argument("--summary", required=True)
        if name == "submit":
            command.add_argument("--artifact", action="append", default=[], help="artifact URI/path reference; does not upload files")
        if name == "decide":
            command.add_argument("--submission", required=True)
            command.add_argument("--outcome", choices=("accepted", "rejected"), required=True)
        if name in {"decide", "reopen", "release"}:
            command.add_argument("--reason", required=True)
        if name == "finish":
            command.add_argument("--outcome", choices=("done", "failed", "cancelled"), default="done")
    retry = modes.add_parser("retry")
    retry.add_argument("--operation", required=True)
    return result


def main():
    args = parser().parse_args()
    try:
        with Agent(args.profile, args.timeout) as client:
            result = client.execute(args)
        print(encoded(result).decode("utf-8"))
        return 0
    except ClientError as error:
        print(encoded(error.payload).decode("utf-8"), file=sys.stderr)
        return error.exit_code
    except (OSError, ValueError, KeyError, TypeError, http.client.HTTPException) as error:
        # Transport errors can include URLs; never emit credential/proof values.
        message = str(error) if isinstance(error, ValueError) else type(error).__name__
        print(encoded({"error": message, "status": "local_error"}).decode(), file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
