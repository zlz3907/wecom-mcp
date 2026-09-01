# 企业微信主体绑定边界

WorkBuddy 企业自定义连接器的 API Key 是统一连接器服务身份，不能标识某次 MCP 调用属于哪位成员，也不能直接写入 Zoop 的业务主体字段。当前 gmzoop 采用企业微信自建应用消息验证码完成业务主体绑定，不依赖网页授权或 OIDC：

1. 首次使用 operator/admin 工具时，WorkBuddy 询问用户的企业微信通讯录完整姓名。
2. `wecom_identity_binding_start` 必须唯一匹配一个启用的通讯录成员，以及 Z-S09 中唯一绑定该 `userid` 的主体记录；任一不唯一都失败关闭。
3. 自建应用向该 `userid` 单发 6 位验证码。服务端只保存 HMAC 摘要，不保存、记录或返回验证码原文。
4. `wecom_identity_binding_confirm` 成功后启用永久绑定句柄。验证码一次性、最多输错 5 次；绑定不设过期。换绑必须持有当前句柄，新验证码确认前旧身份继续有效。
5. 团队 HTTP 入口的 operator/admin 工具每次调用都验证句柄；记录新增时将已验证的 Z-S09 主体自动写入规定的发起/操作主体字段，不能由调用参数冒充。

这一机制用于业务归属和审计，不等于逐用户访问授权：共享 Connector API Key 仍决定连接器角色。若未来需要通过 `mcp_user_authorizations` 对每个成员分别授予工具权限，仍需 WorkBuddy MCP OAuth 2.1 或可信网关向上游传递可验证的稳定成员身份。
