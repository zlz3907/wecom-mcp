# 数据库发现候选验证记录

汇总日期：2026-09-23 06:45 UTC。这份记录只证明本地候选，不能用作生产发布或尖品客可用性证明。

## 源码及制品

- MCP 源码提交：`003e466ce9544e19f93eef6fb33e0948a391399e`。
- MCP 源码 tree：`826ff0565d3d73566081ac818625c43a5fec1e2a`。
- 分支：`codex/gnas-fleet-discovery`，基线 `4cfd3a3`；当前报告为后续纯文档提交，不改变已测源码。
- GNAS 配套提交：`0a1658c9660cf5d23f9d640624ece7003b00be36`，分支 `codex/mcp-binding-discovery`，独立工作树干净。
- MCP Linux/amd64 构建：`wecom-mcp-team-003e466-linux-amd64`，Go 1.25.0，CGO_ENABLED=0。
- 二进制 SHA-256：`963bb63165e2f1847c7400f9446822fd19efb34df37dc100a76e7ae821b57e78`。
- 制品目录：`/Users/zhonglizhi/.codex/release-candidates/wecom-gnas-discovery-20260923/`。含 MCP 二进制、双仓补丁、GNAS gate/race 日志、校验和及 manifest。
- 源码身份通过干净 Git tree 与外部 manifest 记录；该 Go buildinfo 没有 VCS revision 字段，不据此推断生产源码版本。
- GNAS 未生成生产可部署二进制：其正式候选构建依赖受保护配置 overlay，本轮禁止读取明文配置。已完成非敏感测试 overlay 下编译；正式候选须另批后按 GNAS release-controller 流程生成。

## 实际执行

| 验证 | 结果与边界 |
| --- | --- |
| MCP 根模块 `go test ./...`、`go test -race ./...`、`go vet ./...`、`go build ./...` | 全部退出 0；根模块 race/vet 由 Verifier 独立执行 |
| MCP teamserver `go test ./...`、`go test -race ./...`、`go vet ./...`、`go build ./...` | 全部退出 0 |
| 摘要修复及最后配置约束之后 `go test ./internal/team ./cmd/wecom-mcp-team -count=1` | 退出 0：3.719s / 3.708s |
| 同上 `-race` | 退出 0：7.955s / 6.891s；随后 vet 退出 0 |
| 真实 CLI help 与 9 类非法参数 | Verifier 核验 help；全部非法组合退出 2 |
| `GOOS=linux GOARCH=amd64 go build -buildvcs=true -trimpath ... ./cmd/wecom-mcp-team` | 退出 0；仅交叉编译，未在 Linux 启动进程 |
| GNAS `scripts/verify-production-gate.sh` | 06:42:59 UTC，`PRODUCTION_GATE_OK component=gnas-service-gin`；含 gofmt、模块校验、全模块 test、vet、build；service 测试 34.020s |
| GNAS service 专项 race（完整命令见 GNAS 配套文档） | 06:42:50 UTC，退出 0，service 6.430s；日志保留在制品包 |
| 两仓 `git diff --check` | 通过 |
| 独立质量结论 | Verifier PASS；独立 Reviewer 发现跨仓 Source 代际窗口，修复后针对双仓再次测试通过；最终 MCP `003e466` 与 GNAS Source/browser 增量复核 PASS，独立目标测试 MCP 2.047s / GNAS 2.273s |

上述日志名的 production gate 是本地工程门禁名称，不表示执行过生产操作。

## 关键回归场景

- 无逐租户 JSON 派生内存实例；固定 Source/Registry、真实已有实例名称、状态隔离；删除和完整空集合后恢复。
- Registry 与 Z-S00 完整分页；重复 active/字段、晚页重复、禁用、缺失、跨 Key、摘要或九角色不完整均拒绝；不写表或本地租户文件。
- Store 输入/输出深拷贝，初始化和 Registry bootstrap 不可执行；发现模式不列出写工具。
- 真实 HTTP resource handler 的 200 health/readiness、401 challenge、对应 metadata、未知 Host 421、伪造 X-Forwarded-Host 无效。
- 新租户构建失败不发布部分路由；整组 503；后续恢复；并发请求、配置变更排空、取消排空、移除后重新加入。
- Service JWT 每次 introspection，无共享 Basic fallback、无缓存、禁止重定向；错误 tenant/issuer/audience/摘要、失效 token、控制面失败均拒绝。
- 两仓共享同一 Binding JSON 摘要测试向量；新 Source token 不能进入旧 MCP snapshot。
- GNAS 实时 Source 非敏感元数据 guard、企业代际 namespace、动态客户端与 Source 凭据引用迁移、删除和权限撤销的失败关闭。

## 未执行及发布门禁

未执行生产数据库查询/写入、明文配置读取、Nginx 修改、生产发布/重启、Linux 运行验收、真实双服务及企业微信客户端联调、Owner 产品验收。尖品客数据库记录存在来自移交，未在本任务确认其 app 可见性、完整 Z-S00 或现有 OAuth 客户端策略。

候选当前仅查询。共享生产若有必须保持的写入工具，不能直接切换。正式发布和一次性开关启用需单独批准，详见 [发布与回滚方案](GNAS-FLEET-DISCOVERY.md)。
