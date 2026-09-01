---
name: zoop-workbuddy-governance
description: Govern projects, requirements, decisions, tasks, audits, schedules, sessions, and human or AI subjects in the gmzoop nine-table model through the fixed gmzoop MCP. Use when a WorkBuddy user asks to submit or update a requirement, plan or track work, make or request a decision, assign an AI employee, verify or review results, audit an action, schedule governed work, or inspect Zoop progress. Do not use for ordinary Enterprise WeCom office work unrelated to Zoop.
---

# Zoop WorkBuddy Governance

Use the fixed gmzoop MCP as the execution boundary and this Skill as the governance workflow. Never treat the shared WorkBuddy connector credential as a person, an AI employee, or an authorization decision.

## Start with the execution context

1. Confirm the current conversation has an active `identity_binding_id`. If not, ask for the exact Enterprise WeCom directory name, call `wecom_identity_binding_start`, ask for the six-digit application-message code, and call `wecom_identity_binding_confirm`.
2. Keep the returned binding handle only in tool-call context. Do not print it, write it into project files, or place it in Zoop records.
3. Call `wecom_identity_binding_status` when the current initiator or configured AI execution subject is uncertain. Require one personnel subject and one server-configured AI execution subject.
4. Stop before an operator or admin action if identity binding, the configured AI subject, the local Schema mirror, or the fixed gmzoop instance is unavailable.

Read [references/actor-and-authorization.md](references/actor-and-authorization.md) before creating a subject, assigning work, making a decision, or performing an operator/admin action.

## Route the request

- For project, requirement, task, decision, audit, schedule, session, Schema-contract, or subject routing, read [references/nine-table-routing.md](references/nine-table-routing.md).
- For requirement lifecycle, planning, execution, verification, review, recovery, handoff, or Owner acceptance, read [references/governance-workflow.md](references/governance-workflow.md).
- For an explicitly authorized Enterprise WeCom application message, read [references/app-messages.md](references/app-messages.md). Never infer notification authorization from a record write, task assignment, or the word "continue".
- For a simple read-only status request, query only the required tables and paginate until the returned record set is complete. State the scope before making a whole-table claim.

## Common invariants

- Treat online Enterprise WeCom records as the current business truth and the synchronized local Schema as the only allowed field contract. Never guess field IDs, option IDs, document IDs, sheet IDs, or unregistered values.
- Search for an existing record by stable business key, project, source, and normalized title before creating one. Update the same record when it already exists.
- Distinguish registration, confirmation, planning authorization, execution authorization, verification, review, and Owner acceptance. One stage never implies the next.
- Use `wecom_record_apply` for record mutations and immediately read back the same record. Report `applied_readback_pending` or `applied_progress_sync_pending` as pending recovery, not success, and never replay the original mutation blindly.
- Do not modify Schema, deploy software, change production configuration, send external messages, schedule recurring work, or approve a decision unless the user has authorized that action in the current scope. Application messages may target only one enabled member; never synthesize a broadcast, department, tag, or group target.
- A Skill guides behavior but is not a security boundary. Preserve all MCP identity, capability, allowlist, idempotency, and readback checks.

## Finish with evidence

Return the affected record ID, current state, initiator, execution subject when relevant, readback result, unresolved gate, and exactly one next action. Do not call work complete before the required independent verification, review, or Owner gate has actually passed.
