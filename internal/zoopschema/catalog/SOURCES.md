# zoop-v1 catalog build evidence

This file is build-time audit evidence only. It is not embedded and the MCP runtime never reads these paths.

- Field contract source: `/Users/zhonglizhi/.codex/worktrees/83c6/ai-constitution/governance/zoop-new-eight-schema-dictionary-v1.json` (Owner-authorized online mirror captured at `2026-08-11T16:53:56Z`).
- Canonical role names and counts: `/Users/zhonglizhi/workspace/ai-constitution/wecom-ai-project-management/docs/integrations/zoop-nine-tables-onboarding.md`.
- Logical reference topology: `/Users/zhonglizhi/workspace/ai-constitution/wecom-ai-project-management/docs/integrations/zoop-nine-tables-relationship-model.md`, especially the relationship table at lines 66-80 in the reviewed revision.

Generation command used for this candidate:

```sh
go run ./cmd/generate-zoop-catalog \
  -input /protected/path/to/owner-authorized/zoop-nine-tables-schema-dictionary-v1.json \
  -output internal/zoopschema/catalog/zoop-v1.json
```

The generator translates source-only tenant sheet IDs into logical `{role, field_title, multiple}` references and removes every document, sheet, field and option ID from the output. Tests reject known source IDs and exact-title drift. `FIELD_TYPE_FORMULA` is retained as observed contract but marked `unsupported_for_create` until an upstream create contract is proved.
