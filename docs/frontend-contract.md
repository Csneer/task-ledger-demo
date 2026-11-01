# Task Orbit frontend contract

2026-11-01 · implementation scope. No upstream protocol or pinned inputs change.

| UI | HTTP operation | Input/output and behavior |
| --- | --- | --- |
| Connect / actor-aware controls | GET `/v1/identity` (host addition) | Bearer maps to actor and credential capabilities; current service read access rechecked; no secret returned. Directory state further limits controls. |
| Workspace / task nodes | POST `/v1/queries`, `workspace.snapshot` | Complete task bundles, projects, participants, sessions, signed cursor. Overview, chain and task scopes filter actual displayed nodes. No server pagination; capacity errors are visible. |
| Overview / chain grouping / neighborhood | Client projection only | Full TaskRef, same-authority explicit parent/related/depends_on connections form groups; external references do not merge them. Pages contain up to 12 desktop / 4 mobile groups. A 24-node neighborhood and filters identify visible/total counts and partial paths. No title-based test classification or task mutation. |
| Live updates | `events.read` | Drain fixed batch with next_cursor; only after done refresh complete snapshot; dedupe event_id, no partial transaction projection. |
| Full task timeline | `events.open` + `events.read` | TaskRef-scoped history, paginated; distinct from current workspace cursor. |
| Global / chain historical playback | independent `events.open({})` + `events.read` | Drain fixed batches in service order; retain at most 1200 core event projections. Filter to the selected chain and visible neighborhood, identifying partial coverage. Follow its own cursor, never replace it with a snapshot cursor. |
| Single-task playback | independent `events.open({task: TaskRef})` + `events.read` | Real single-task node scope plus a separate task-scoped feed retaining up to 5000 events; not a filter of the global tail. Explicit recent-history labels on truncation. Playback remains read-only and separate from live feedback. |
| Create / child / variants | POST `/v1/commands`, `task.create` | Goal, expected_result, optional relations `{kind,target}`, application project and claim_policy. Each variant is separately committed. |
| Claim / start / report / submit | core commands + session.begin | Browser creates session proof, binds claim, uses matching headers. Never takes over another process's session. |
| Decide | `task.decide` | Immutable issuer, pending submission ID and current Task revision; explicit reason. |
| Retry | `task.reopen` | Only rejected/failed; terminal completed/cancelled create related new tasks instead. |
| Release / cancel / expectations | existing commands | Explicit action with reason and expected_revision; no generic setter. |
| Projects / participants / targeting | POST `/v1/actions` | Existing project.create, participant.create, claim_policy.update. The UI lists and registers participants; profile updates remain available through the API. Target only enabled agents. Registration never issues credentials. |

All core writes retain the full original command ID, revision, request and application fields on unknown outcome. Scope local persistence by authority and actor. A successful replay is followed by a current snapshot, not installed as current state. Multi-create stops on the first uncertain/failed item and retains planned task IDs for deliberate continuation. `io.taskledger.ui` extension only carries presentation group/kind metadata; never authority or permissions.

Overview page size is bounded by both width and available canvas height; the 12/4 desktop/mobile counts are upper bounds. This is solely a view-density rule and never limits the complete workspace query or changes task facts.

Execution grouping uses existing Assignment/Run/Submission records or generation > 0. Pending acceptance and failed-task summaries navigate to real task scopes; they are not inferred acceptance decisions. Client `kind: group` nodes are never sent as protocol TaskRefs in commands. Relation grouping does not imply state aggregation, delegation authority or event causality. Search locates actual tasks; state filters and neighborhood trimming preserve the full underlying snapshot.

The player synchronizes the recorded actor/action/assignment-round label with a highlighted task node and an optional following camera. Current task state is separately labelled 当前. Pan/zoom pauses camera following, not playback; restore-follow, previous/next, speed and complete step details are presentation controls. Dialogs and expanded step details suspend automatic progression and camera movement while retaining play intent. Replay never moves DOM focus. Reduced-motion, hidden/offscreen views and editing are respected. Partial/recent history and displayed X/Y counts must not imply a complete task process.

Overview pagination, neighborhood limits and event retention do not lift the backend's existing snapshot or response-size limits. These presentation behaviors introduce no new protocol operation, generic state setter or server-side pagination contract. Validation evidence belongs in the separate validation reports; this document specifies behavior and boundaries, not a claim that every scenario has passed.

No automatic agent launching: the current assistant launches subagents, which use the public skill and their own credentials. Web reads their reports and submissions. Missing launcher/handoff capabilities are not exposed as working controls.
