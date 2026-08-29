# MCP 用户授权候选合同

状态：隔离分支适配候选；绑定 GNAS main `f7a2466c4a60674db2230e71af945b1685843020` 和 14/14 非生产 HTTPS smoke。不得据此自动合并、Release、部署或保存 WorkBuddy Connector。

## MCP 内部稳定边界

MCP 只依赖以下归一化查询和结果，不访问 Mongo，也不理解 `mcp_user_authorizations` 的表结构、索引或迁移：

```text
query: tenant + userid + resource + principal_assertion
decision: schema_version + tenant + userid + active
          + effective_scopes[] + effective_tools[]
          + policy_version + evaluated_at
```

- `userid` 必须来自已验证 OAuth claim 映射，非空、唯一、大小写敏感；不得使用姓名、手机号、openid、external_userid 或客户端参数替代。
- `active=false`、不匹配的 tenant/userid、缺 scope、未知工具、空 policy_version、超时或结构漂移均拒绝。
- `effective_tools=["*"]` 只表示当前 MCP 二进制的公开工具集合；不得展开为 GNAS API、管理接口、未来版本新增工具或跳过静态角色/企业微信业务权限。
- 正向与拒绝结果均不缓存；每次 tools/list 与 tools/call 独立实时解析，固定两秒超时，撤权在下一请求生效。

## 已冻结的 GNAS 适配合同

适配层固定使用 `POST /gnas/service/resolveAuthorizationV1`、短期 Service JWT、`X-Auth-Type: service_jwt`、严格 `code + data` envelope 和返回 `resource`。`principal_assertion` 只能从已验证 OAuth claim 转交，MCP 不自行签名。40101/40301/40501/40001/50301 必须与 HTTP 状态精确匹配，否则视为合同漂移并失败关闭。fixture 位于 `internal/team/testdata/resolve_authorization_v1.json`。

## Feature flag 与回滚

- 默认：`TEAM_MCP_USER_AUTHZ_ENABLED=false`，保留 v0.2.17 静态 OIDC 角色路径。
- 候选启用前必须同时配置同源 token/resolver URL、固定 tenant/resource、专用 GNAS caller app 凭据、企业微信 userid claim 和 GNAS principal assertion claim。
- 任一配置或 resolver 缺失时，即使 feature flag=true 也拒绝启动。
- 回滚只关闭 feature flag、重启候选服务并回读 OAuth、tools/list 和只读 tools/call；不修改 GNAS 数据、企业微信资产、实例配置或 Schema。

## 质量证据要求

- 正例：显式工具、`*` 展开、角色交集、tools/list 和 tools/call。
- 负例：active=false、缺 scope、未知工具、userid/assertion 缺失、结构漂移、错误码不匹配、非 200、两秒超时、撤权下一请求。
- 审计：只有主体 HMAC、授权结果、policy_version、请求 ID、工具和结果；无 userid、Token、Secret、参数或响应原文。
- 集成门禁：GNAS 稳定 commit/main 状态、接口合同、迁移/回滚、Verifier/Reviewer 双 PASS；随后 MCP 独立 Verifier/Architecture Reviewer 双 PASS。
