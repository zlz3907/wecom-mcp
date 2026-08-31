# ResolveAuthorizationV1 MCP 适配证据

状态：隔离候选；未合并 main、未 Release、未部署、未启动 gmzoop、未保存 WorkBuddy Connector。

## 固定输入

- MCP 冻结父候选：`cac08c741a9cc2ae328c89bbb4c90f82b932e518`
- GNAS 稳定 main：`f7a2466c4a60674db2230e71af945b1685843020`
- GNAS linux/amd64 smoke 制品 SHA-256：`e58e60957b9895dd66cd6301cf02e4d746484f70fc640bfe69e029a9e5344a76`
- GNAS 脱敏 smoke 结果 SHA-256：`3788216a99accfc17daca7c5452d8e760800cfad5d2c747821e204eaf1c7972f`
- GNAS smoke：14/14；合同无漂移；撤权后的下一请求即时拒绝；临时服务、证书、密钥、目录、进程和端口均已清理。

## 本候选变更

- OAuth 验证结果必须同时映射唯一企业微信 `userid` 与 GNAS 签名 `principal_assertion`；缺失或歧义失败关闭。
- 每次授权解析通过 JSON POST 从固定 `getJwtToken` 入口取得短期 Service JWT，不使用 URL 凭据，不跟随重定向。
- token 与 resolver 必须为同一 HTTPS origin，并使用固定稳定路径。
- ResolveAuthorizationV1 请求包含 `tenant`、`userid`、`resource`、`principal_assertion`，并发送 `Authorization: Bearer` 与 `X-Auth-Type: service_jwt`。
- 成功响应严格解析 `code=200 + data`，并精确校验 `schema_version`、tenant、userid、resource、active、tools、scopes、policy_version、evaluated_at。
- 40101、40301、40501、40001、50301 必须与固定 HTTP 状态匹配；结构、类型、content-type、大小或错误码漂移全部失败关闭。
- 正向与拒绝授权结果均不缓存；每次 `tools/list` 和 `tools/call` 独立实时解析，固定总截止时间两秒；`tools/call` 不复用此前 list 决策。
- `tools=["*"]` 仍只展开当前二进制公开工具集合，并与静态角色权限取交集。

## 受影响质量检查

执行目录：`teamserver/`。

- `go mod verify`：PASS
- `go test -count=1 ./...`：PASS
- `go test -race -count=1 ./...`：PASS
- `go vet ./...`：PASS
- `go build ./cmd/wecom-mcp-team`：PASS
- `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./cmd/wecom-mcp-team`：PASS
- linux/amd64 候选制品 SHA-256：`d5b16d27a4ab15a1c2d5fe45d2daf8c8b67066e01982a2156952644c6f02fd5f`
- `git diff --check`：PASS

冻结候选已通过且输入未变化的 stdio 主模块检查未重复执行；本轮变更仅在 `teamserver`。

## 未证明与后续门禁

- 未执行真实 OAuth、真实 GNAS Service app 凭据、真实 gmzoop 运行链或 WorkBuddy Connector。
- 未合并 main、未创建 PR、未 Release、未升级服务器、未启动服务。
- 独立 Verifier/Reviewer 结论必须绑定本候选代码 SHA 与本文件 SHA-256；通过后才可请求后续合并门禁。
