# gmzoop 远程 MCP 单 Prompt 配置

将下面这一句话交给当前客户端中具备 MCP 配置能力的 Agent：

```text
请为当前客户端配置 gmzoop 远程 MCP，并严格执行：https://raw.githubusercontent.com/zlz3907/wecom-mcp/main/GMZOOP_REMOTE_SETUP_PROMPT.md
```

以下是 Agent 必须执行的完整规范。固定 MCP 名称为 `gmzoop`，固定 Streamable HTTP 地址为 `https://mcp.jyiai.com/gmzoop/mcp`。

## 安全边界

1. 只配置远程 MCP，不下载或安装本地 `wecom-mcp-v2` 二进制，不执行实例初始化、Registry/Schema 变更或企业微信业务写入。
2. 不向用户索取、读取、回显、复制或验证 Connector API Key 的值。配置文件、Git、工作区、聊天和命令行参数中不得出现真实 Key。
3. 先确认当前宿主产品及其官方 MCP 配置入口。不得仅凭目录名、历史配置或相似产品猜测客户端。
4. 先写入不含密钥的固定 URL 配置，再把唯一人工步骤指向当前客户端的安全密钥入口。客户端没有可确认的密钥或环境变量入口时，保留已有配置并报告 `configured=no`，不得降级为明文写入。
5. `gmzoop` 是已经初始化的共享服务端实例。任何员工都不得再次运行 `wecom_instance_initialize` 的 apply、`wecom_instance_initialize_apply`、`wecom_registry_bootstrap` 或 Schema migration/sync。
6. 连接器 Key 只是服务凭据，不代表人员身份。每位员工首次执行 operator/admin 操作前，仍须以本人企业微信通讯录姓名完成验证码绑定；不得复用 gm 或其他员工的 `identity_binding_id`。

## 按客户端配置

### WorkBuddy 企业版

- 优先由企业管理员在“企业自定义连接器 / MCP 管理”中一次性创建并发布，普通员工不接触 Key。
- MCP Server URL：`https://mcp.jyiai.com/gmzoop/mcp`
- 认证方式：API Key
- Header Name：`Authorization`
- Header Value：由管理员在密钥输入框填写 `Bearer ` 加 Connector Key。
- 若界面提供 HTTP 类型，使用该版本界面生成的远程 HTTP 配置；不要覆盖 WorkBuddy 内部文件。
- 启用仓库中的 `.codebuddy/skills/zoop-workbuddy-governance` Skill。管理员已发布连接器时，员工只需启用连接器和 Skill，然后完成个人身份绑定。

### Codex Desktop、Codex CLI 或 Codex IDE 扩展

先运行不含密钥的官方配置命令：

```sh
codex mcp add gmzoop --url https://mcp.jyiai.com/gmzoop/mcp --bearer-token-env-var GMZOOP_MCP_API_KEY
```

该命令只保存环境变量名，不保存 Key。随后提示用户在启动 Codex 的受保护环境或组织批准的密钥管理入口中设置 `GMZOOP_MCP_API_KEY`，值只填 Connector Key 本身，不加 `Bearer `。不要代替用户读取该变量。重启 Codex 后再做只读验证。

### TRAE、CodeBuddy 或其他支持远程 MCP 的客户端

- 使用当前产品官方“添加 MCP / 工具”界面，传输类型选择 Streamable HTTP 或该产品等价的 HTTP 类型。
- 名称填 `gmzoop`，URL 填固定地址。
- 优先选择产品的 Secret、API Key 或环境变量引用功能；认证请求最终必须形成 `Authorization: Bearer <Connector Key>`。
- 只有产品明确支持带权限保护的用户级私密配置时，才可让用户在该产品界面填写 Key。不得在项目级、可提交或团队共享 JSON 中保存明文 Key。
- 无法从当前产品官方界面、内置帮助或已确认配置契约判断字段时，停止在 `configured=no`，并指出用户应打开的官方“添加远程 MCP”入口；不得套用 Codex、WorkBuddy 或其他客户端格式。

## Skill 与身份绑定

客户端支持 Skill 时，安装或启用仓库内的 `zoop-workbuddy-governance` Skill。Skill 可指导流程，但不是权限边界。

首次需要写表或发消息时：

1. 询问当前员工本人的企业微信通讯录完整姓名。
2. 调用 `wecom_identity_binding_start`。
3. 让员工只在工具参数中提交企业微信收到的 6 位验证码并调用 `wecom_identity_binding_confirm`。
4. 后续只使用该员工自己的永久绑定句柄；不要显示、写入项目或放进业务记录。

如该员工没有唯一启用的 Z-S09 人员主体，停止并提示联系 Zoop 管理员补齐人员主体，不得自动创建或猜测。

## 只读验收

配置后按层级验收，不能互相替代：

1. `configured`：客户端保存了固定 URL 和安全的 Key 引用或管理员连接器。
2. `loaded`：当前客户端完成 MCP `initialize` 和 `tools/list`。
3. `verified`：真实调用一次只读 `wecom_schema_status` 并成功返回。
4. `identity_bound`：当前员工完成本人验证码绑定；这不由只读验证自动推导。

验收期间禁止调用消息发送、记录写入、初始化 apply、Schema sync/migration 或任何恢复性写入。

最终输出：

```ini
client=workbuddy|codex|trae|codebuddy|generic|unknown
configured=yes|no
loaded=yes|no|unknown
verified=yes|no|unknown
identity_bound=yes|no|not_required
key_location=admin_connector|protected_environment|client_secret_store|unavailable
next_action=<仅一个具体动作，不含任何密钥>
```

