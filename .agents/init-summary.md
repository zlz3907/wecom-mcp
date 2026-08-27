# 治理初始化摘要

## 初始化范围

- 新增项目级 `AGENTS.md`，声明 wecom-mcp-v2 的增量约束和验证入口。
- 新增 `.agents/skills/README.md`，只链接权威技能，不复制核心流程。
- 保留工作区 AI Constitution 与两份 Zoop 工作流为唯一权威来源。

## 项目证据

- Go 模块：`go.mod`
- MCP 服务入口：`cmd/wecom-mcp-v2/`
- 固定实例配置示例：`config/zoop_wecom_zhycit.json.example`
- 现有验证：`go test ./...`、`go test -race ./...`、`go vet ./...`
- 发布与安装验证脚本：`scripts/`

## 安全边界

- 初始化研发只使用 fake requester、本地临时目录或明确授权的受控沙箱。
- 不在本次治理初始化中执行真实企业微信初始化、删除、部署或 WorkBuddy 最终验收。
- 不写入凭据、生产数据或租户专属资产标识。

## 初始化证据

- Owner 于 2026-08-27 明确授权最小项目治理初始化并继续在隔离 worktree 实现 initializer。
- 初始化目标分支：`codex/wecom-instance-initialize`。
- 初始化基线：`origin/main@21e8a3c96a46c172a06069987cd42977037c2c2c`。
- 用户现有根目录、分支和其他 worktree 未清理、未删除。
