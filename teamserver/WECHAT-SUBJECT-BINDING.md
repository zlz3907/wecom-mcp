# 企业微信主体绑定边界

## OAuth 员工衔接修复候选（REQ-WECOM-TEAM-MCP-SERVER-001）

2026-09-10：仅本地修复候选，未部署、未产品验收。以下规则适用于 OAuth 2.1 模式；下文的验证码流程继续适用于旧 Connector/OIDC 模式。

- 传输层验证 issuer、tenant、resource/audience、有效期和实时工具策略后，单独传递可信员工 userid；工具参数不能指定或覆盖该身份。
- 写工具每次仅从本实例固定 Registry 对应的 Z-S09 解析唯一启用人员主体，再复用原业务权限和审计。人员缺失、停用、重复或仅有 AI 主体均拒绝；不自动登记人员，不赋予全员管理员权限。
- OAuth 工具 schema 不再要求 identity_binding_id，也不公开验证码绑定工具。AI 执行主体仍来自实例配置，和员工发起人分开。
- 身份解析和实际业务调用之间的实例配置 digest 必须一致；配置变更时失败关闭，不能将旧实例人员用于新实例业务目标。
- fake 端到端测试通过新入口调用真实消息写工具，断言固定 source、token 员工业务审计和独立 AI 主体；缺人员、缺员工、伪造句柄不产生业务写请求。测试拦截所有 HTTP，无真实网络或业务资产写入。
- 上线前仍需独立验证/审查、受控部署批准及国脉实例真实员工验收。研发管理使用的中和圆创 Zoop 与交付实例严格分开。

## 旧连接器验证码流程

### 受管执行器传输选择

固定实例可显式设置 `GNAS_WECOM_TRANSPORT=managed_executor`，将已编译白名单内的
表格/通讯录等操作统一送入 GNAS `/gnas/service/wecomExecute`。外层固定 POST，
真实上游 method/path 由服务端校验；GET 不携带请求体。必须先部署支持已授权
WeCom Broker source 的 GNAS 修复；不能用此配置绕过服务身份的 source/method/path
授权。未设置或设置为 `legacy_proxy` 时保留原 `/api/<source>` 路径，已有消息/媒体
执行器路径保持不变。配置只改变传输，不改变固定实例、Registry、员工授权或目标文档。

本轮只增加代码和fake传输测试，不修改任何线上环境变量。

WorkBuddy 企业自定义连接器的 API Key 是统一连接器服务身份，不能标识某次 MCP 调用属于哪位成员，也不能直接写入 Zoop 的业务主体字段。当前 gmzoop 采用企业微信自建应用消息验证码完成业务主体绑定，不依赖网页授权或 OIDC：

1. 首次使用 operator/admin 工具时，WorkBuddy 询问用户的企业微信通讯录完整姓名。
2. `wecom_identity_binding_start` 必须唯一匹配一个启用的通讯录成员，以及 Z-S09 中唯一启用且绑定该 `userid` 的人员主体记录；同一 `userid` 下的 AI 执行主体不参与人员绑定，人员主体缺失或不唯一时失败关闭。
3. 自建应用向该 `userid` 单发 6 位验证码。服务端只保存 HMAC 摘要，不保存、记录或返回验证码原文。
4. `wecom_identity_binding_confirm` 成功后启用永久绑定句柄。验证码一次性、最多输错 5 次；绑定不设过期。换绑必须持有当前句柄，新验证码确认前旧身份继续有效。
5. 团队 HTTP 入口的 operator/admin 工具每次调用都验证句柄；记录新增时将已验证的 Z-S09 主体自动写入规定的发起/操作主体字段，不能由调用参数冒充。

这一机制用于业务归属和审计，不等于逐用户访问授权：共享 Connector API Key 仍决定连接器角色。若未来需要通过 `mcp_user_authorizations` 对每个成员分别授予工具权限，仍需 WorkBuddy MCP OAuth 2.1 或可信网关向上游传递可验证的稳定成员身份。
