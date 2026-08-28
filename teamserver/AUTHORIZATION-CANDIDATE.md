# MCP 用户授权候选合同

状态：隔离分支候选；等待 GNAS 稳定 SHA 和独立质量证据后再集成。不得据此合并、Release、部署或保存 WorkBuddy Connector。

## MCP 内部稳定边界

MCP 只依赖以下归一化查询和结果，不访问 Mongo，也不理解 `mcp_user_authorizations` 的表结构、索引或迁移：

```text
query: tenant + userid + resource
decision: schema_version + tenant + userid + active
          + effective_scopes[] + effective_tools[]
          + policy_version + evaluated_at
```

- `userid` 必须来自已验证 OAuth claim 映射，非空、唯一、大小写敏感；不得使用姓名、手机号、openid、external_userid 或客户端参数替代。
- `active=false`、不匹配的 tenant/userid、缺 scope、未知工具、空 policy_version、超时或结构漂移均拒绝。
- `effective_tools=["*"]` 只表示当前 MCP 二进制的公开工具集合；不得展开为 GNAS API、管理接口、未来版本新增工具或跳过静态角色/企业微信业务权限。
- 缓存键精确包含 tenant/userid/resource；TTL 最大 60 秒；刷新失败不使用过期授权。

## 待 GNAS 稳定 SHA 确认的适配项

以下字段只存在于单一适配层，不得分散到工具执行代码：

1. ResolveAuthorizationV1 的最终 HTTPS 路径。
2. GNAS 受控服务鉴权和请求签名方式。
3. HTTP 与业务错误码映射。
4. 最终 JSON envelope 或字段命名。
5. policy_version、evaluated_at 的最终格式与缓存失效信号。

当前 fixture 位于 `internal/team/testdata/resolve_authorization_v1.json`，仅用于候选契约测试。GNAS 稳定合同不一致时修改 HTTP 适配层和 fixture，不修改 MCP 工具授权语义。

## Feature flag 与回滚

- 默认：`TEAM_MCP_USER_AUTHZ_ENABLED=false`，保留 v0.2.17 静态 OIDC 角色路径。
- 候选启用前必须同时配置 resolver URL、固定 tenant/resource、企业微信 userid claim、TTL 和超时，并注入已批准的 GNAS 请求签名器。
- 未注入签名器或 resolver 时，即使 feature flag=true 也拒绝启动。
- 回滚只关闭 feature flag、重启候选服务并回读 OAuth、tools/list 和只读 tools/call；不修改 GNAS 数据、企业微信资产、实例配置或 Schema。

## 质量证据要求

- 正例：显式工具、`*` 展开、角色交集、tools/list 和 tools/call。
- 负例：active=false、缺 scope、未知工具、userid 缺失、结构漂移、非 200、超时、过期缓存刷新失败。
- 审计：只有主体 HMAC、授权结果、policy_version、请求 ID、工具和结果；无 userid、Token、Secret、参数或响应原文。
- 集成门禁：GNAS 稳定 commit/main 状态、接口合同、迁移/回滚、Verifier/Reviewer 双 PASS；随后 MCP 独立 Verifier/Architecture Reviewer 双 PASS。
