# Actor and authorization model

## Subjects

- The verified Enterprise WeCom member is the human initiator. It answers who requested, confirmed, decided, or accepted the work.
- The server-configured Z-S09 AI subject is the WorkBuddy AI executor. It answers which stable AI employee performed the governed operation.
- The connector API key and the Enterprise WeCom self-built application are service identities. Never store either as a Zoop business subject.
- Register one Z-S09 row per stable AI employee or materially different permission boundary, not per physical desktop, conversation, model selection, or process restart.

The fixed gmzoop WorkBuddy subject is expected to be an enabled `AI 执行主体` with platform `Tencent WorkBuddy`. Its initial policy is `AI 辅助`, `不得自主`, and `人工触发`; only an explicit Owner decision may widen those values.

## Field attribution

The remote MCP enforces these defaults for new records:

- Z-S01 `需求提出主体`: verified human initiator.
- Z-S02 `决策主体`: verified human initiator.
- Z-S04 `操作主体`: configured WorkBuddy AI execution subject.
- Z-S05 `调度主体`: configured WorkBuddy AI execution subject.
- Z-S06 `发起主体`: verified human initiator; `执行主体`: configured WorkBuddy AI execution subject.

Do not override those fields. A conflicting value must fail.

Z-S03 is explicit because the responsible subject and actual executor may differ. Set `任务责任主体` to the accountable person or AI subject and `任务执行主体` only to the subjects that will perform the task. Do not automatically assign every task to the current WorkBuddy instance.

## Authorization gates

- User wording that authorizes requirement registration authorizes only the minimum Z-S01 write and its readback/audit, not automatic planning or implementation.
- `自动规划授权` permits evaluation and task decomposition only after the requirement baseline is confirmed. It does not authorize code changes, deployment, production data, payment, external notification, scheduling, or Owner acceptance.
- A task's `执行模式` and risk must stay within the Z-S09 subject's configured maximum autonomy and the current user instruction. When they conflict, choose the narrower boundary or stop for a decision.
- Human decisions belong in Z-S02. Never synthesize `已确认`, `已拒绝`, risk release, or an Owner acceptance result from silence, tool success, or task completion.
- Schema migration requires its dedicated preview, explicit admin authorization, local admin identity, and online readback. A Skill must never construct a raw structural mutation as a shortcut.

## Identity failure

If the user changes, requests rebind, or disputes attribution, keep the old binding active while starting rebind with `current_binding_id`. Use the new identity only after the application-message code is confirmed. If a name, userid, personnel row, or AI execution row is not unique, stop without writing.
