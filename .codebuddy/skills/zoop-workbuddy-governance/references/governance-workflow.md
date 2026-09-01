# Governance workflow

## Requirement intake

1. Resolve one existing Z-S07 project. If project ownership is genuinely unknown, ask only that question before creating a requirement.
2. Search Z-S01 by stable requirement number when available, then project, source, and normalized title. Update an existing requirement rather than creating a duplicate.
3. Capture the user's existing facts without asking them to repeat information: desired result, context/value, scope and non-scope, acceptance criteria, constraints, priority/risk when stated, and whether AI may evaluate and decompose after confirmation.
4. Use `待澄清` when a material baseline fact is missing, `待确认` when the baseline is complete but not explicitly confirmed, `已确认` only after named confirmation, and `草稿` only when the user asks for a draft.
5. Registration does not start implementation. Record the current gate and one next action.

## Planning and task creation

Plan only when Z-S01 is `已确认` and automatic planning is explicitly enabled, or when the Owner directly requests a bounded decomposition. Create the smallest independent Z-S03 tasks needed for checkable progress.

- `待拆解`: work exists but the executable boundary is not ready.
- `待执行`: inputs, owner, boundary, and acceptance are ready.
- `执行中`: one named executor is performing the current revision.
- `待验证`: execution evidence exists and awaits an independent quality role.
- `返工`: a verifier or reviewer returned a checkable correction.
- `阻塞`: progress requires an external dependency, human prerequisite, or Z-S02 decision.
- `已完成`: the task's required quality gates passed; this still does not imply Z-S01 Owner acceptance.
- `已取消`: the task is intentionally no longer required.

Set `任务责任主体`, `任务执行主体`, `任务类型`, `执行模式`, source revision, acceptance evidence, and blocker route from actual facts. Do not assign a generic AI task when the deliverable or quality role is unclear.

## Independent quality roles

Keep Executor, Verifier, Reviewer, and Owner conclusions distinct. A role may not approve its own output as an independent gate.

- Lower-risk bounded work needs at least one independent quality conclusion when the requirement asks for verification.
- Higher-risk work uses Executor, then independent Verifier, then independent Reviewer, followed by any required Owner gate.
- A pending Reviewer is not a business blocker by itself. Keep the task `待验证` until the exact candidate receives its review conclusion.
- Tool success, tests, deployment, or message delivery prove only their stated evidence layer.

## Recovery and escalation

For one stable uncertainty key, allow at most three bounded, evidence-producing recovery attempts. If uncertainty remains, create or reuse one Z-S02 human handoff containing the source evidence, accountable person, decision needed, exit condition, prohibited actions, and one recovery action. Stop automatic retries after escalation.

Never replay an uncertain mutation. First read the current online record, reservation state, and audit evidence. Reconcile only the missing derived state, such as requirement progress, when the dedicated recovery tool supports it.

## Completion and acceptance

Task completion can advance the requirement toward `待验收`; it cannot set `业务验收结果=通过`. Only the Owner's named conclusion may complete final acceptance. Report separately:

- what was registered or changed;
- what was executed;
- what was independently verified/reviewed;
- what remains unproven;
- the current human or Owner gate;
- exactly one next action.
