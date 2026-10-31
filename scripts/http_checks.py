"""Bounded deterministic checks against the acceptance runner's real HTTP server.

These helper requests are harness evidence, not independent Agent clients.
No database access, model invocation or credential/proof logging is performed.
"""
import base64
import concurrent.futures
import hashlib
import json
import secrets
import urllib.error
import urllib.request

BINDING = "ledger-http-draft-1"
PROTOCOL = "task-core-draft-1"
AUTHORITY = "acceptance"
PROJECT = "synthetic"


def _ref(tid):
    return {"authority_id": AUTHORITY, "task_id": tid}


def _command(tid, operation, revision, body, app=None, cid=None):
    return {"binding_version": BINDING, "command": {
        "protocol_version": PROTOCOL, "command_id": cid or secrets.token_hex(12),
        "task": _ref(tid), "expected_revision": revision,
        "operation": operation, "body": body}, "application": app or {}}


def _exchange(run, who, route, payload, headers=None):
    """Return status and body for a race whose expected winner is unknown."""
    raw = json.dumps(payload, separators=(",", ":")).encode()
    headers = {"Content-Type": "application/json",
               "Authorization": "Bearer " + run.tokens[who], **(headers or {})}
    request = urllib.request.Request(run.url + "/v1/" + route, raw, headers)
    try:
        with urllib.request.urlopen(request, timeout=10) as response:
            return response.status, json.load(response)
    except urllib.error.HTTPError as response:
        return response.code, json.load(response)


def _error(response, scope, code):
    error = response["error"]
    assert error["scope"] == scope
    actual = error["error"]["kind"] if scope == "core" else error["code"]
    assert actual == code, (scope, actual, code)


def _session(run, who, suffix):
    sid = "negative-" + who + "-" + suffix
    secret = secrets.token_bytes(32)
    run.action(who, "session.begin", 0, {
        "session_id": sid, "project_id": PROJECT,
        "goal": "Synthetic HTTP counterexample check",
        "proof_digest": hashlib.sha256(secret).hexdigest()})
    return {"who": who, "sid": sid, "headers": {
        "X-Ledger-Session": sid,
        "X-Ledger-Session-Proof": base64.urlsafe_b64encode(secret).decode().rstrip("=")}}


def _task_response(result):
    return result["result"]["task"]


def before_agents(run):
    """Run after directory/project bootstrap, before model-driven work."""
    suffix = secrets.token_hex(6)
    tids = []
    sessions = []
    failures = []
    run._http_check_tasks = tids
    run._http_check_sessions = sessions
    try:
        for who in ("pi", "claude"):
            sessions.append(_session(run, who, suffix))
        first, second = sessions

        replay_tid = "negative-retry-" + suffix
        create = _command(replay_tid, "task.create", 0, {
            "goal": "Synthetic concurrent retry", "expected_result": "One creation fact"},
            {"project_id": PROJECT, "claim_policy": {"mode": "workspace"}})
        # Register cleanup identity before any write could commit with a lost reply.
        tids.append(replay_tid)
        with concurrent.futures.ThreadPoolExecutor(max_workers=6) as pool:
            futures = [pool.submit(run.http, "human", "commands", create) for _ in range(6)]
            responses = [future.result() for future in futures]
        fresh = sum(result["result"]["replayed"] is False for result in responses)
        replayed = sum(result["result"]["replayed"] is True for result in responses)
        assert (fresh, replayed) == (1, 5), (fresh, replayed)
        assert run.task(replay_tid)["task"]["revision"] == 1
        run.note("http-concurrent-create-retry", clients=6, fresh=fresh, replayed=replayed,
                 evidence="deterministic HTTP harness")

        claim_requests = [_command(replay_tid, "task.claim", 1, {}, {"session_id": s["sid"]}) for s in sessions]
        with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
            futures = [pool.submit(_exchange, run, s["who"], "commands", req, s["headers"])
                       for s, req in zip(sessions, claim_requests)]
            claims = [future.result() for future in futures]
        assert sorted(status for status, _ in claims) == [200, 409]
        winner = next(i for i, (status, _) in enumerate(claims) if status == 200)
        _error(claims[1 - winner][1], "core", "STALE_REVISION")
        claimed = _task_response(claims[winner][1])
        snapshot = run.task(replay_tid)
        assert snapshot["task"]["revision"] == 2
        assert sum(r["record_type"] == "Assignment" for r in snapshot["records"]) == 1
        run.command("human", replay_tid, "assignment.release", claimed["revision"], {
            "assignment_id": claimed["current_assignment_id"], "generation": claimed["generation"],
            "reason": "End synthetic race"})
        run.note("http-concurrent-claim", statuses=[200, 409], assignments=1,
                 evidence="deterministic HTTP harness")

        proof_tid = "negative-proof-" + suffix
        tids.append(proof_tid)
        run.command("human", proof_tid, "task.create", 0, {
            "goal": "Synthetic execution fencing", "expected_result": "No stale writes"},
            {"project_id": PROJECT, "claim_policy": {"mode": "workspace"}})
        claim = _command(proof_tid, "task.claim", 1, {}, {"session_id": first["sid"]})
        # The session ID is correct, but no proof is supplied. Nothing may commit.
        denied = run.http(first["who"], "commands", claim, expected=403,
                          headers={"X-Ledger-Session": first["sid"]})
        _error(denied, "binding", "SESSION_PROOF_REQUIRED")
        assert run.task(proof_tid)["task"]["revision"] == 1
        claimed_result = run.http(first["who"], "commands", claim, headers=first["headers"])
        assert claimed_result["result"]["replayed"] is False
        claimed = _task_response(claimed_result)
        active = _task_response(run.http(first["who"], "commands", _command(proof_tid, "run.start", 2, {
            "assignment_id": claimed["current_assignment_id"], "generation": claimed["generation"],
            "basis_version": claimed["expectation_version"]}), headers=first["headers"]))
        run.command("human", proof_tid, "assignment.release", active["revision"], {
            "assignment_id": active["current_assignment_id"], "generation": active["generation"],
            "reason": "Explicitly replace synthetic assignee"})
        offered = run.task(proof_tid)["task"]
        replacement = _task_response(run.http(second["who"], "commands", _command(
            proof_tid, "task.claim", offered["revision"], {}, {"session_id": second["sid"]}),
            headers=second["headers"]))
        before = run.task(proof_tid)
        stale = run.http(first["who"], "commands", _command(proof_tid, "run.report", replacement["revision"], {
            "assignment_id": active["current_assignment_id"], "generation": active["generation"],
            "run_id": active["current_run_id"], "message": "This stale report must not commit"}),
            expected=409, headers=first["headers"])
        _error(stale, "core", "STALE_ASSIGNMENT")
        assert run.task(proof_tid) == before
        run.note("http-proof-retry-and-old-assignment", missing_proof=403, corrected_same_id="fresh",
                 stale_assignment=409, unchanged_snapshot=True, evidence="deterministic HTTP harness")

        query = {"binding_version": BINDING, "operation": "service.info", "body": {}}
        denied = run.http("human", "queries", query, expected=401,
                          headers={"Authorization": "Bearer invalid"})
        _error(denied, "binding", "UNAUTHENTICATED")
        media = run.http("human", "queries", query, expected=415,
                         headers={"Content-Type": "text/plain"})
        _error(media, "binding", "INVALID_MESSAGE")
        duplicate = run.http("human", "queries", (
            b'{"binding_version":"ledger-http-draft-1","operation":"service.info",'
            b'"operation":"workspace.snapshot","body":{}}'), expected=400)
        _error(duplicate, "binding", "INVALID_MESSAGE")
        run.note("http-wire-rejections", unauthenticated=401, wrong_media_type=415,
                 duplicate_json_key=400, evidence="deterministic HTTP harness")
    finally:
        # Try every owned object even if one cleanup request fails. On any failure
        # preserve the run evidence and fail acceptance rather than claim cleanup.
        for tid in tids:
            try:
                snapshot = run.task(tid)
                task = snapshot["task"]
                if task["current_assignment_id"] is not None:
                    task = _task_response(run.command("human", tid, "assignment.release", task["revision"], {
                        "assignment_id": task["current_assignment_id"], "generation": task["generation"],
                        "reason": "Cleanup synthetic HTTP check"}))
                if task["state"] not in {"completed", "cancelled"}:
                    run.command("human", tid, "task.cancel", task["revision"],
                                {"reason": "Cleanup synthetic HTTP check"})
            except Exception as error:
                failures.append({"kind": "task", "id": tid, "error_type": type(error).__name__})
        for session in sessions:
            try:
                current = run.query("human", "session.get", {"session_id": session["sid"]})["session"]
                if current["state"] != "ended":
                    run.http(session["who"], "actions", {
                        "binding_version": BINDING, "request_id": secrets.token_hex(12),
                        "operation": "session.end", "expected_revision": current["revision"],
                        "body": {"session_id": session["sid"], "outcome": "done",
                                 "summary": "Synthetic HTTP check ended"}}, headers=session["headers"])
            except Exception as error:
                failures.append({"kind": "session", "id": session["sid"], "error_type": type(error).__name__})
        # Proofs were only required during these checks; retain public IDs alone.
        run._http_check_sessions = [s["sid"] for s in sessions]
        if failures:
            run.note("http-check-cleanup", status="failed", failures=failures)
            raise AssertionError("HTTP counterexample cleanup failed; see redacted report")
        run.note("http-check-cleanup", status="passed", tasks_cancelled=len(tids), sessions_ended=len(sessions))


def after_agents(run):
    """Check scoped event pages without repeating restart/actor ownership checks."""
    tids = getattr(run, "_http_check_tasks", [])
    if not tids:
        # This phase can also be integrated into an already-running acceptance
        # execution that did not run before_agents. The parent is read-only here.
        tids = ["parent"]
    tid = tids[0]
    cursor = run.query("human", "events.open", {"task": _ref(tid)})["cursor"]
    first = run.query("human", "events.read", {"cursor": cursor, "limit": 2})
    assert run.query("human", "events.read", {"cursor": cursor, "limit": 2}) == first
    count = 0
    unique = set()
    core_creates = 0
    while True:
        page = run.query("human", "events.read", {"cursor": cursor, "limit": 2})
        for entry in page["events"]:
            event = entry["event"]
            assert event["task"] == _ref(tid), "task-scoped page leaked another task"
            assert event["event_id"] not in unique, "fixed batch duplicated an event"
            unique.add(event["event_id"])
            if entry["source"] == "core" and event["operation"] == "task.create":
                core_creates += 1
            count += 1
        if page["done"]:
            assert page["next_cursor"] is None and page["follow_cursor"]
            break
        assert page["next_cursor"] and page["follow_cursor"] is None
        cursor = page["next_cursor"]
        assert count < 100, "bounded synthetic page budget exceeded"
    assert core_creates == 1
    tampered = ("B" if cursor[0] == "A" else "A") + cursor[1:]
    invalid = run.http("human", "queries", {"binding_version": BINDING, "operation": "events.read",
                       "body": {"cursor": tampered, "limit": 2}}, expected=400)
    _error(invalid, "binding", "INVALID_CURSOR")
    for cleanup_tid in getattr(run, "_http_check_tasks", []):
        assert run.task(cleanup_tid)["task"]["state"] == "cancelled"
    for sid in getattr(run, "_http_check_sessions", []):
        assert run.query("human", "session.get", {"session_id": sid})["session"]["state"] == "ended"
    run.note("http-scoped-event-pages", fixed_page_repeat=True, events=count, unique_events=len(unique),
             task_create_events=core_creates, tampered_cursor=400,
             evidence="deterministic HTTP harness, not independent Agent evidence")
