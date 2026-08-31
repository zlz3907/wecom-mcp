# 企业微信主体绑定边界

WorkBuddy 企业自定义连接器的 API Key 是统一连接器服务身份，不能标识某次 MCP 调用属于哪位成员。它只能保护 gmzoop 测试服务并限制为 reader，不能用于 Zoop 的业务主体归属或逐人 MCP 授权。

企业微信自建应用网页授权可以在用户完成企业微信授权后返回企业内唯一 `userid`。因此第二阶段应采用“首次绑定、后续显式选用”的业务流程：

1. 用户在企业微信授权页面完成自建应用的网页授权；回调端仅兑换一次性 code 并验证 `userid`，不把 code、CorpSecret 或 access token 回传给 WorkBuddy。
2. 服务端创建或回读 Z-S09 协作主体，将 `userid` 只以受保护的稳定绑定键关联；业务记录保存的是 Zoop 主体引用，不保存 token。
3. WorkBuddy 会话显式选择已绑定主体后，需求创建、任务分配等业务工具才可使用该主体。Connector API Key 仍只代表连接器，不代表该主体。

在没有 WorkBuddy 逐用户已验证请求身份的情况下，这一绑定只解决业务归属和审计提示，不得当作用户级访问授权。若要让 `mcp_user_authorizations` 按人强制执行，仍需 WorkBuddy MCP OAuth 2.1 加外部授权服务器，或由可信网关转发可验证的成员身份。

部署前必须由企业微信管理员配置自建应用的网页授权可信域名和服务器可信 IP；实际 OAuth 回调、CorpID、AgentId、Secret 仅通过受保护服务器配置注入。
