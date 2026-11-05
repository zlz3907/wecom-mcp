# teamserver capability catalog (MCP -> GNAS)

本能力目录是默认关闭的只读目录上报。它描述当前 `teamserver` 实例由
`ToolDefinitionsForPlugins(cfg.Plugins, ...)` 生成的插件工具集合，不读取 legacy
`tools` 静态列表，也不把目录上报当作授权。`tools/call` 仍由 OAuth 2.1、实例
角色和每次 `ResolveAuthorizationV1`/token policy 校验决定。

## 开关和环境变量

启用前需要 GNAS 已登记与当前进程完全匹配的 Binding、Source、资源和 Service
JWT 身份。默认不开启，不改变现有启动或请求路径：

```text
TEAM_MCP_CAPABILITY_CATALOG_PUSH_ENABLED=false   # 默认值
GNAS_CAPABILITY_CATALOG_ENDPOINT=https://<gnas>/gnas/service/reportMCPCapabilityCatalogV1
TEAM_MCP_CAPABILITY_BINDING_ID=<仅单实例显式配置时使用>
TEAM_MCP_CAPABILITY_SOURCE=<仅单实例显式配置时使用>
TEAM_MCP_CAPABILITY_INSTANCE_ID=<GNAS Binding 配置了 mcp_instance_id 时使用>
```

`GNAS_CAPABILITY_CATALOG_ENDPOINT` 未设置时从 `GNAS_BASE_URL` 派生固定路径。
Fleet/discovery 模式从 GNAS 返回的 Binding 注入 binding/source/resource/instance
身份，不接受进程外覆盖。启用后必须使用 `OAuth21ServiceJWT=true` 的既有
`/gnas/service/getJwtToken` 方向；缺少身份、scope、Binding 或 digest 时启动/上报
失败关闭。请求客户端禁止 HTTP 重定向；生产端点必须 HTTPS（仅 loopback 测试可用
HTTP）。

## 上报合同

```text
POST /gnas/service/reportMCPCapabilityCatalogV1
Authorization: Bearer <existing short-lived Service JWT>
X-Auth-Type: service_jwt
Content-Type: application/json
```

请求字段如下，服务端必须按当前 authenticated app 的 Binding 全字段匹配：

```json
{
  "schema_version": "mcp-capability-catalog.v1",
  "mcp_instance_id": "<optional exact configured id>",
  "binding_id": "<exact BindingID>",
  "source": "<exact managed Source>",
  "public_resource": "<exact Binding.PublicResource>",
  "authorization_resource": "<exact Binding.AuthorizationResource>",
  "catalog_version": "<teamserver version + runtime digest>",
  "catalog_digest": "sha256:<lowercase hex>",
  "tools": [{"name": "<tool>", "description": "<description>"}],
  "scopes": ["<real OAuth/plugin scope>"],
  "request_id": "<fresh random id>",
  "purpose": "capability_catalog_report"
}
```

`tools` 来自实际实例的插件目录；`scopes` 只来自 `TEAM_MCP_REQUIRED_SCOPES`
等已存在 OAuth/插件授权合同。`APIWhitelist` 是上游 API 能力限制，不是 OAuth
scope，不能映射成 `mcp.group.*`。没有真实 scope 时上报失败关闭。空值、重复值、
通配符、跨 Binding/tenant/resource/instance 的值均拒绝。

`catalog_digest` 与 GNAS `mcpAdminCapabilityDigest` 完全一致：以
schema/instance/binding/source/public resource/authorization resource/version 及
按名称排序的工具（含 description）和 scope 规范 JSON 做 SHA-256；不包含
`request_id`、`purpose` 或 Service JWT。响应必须是 `application/json`，且包含：

```json
{
  "accepted": true,
  "policy_version": "<same catalog_version>",
  "catalog_digest": "<same digest>",
  "schema_version": 1,
  "mcp_instance_id": "<GNAS verified id or legacy binding fallback>"
}
```

旧 `request_id` 重放、HTTP 非 200、digest/version 不一致、非 JSON、超时或重定向
均失败关闭。GNAS 侧负责保存/过期/重放控制和管理端读取；该目录本身不授予任何
员工、AI 主体、任务或项目权限。

## GNAS 侧配套

GNAS 需启用其独立、默认关闭的 `reportMCPCapabilityCatalogV1` 处理器，并通过
现有 Service JWT 的 app_id 读取已验证 `mcp_bindings` 与 managed Source。GNAS 管理
页面只读取 MCP 上报的目录快照；它不得回调 MCP，也不得以 `tools/list` 替代目录。
实际执行仍需 OAuth access token、正确 audience/resource/tenant 和现有
`ResolveAuthorizationV1`/introspection 决策。
