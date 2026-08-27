# wecom-mcp-v2 项目治理规则

## 权威来源

本项目复用工作区治理核心，不复制或分叉其内容：

- [`AI_CONTEXT.md`](../ai-constitution/AI_CONTEXT.md)
- [`rules/`](../ai-constitution/rules/)
- [`企业微信智能文档协同 AI 研发流程`](../ai-constitution/wecom-ai-project-management/docs/workflows/ai-rd-collaboration-flow.md)
- [`看板状态流转`](../ai-constitution/wecom-ai-project-management/docs/workflows/kanban-state-flow.md)

发生冲突时，以工作区治理核心和上述两份 Zoop 工作流为准。

## 项目范围

本仓库提供固定租户、固定实例的企业微信智能表格 MCP 服务。实例配置、Schema 镜像、企业微信文档和 Zoop 记录属于受控资产。

## 强制约束

- 不得在源码、测试、日志、提交或示例配置中写入真实凭据、租户专属文档 ID、子表 ID、字段 ID 或业务记录。
- 默认使用 fake requester、本地临时目录和 stdio 合约测试；未获得测试实例或受控沙箱授权时，不得执行真实企业微信写入、初始化或删除。
- 初始化必须幂等。远程创建结果不确定时保留 journal/sentinel，恢复同一资产，禁止盲目重试创建。
- 导入已有 `docid` 默认只读核验，不清理已有字段或记录。
- 仅可清理本次初始化创建且仍严格匹配平台默认模板的内容。主字段优先复用并改造成标准主字段；非空、被修改、来源不确定或接口能力未验证时必须失败关闭。
- 本地配置提交前必须创建备份，并使用同文件系统临时文件、同步和原子替换。失败时保留原配置和恢复证据。
- WorkBuddy 验收必须区分 `installed`、`configured`、`loaded`、`registry_verified`、`nine_tables_verified`、`schema_synced`、`tools_call_verified`、Owner 验收和生产部署。
- 修改应限定在隔离 worktree，不得清理或删除用户现有根目录、分支或其他 worktree。

## 验证命令

最低验证入口来自本仓库 Go 模块与现有脚本：

```sh
go test ./...
go test -race ./...
go vet ./...
```

涉及 stdio、安装器或跨平台原子替换时，还应执行对应测试和构建；不能在当前平台验证的项目必须明确记录为未验证。

## Skills 入口

项目技能入口见 [`.agents/skills/README.md`](.agents/skills/README.md)。Skills 只提供执行方法，不构成授权边界，也不得复制 Zoop 核心流程。
