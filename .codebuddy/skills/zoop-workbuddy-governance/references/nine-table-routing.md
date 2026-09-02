# Nine-table routing

Use the smallest set of tables that represents the requested fact.

| Role | Purpose | Write when |
| --- | --- | --- |
| Z-S07 | Project boundary | A project is explicitly created or its lifecycle changes. Requirements must resolve to one existing project. |
| Z-S01 | Requirement baseline and business acceptance | A requirement is registered, clarified, confirmed, progressed, cancelled, or accepted. Search for duplicates first. |
| Z-S02 | Human decision or exception | A named choice, risk release, architecture/business tradeoff, or escalation needs an accountable human conclusion. |
| Z-S03 | Executable work and independent quality stages | Confirmed work is decomposed, assigned, executed, verified, reviewed, blocked, reworked, cancelled, or completed. |
| Z-S04 | Material governance audit | A governed write, decision, escalation, recovery, or final gate needs durable evidence. Do not log secrets or full sensitive payloads. |
| Z-S05 | Recurring or externally scheduled work | The user explicitly requests a schedule or an already-authorized governance heartbeat is changed. Do not infer scheduling from “continue”. |
| Z-S06 | AI/human work session | A durable execution, verification, review, coordination, handoff, or waiting session must be linked to its project and subjects. |
| Z-S08 | Schema compatibility contract | Read to determine whether the current nine-table model is compatible. Do not treat it as a writable business log. |
| Z-S09 | Human, AI, and system subjects | A stable actor or permission boundary is registered, updated, paused, or disabled. Do not register conversations or physical terminals. |

## Core relations

- Resolve Z-S07 before creating Z-S01.
- Link every Z-S03 task to one real Z-S01 requirement.
- Link Z-S02 decisions, Z-S04 audits, and Z-S06 sessions to their actual requirement/project context when the Schema provides that relation.
- Use Z-S09 references for responsibility and execution. A text name is not a subject binding.
- Do not create duplicate requirements, tasks, decisions, schedules, sessions, or subjects when their stable key already exists.

## Read completeness

Use `wecom_record_query` with compact results and bounded pagination for discovery. Continue from `next_offset` while `has_more=true`. A complete whole-table conclusion requires the accumulated returned records to equal `record_count`. For a known record, query its exact `record_id` and read back only the fields needed for the conclusion.

## Write shape

Use field titles in `records[].values`; never send tenant routing, document IDs, sheet IDs, credentials, or field IDs. Use registered option names exactly. Cross-table references use `[{
"record_id":"..."
}]`. Date/time values use decimal millisecond timestamp strings. Do not write attachment, formula, lookup, or system-generated fields.
