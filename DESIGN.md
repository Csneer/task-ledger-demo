# Design

## Source of truth

Active · 2026-11-01. User approved the Task Orbit interactive concept: a task-first node canvas, violet/cyan ambient depth, flowing connections, hover summaries and on-demand dialogs. The previous card board and permanent left navigation were rejected. Evidence: existing HTTP contracts, pinned task-core semantics, and the approved code-native concept. This is a functional adaptation, not a pixel clone of a third-party site.

## Brand

Deep, calm, precise, alive. Dim environmental motion supports brighter task facts. No agent avatar wall, KPI dashboard, persistent sidebar or fake progress percentage.

## Product goals

Record work performed by the current Codex session and its subagents. Publish, discover, claim, report, deliver and accept through the public service. Explain parent/child work, retries and independent parallel attempts. No automatic agent launcher. Human issuer decisions remain explicit.

Completed work remains visibly traceable after reconnecting. Historical playback connects each recorded action to its task node, actor and assignment round. A following camera brings the active historical node into readable view without changing present task states or keyboard focus. Visual direction approval is not acceptance of every subsequent implementation; verification results are maintained separately.

## Personas and jobs

People publish and accept; agents record execution, create child tasks and deliver. A human can also execute. The current assistant launches subagents outside this application; each client claims using its own credentials and session proof.

## Information architecture

Three real display scopes: task-chain overview → one task chain → one task. Overview pages contain up to 12 groups on desktop or 4 on mobile; entering a chain filters actual nodes, and single-task scope contains only that task. Grouping uses explicit parent/related/depends_on connections between full TaskRefs in the same authority. External or missing references do not merge groups. Groups are client display objects, never protocol Tasks.

Separate work with execution records from work not yet claimed, using Assignment/Run/Submission facts or a positive generation; never classify tasks as tests from titles or prefixes. Pending acceptance and failure summaries help locate work needing attention. Search selects and locates an actual task. Chain filters and the bounded 24-node neighborhood show the visible/total count and explicitly identify a partial view and partial historical sequence.

Thin top navigation retains workspace/project, search, new task and connection status. Workspace popover exposes projects, participants and connection. Node tooltip gives summary; task details contain expectations, current assignment, reports, submissions, decisions, relations and actions. Bottom event strip opens recorded timeline. No persistent sidebar.

## Design principles

Task is the visual and authority object. Relation nodes are separate tasks, not lifecycle stages. Submission is pending acceptance. Parent completion is never inferred. Targeting is not claiming. Retry of rejected/failed tasks preserves identity/history; another attempt at a completed task creates a new related task. Multiple draws create separate tasks, never multiple current assignees.

## Visual language

Near-black/navy surfaces, blurred purple/cyan/teal glow, sparse particle mesh. Mint active rings, amber pending acceptance, muted green completed; rejected/failed/cancelled use distinct labels and icons. Compact circular nodes, short labels and thin curved connections. Event flashes distinguish branching, reports, submission, acceptance and reopening. Active rings are state indicators, not measured progress. Offscreen/hidden tabs stop animation; reduced-motion preserves all facts.

Completed nodes retain slow orbital motion and relationships keep a directional sheen. These are visual traces of recorded work; relation flow does not imply causal or dependency order. The explicitly labelled 历史流转 player follows public event order, not layout, timestamps or current status. Reduced-motion starts paused and keeps numbered steps and text available.

Current state is labelled 当前; a distinct 回放 label identifies the historical actor, action and 领取前 / 第 N 轮承接. Playback follows its node by default. Manual pan or zoom stops camera following while playback continues; 恢复跟随 restores it. Previous/next steps, speed, replay and expandable current-step details remain compact. Modal dialogs and expanded playback details suspend automatic progression and camera movement while retaining play intent. Hidden/offscreen views and editing also prevent automatic camera movement. Explicit replay never takes DOM focus.

## Components

`web/graph.js` and `graph.css`: TaskGraph, responsive group layout, motion, following camera and tooltip. `web/app.js` and `ui.css`: shell, real display scopes, dialogs, forms and event strip. `web/journeys.js`: relation-based groups and bounded neighborhoods. `web/api.js`: authenticated HTTP and durable tab-local pending intents. `web/model.js`: task projection and command construction. Tokens belong to these scoped styles. No runtime third-party frontend framework or external assets.

`web/history.js` owns independent history cursors and bounded projections. The global feed retains up to 1200 core events; a separate TaskRef-scoped feed retains up to 5000 for single-task replay. Single-task replay does not merely filter the global tail. Drain a fixed event batch before publishing its historical sequence; ordinary snapshot refreshes never advance these cursors. Retention, loading, failure and partial history are explicitly labelled; truncation means recent history, not complete history. Replayed events remain separate from live feedback and never issue commands.

## Accessibility

Native task buttons, meaningful labels, visible keyboard focus, Escape-close dialogs, focus restoration, usable non-hover details and reduced-motion. Contrast and screen-reader labels must remain legible; formal WCAG certification is not claimed.

## Responsive behavior

Overview density adapts to available canvas height as well as width: 12 desktop / 4 mobile are upper bounds, with fewer groups on short windows. Short mobile viewports use compact playback controls and legible node labels. Camera positioning measures the node's asymmetric visual extent, including its history phase, against the actual playback bar rather than assuming a minimum free height.

Desktop overview uses three or four columns; mobile uses two. Overview fitting preserves readable text instead of shrinking all groups into one screen. Task and group titles wrap to two lines with complete content in details/tooltips. Narrow screens retain minimal navigation and full-width dialogs. Task chains support pan, zoom and fit; following positions historical nodes above the player. Operation chips scroll horizontally without shrinking to numbers. Complete actor/action/round details remain keyboard- and touch-accessible. No hover-only essential action.

## Interaction states

Explicit disconnected, loading, empty, stale/offline, conflict and unknown-write states. Keep last snapshot on a failed refresh. Persist the exact pending request before sending; retry the same ID on unknown outcome. Batch draws show individual commits and partial failure, never claim atomicity. Historical replay never overwrites current facts. Network recovery refreshes a complete snapshot.

## Content voice

Chinese product labels; technical identifiers in secondary detail. Use 待领取 / 已领取 / 执行中 / 待验收 / 已完成 / 已拒绝 / 执行失败 / 已取消. No “成功” label for submission or agent process exit.

## Implementation constraints

Go embeds native ES modules and CSS on the same origin. Existing binding unchanged except an authenticated host identity endpoint needed by the browser. No CORS bridge, token in URL, automatic launcher or direct database access. Credentials are entered locally and kept only in this browser tab; never included in public events. Browser tests use isolated services and private credentials. Test current revision, issuer-only decisions, lost responses, derived task relations and event cursor recovery.

## Open questions

- [ ] Product: long-term volumes beyond the current 1000-object snapshot limit; overview pagination and local neighborhoods do not provide server-side pagination or remove this limit.
- [ ] Product: credential issuance remains out-of-band; participant registration alone does not create a login.
