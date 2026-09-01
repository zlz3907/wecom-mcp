# wecom-mcp-v2

这是一个连接**企业微信智能表格**的 MCP 服务。

面向国脉爱特团队共享、服务器部署和 WorkBuddy 远程接入的 Streamable HTTP 版本位于 [teamserver/README.md](teamserver/README.md)。它使用独立 Go 模块和服务端 OIDC，不改变本地 stdio 安装方式。

安装后，Codex 或 TRAE 可以用自然语言查询 Zoop 的需求、任务等记录，也可以在明确指令下受控维护记录。它适合已经使用 Zoop 管理研发工作，希望直接在 AI 客户端里查看和更新企业微信智能表格的个人或团队。

## 非技术用户自动安装

现在用户只需把下面这句话粘贴给任何具备本机终端和本地 stdio MCP 管理能力的 Agent：

```text
请安装 wecom-mcp-v2，并严格执行：https://raw.githubusercontent.com/zlz3907/wecom-mcp/main/AGENT_INSTALL_PROMPT.md
```

完整安装规则只维护在 [AGENT_INSTALL_PROMPT.md](AGENT_INSTALL_PROMPT.md)，避免 README 与安装规范不一致。非技术用户不需要执行 PowerShell、双击 `.cmd`、选择 CPU 架构、修改白名单或手动清理 staging 目录。

Windows Agent 必须根据当前宿主明确暴露的产品身份区分 Codex Desktop、TRAE SOLO CN、TRAE Work CN、WorkBuddy、其他通用 MCP 客户端和普通终端，不能仅凭当前目录或 `.codex`/`.trae` 文件夹猜测。Codex、TRAE SOLO CN、TRAE Work CN 和 generic 的二进制分别安装在 `%LOCALAPPDATA%/wecom-mcp-v2/clients/<client>`，因此不受工作空间位于 C/D/E 盘影响。Codex 注册到当前用户 `~/.codex/config.toml`，SOLO 注册到 `%APPDATA%/TRAE SOLO CN/User/mcp.json`，Work 使用项目级 `.trae/mcp.json`。只有实际用户级安装目标不可写、平台不支持或宿主不具备本地 stdio MCP/终端能力时才报告 `agent_blocked`；无法确认某个通用客户端的注册路径，不再阻止二进制安装。

MCP 二进制遵循通用 stdio 协议，客户端名称不在预置列表中不等于不支持。像豆包 Work 这类其他客户端，只要端上 Agent 能确认本机 stdio MCP 能力，就先完成通用二进制安装；Agent 能从当前客户端自身确认“添加 MCP”的入口或配置契约时继续自动注册，无法确认时则如实报告 `installed=yes、configured=no`，不会再因为产品名未知而整套阻断，也不会猜用其他客户端的配置路径。

macOS/Linux 安装器原生接受 `--client generic` 和 `--client workbuddy`：两种模式都会完成受校验的通用二进制安装，并把客户端注册留给端上 Agent 自己确认的 MCP 管理入口。端上 Agent 不需要也不应该把 `generic` 改成 `none`、`standalone` 或 `other`。

如果本机缺少组织配置，普通用户只会看到一句话：“程序已经安装，但组织配置尚未部署，请联系本组织技术人员部署 GNAS 本机配置包；部署后重新粘贴原安装指令即可。”用户不需要领取文件、选择目录、告诉 Agent 路径、理解 Schema、填写参数或粘贴密钥。

技术人员将完整配置包部署到固定的用户级受保护位置。Agent 会先读取已有客户端注册项，再自动检查 Windows `%LOCALAPPDATA%/wecom-mcp-v2/config/zoop_wecom_zhycit.local.json` 或 macOS/Linux `${XDG_CONFIG_HOME:-$HOME/.config}/wecom-mcp-v2/zoop_wecom_zhycit.local.json`，并从实例配置内部自动验证 Schema 路径。配置与三项 GNAS 环境齐备后，Agent 使用 Release 内受校验的配置助手备份并合并对应客户端配置，然后提示重启客户端完成只读验证。

安装器只接受固定 Release 资产，自动识别 OS/CPU 架构并校验 `SHA256SUMS`。macOS/Linux 保留旧版本并原子切换 `~/.mcp/wecom-mcp-v2/current`；Windows 由 Release 内的 `install.ps1` 按宿主客户端安装。Codex、TRAE SOLO CN 和 TRAE Work CN 使用 `%LOCALAPPDATA%` 下彼此隔离的用户级目录，WorkBuddy 使用用户级 `.codebuddy/mcp-servers`，普通终端才使用 `.mcp`。Windows 不创建链接。它将 `installed`、`configured`、`loaded`、`verified` 分开报告；没有本地受保护配置时可以只安装二进制，但不会伪装服务已可用。

```sh
curl -fsSL https://raw.githubusercontent.com/zlz3907/wecom-mcp/vX.Y.Z/install.sh | sh -s -- --version vX.Y.Z --client auto
```

`vX.Y.Z` 必须替换为实际存在的固定 Release tag。需要审阅时，先下载固定 tag 的 `install.sh`、`RELEASE-MANIFEST.txt`、`LICENSE` 和 `SHA256SUMS`，按校验和验证后再执行脚本。

下面的 PowerShell 命令只用于技术人员审计，普通用户不需要执行。Windows Agent 在固定 Release 中下载并按 `SHA256SUMS` 校验 `install.ps1` 后，按客户端执行其一：

```powershell
# TRAE SOLO CN：用户级隔离安装，MCP 注册到用户级官方配置
powershell -NoProfile -ExecutionPolicy Bypass -File .\install.ps1 -Version vX.Y.Z -Client trae-solo-cn

# TRAE Work CN：用户级隔离安装；Workspace 仅确定项目级 MCP 配置
powershell -NoProfile -ExecutionPolicy Bypass -File .\install.ps1 -Version vX.Y.Z -Client trae-work-cn -Workspace C:\absolute\workspace

# WorkBuddy 用户级安装
powershell -NoProfile -ExecutionPolicy Bypass -File .\install.ps1 -Version vX.Y.Z -Client workbuddy

# 其他已确认支持本地 stdio MCP 的客户端
powershell -NoProfile -ExecutionPolicy Bypass -File .\install.ps1 -Version vX.Y.Z -Client generic

# Codex Desktop / CLI / IDE extension：用户级隔离安装
powershell -NoProfile -ExecutionPolicy Bypass -File .\install.ps1 -Version vX.Y.Z -Client codex

# 普通独立终端
powershell -NoProfile -ExecutionPolicy Bypass -File .\install.ps1 -Version vX.Y.Z -Client standalone
```

它会自动探查既有客户端引用和固定用户级位置；如果组织配置不存在，安装器只安装二进制并明确报告 `configured=no`，不会复制示例配置、凭据或伪装服务已可用。macOS/Linux 安装器和 Release 内配置助手会自动使用固定配置位置；Windows `install.ps1` 只安装二进制，后续由 Agent 调用受校验的配置助手自动发现固定位置并注册。普通用户不提供配置或 Schema 路径。技术配置包与各客户端后续步骤以 [AGENT_INSTALL_PROMPT.md](AGENT_INSTALL_PROMPT.md) 为准。

Codex 使用当前用户 `~/.codex/config.toml`；TRAE SOLO CN 使用官方用户级 `%APPDATA%/TRAE SOLO CN/User/mcp.json`；TRAE Work CN 使用项目级 `<workspace>/.trae/mcp.json`；WorkBuddy 使用官方用户级 `~/.codebuddy/.mcp.json`。配置助手只备份并合并固定实例，不覆盖未知配置。`mcp-servers/wecom-mcp-v2` 是本项目定义的二进制子目录。当前支持矩阵、回滚规则和发布前门禁见 [便携安装说明](PORTABLE_INSTALL.md)。

## 30 秒 Quick Start

从源码本地构建：

```sh
git clone https://github.com/zlz3907/wecom-mcp.git
cd wecom-mcp
go build -o bin/wecom-mcp-v2 ./cmd/wecom-mcp-v2
cp config/zoop_wecom_zhycit.json.example config/zoop_wecom_zhycit.local.json
```

需要 Go 1.23 或更高版本。复制出的示例配置不能直接运行，请先编辑 `config/zoop_wecom_zhycit.local.json`：

- 将 `tenant_route`、`registry_key` 填为你自己的受管服务路由和登记键。
- 将 `schema_admin_user` 填为运行 MCP 的完整本机系统身份，将 `wecom_operator_userid` 填为当前固定租户中真实、在职且唯一匹配的企业微信 userid；两者不是同一种身份。
- 将 `registry_document_id` 填为已有登记表的文档 ID；留空时只能先由 MCP 客户端显式调用 `wecom_registry_bootstrap` 创建并写回登记表，普通查询不能使用空值。
- 将 `schema_mirror_path` 改为已有 Schema 镜像文件的绝对路径。
- 将 `state_path` 改为本机可写位置的绝对路径。

再通过运行环境提供 `GNAS_BASE_URL`、`GNAS_APP_ID` 和 `GNAS_APP_SECRET`，不要把凭据写入仓库。可以用下面的只读调用检查二进制、环境变量和配置是否能正常加载：

上面的 pipe 入口适合已信任仓库维护者的场景。引导提示词会把 `latest` 仅用于解析固定 tag，再验证同一公开 Release 的 `install.sh` 或 `install.ps1`、`RELEASE-MANIFEST.txt`、`LICENSE`、`SHA256SUMS` 和平台压缩包；安装器不需要 GitHub 登录态，也不会索要或复制 token。安装器拒绝损坏的校验和、tag/平台不匹配以及未声明的资产。它绝不执行 `latest` 直接返回的二进制。Release 的 `SHA256SUMS` 必须同时包含两个安装器、`RELEASE-MANIFEST.txt`、`LICENSE` 和全部平台压缩包。

本地回滚不会下载任何内容：`~/.mcp/wecom-mcp-v2/current` 只会切到再次通过 manifest/hash 校验的既有 release，重启客户端并执行只读 smoke test。可用 `install.sh --rollback <已安装版本>` 完成原子切换。`install.sh --uninstall` 只移除 `current` 软链接，保留 release、备份和客户端配置；客户端配置应通过安装器生成的备份或人工审阅后恢复。输出中的 `installed` 只证明本地受校验文件/current，`configured` 只证明客户端配置写入，`loaded` 必须有当前运行时 initialize/tools/list，`verified` 还必须有真实只读 `wecom_schema_status` tools/call。当前使用 Apache-2.0；许可证只覆盖本项目代码，不覆盖企业微信数据、商标、服务访问权或用户自己的配置。

```sh
printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"wecom_schema_status","arguments":{}}}' | \
  ./bin/wecom-mcp-v2 --config "$PWD/config/zoop_wecom_zhycit.local.json"
```

返回包含 `result` 且没有 `isError` 后，把下面的 stdio 命令注册到 Codex 或 TRAE；请将 `/path/to/wecom-mcp` 替换为仓库的真实绝对路径：

```text
/path/to/wecom-mcp/bin/wecom-mcp-v2 --config /path/to/wecom-mcp/config/zoop_wecom_zhycit.local.json
```

更完整的本地安装、校验和回滚方法见 [便携安装说明](PORTABLE_INSTALL.md)。

## 当前工具

`wecom_instance_initialize` 是 WorkBuddy 等客户端使用的统一初始化入口：先以 `action=status` 获取只读 dry-run 与短期 preview，再在允许执行的状态下以同一工具的 `action=apply` 提交该 preview。原有 `wecom_instance_initialize_status` / `wecom_instance_initialize_apply` 保留为兼容入口。统一入口不会绕过下面的 catalog、权限、快照、幂等或恢复门禁。

`wecom_instance_initialize_status` 是首次初始化的只读入口，同时也是 dry-run。它通过专用的 `instance_initialize` capability group 回读 Registry、唯一 active row、Z-S01 至 Z-S09、生成版 Schema、初始化 journal 和 Z-S01 `limit=1` smoke，只输出结构计数、冲突、计划操作、快照摘要、短期 `preview_id` 与 `expires_at`，不返回业务记录内容。线上字段的 ID、类型、选项和逻辑关联必须与本地 Schema 逐项一致，且 generation 元数据完整，才可能返回 `ready`。`registry_document_id` 是无未决 sentinel 时的既有 Registry 导入候选；`recovery_registry_document_id` / `recovery_business_document_id` 只能绑定已有 uncertain sentinel；业务恢复会继续核验该 sentinel 指向的同一文档，不会猜测或新建替代资产。任一分页、文档管理权限或结构快照不完整时不会签发可用于 apply 的 preview。

`wecom_operator_userid` 是受保护配置中的固定企业微信成员 userid。初始化器先通过成员目录确认其属于当前固定租户，并确保 Registry 与业务文档中该成员为管理员（`auth=7`）；这不代表文档 owner。`ai_execution_subject_record_id` 可固定指向 Z-S09 中已登记的 AI 执行主体；团队 HTTP operator/admin 调用要求该值与验证码确认的人员主体同时存在，用于区分人员发起者与 AI 执行者。原生 API 修改主体仍为 `native_api_actor=application`，不会伪装成企业微信系统修改人。旧 stdio 配置仍可加载，但缺少固定人员或相应 capability 时远程变更保持关闭。

文档权限核验遵循企业微信“获取文档权限信息”（官方文档 path 97461）的实际响应结构，并依赖该接口只能访问调用应用所创建文档的权限约束证明应用管理面；`doc_member_list` 中的人类成员可以是只读、可编辑或管理员，初始化器会严格校验其官方结构。`schema_admin_user` 仅作为受保护本机操作系统账号门禁，不与企业微信 `userid` 混用；Windows 必须填写 `whoami` 返回的完整 `域或电脑名\\用户名`，运行时按 Windows 规则对完整身份忽略大小写比较，不会丢弃 authority 前缀或把不同安全主体的同名账号视为同一人。空的部门权限列表被上游省略时按空列表处理。任意自造的 `auth_type=admin` 等字段不会被视为管理权限证据。

`wecom_instance_initialize_apply` 只接受未过期且与当前完整快照一致的 `preview_id`、status 原样返回的 `preview_expires_at` 和固定 Owner 防误触授权。实际写权限仍由专用 `instance_initialize` capability group 控制；初始化器不会自行扩展白名单。当前生产 catalog 已包含 Z-S01“进度条”的依赖和公式结构，但该字段仍标记为 `unsupported_for_create=true`，因此 catalog 的 `complete_for_creation=false`；需要 fresh 创建 Registry、业务文档或九表时会返回 `capability_gap`，不签发 apply preview，也不执行线上或本地写入。已有完整九表实例的 `ready`/no-op、导入和恢复路径已通过本地 synthetic requester 测试，但这不等于真实企业微信运行验收。已有 `wecom_registry_bootstrap` 和 `wecom_schema_sync` 保持兼容，但不能替代完整实例初始化。

当线上 Registry、唯一 active row 和九表已经满足 catalog 时，apply 会生成并回读新的 Schema generation，执行 Z-S01 候选 smoke，备份并原子切换完整配置，再对持久配置执行最终 Z-S01 只读 smoke。新建文档或子表只允许复用平台唯一默认文本主字段；必须先验证完整默认字段模板，随后才可删除本次操作创建表中的空默认记录。已有 `docid` 只做核验与补齐，不清理已有字段或记录。远程创建或 active row 写入结果不确定时保留 durable journal，并要求回读恢复，禁止盲目重复创建。

示例配置展示完整的 `instance_initialize` 权限集合，但不会迁移已部署实例。现有实例若仅允许只读初始化操作，必须由管理员通过受保护配置包升级 capability group；普通调用者和初始化器本身都不能提升该权限。

WorkBuddy 验收必须逐层报告，不能把安装成功等同于业务可用：

```ini
installed=yes|no
configured=yes|no
loaded=yes|no
registry_verified=yes|no
nine_tables_verified=yes|no
schema_synced=yes|no
tools_call_verified=yes|no
owner_accepted=no
production_deployed=no
```

`wecom_record_query` 提供固定租户内的只读查询：支持 `record_ids` 精确读取、受控 `filter_spec`、排序、`offset/limit` 分页和 `field_ids` 投影。默认返回紧凑单元格值，并显式返回 `record_count`、`returned_count`、`has_more`、`next_offset` 与 `response_truncated`。调用方不能传入租户、文档、子表或凭据参数。

过滤字段 ID、字段类型、排序字段和投影字段都必须存在于本地只读 Schema 镜像；服务端会补齐并校验企业微信所需的 `field_type`，不把客户端提供的任意对象直接转发到线上。

例如按 Z-S03 的任务编号精确查询：

```json
{
  "target_role": "Z-S03",
  "filter_spec": {
    "conjunction": "CONJUNCTION_AND",
    "conditions": [{
      "field_id": "field_task_id",
      "operator": "OPERATOR_IS",
      "string_value": {"value": ["TASK-EXAMPLE-001"]}
    }]
  },
  "field_ids": ["field_task_id", "field_title", "field_status"],
  "limit": 100
}
```

## 安装后怎么用

重新加载客户端中的 MCP 后，可以先从只读问题开始：

- “查看当前企业微信 Schema 的状态。”
- “读取 Zoop 需求表的前 10 条记录。”
- “读取 Zoop 任务表的前 20 条记录，并帮我概括当前进展。”
- “列出 Zoop 协作主体表中的记录。”
- “告诉我这个 MCP 当前有哪些可用工具，不要执行写入。”

实际可见内容取决于本机配置和企业微信权限。需要修改记录时，请明确说明目标和改动内容，并先确认客户端展示的操作。

## 支持范围

- 系统：macOS、Linux、Windows amd64。
- 客户端：Codex、TRAE。
- WorkBuddy：支持官方 MCP 设置入口的引导式配置；未知本机文件契约时低层安装器会阻断，不猜路径、不覆盖配置。
- Windows：提供 amd64 Release 包；通过 Windows CI 原生构建，并用 PowerShell 5.1 分别验证 standalone、Codex 用户级隔离安装/注册、TRAE SOLO CN 用户级隔离安装/注册、TRAE Work CN 用户级隔离安装与项目级配置目标、WorkBuddy 用户级安装及重复安装。

本服务使用 stdio JSON-RPC，每个进程连接一个由本机配置固定的企业微信实例。

## 更多文档

- [便携安装、校验与回滚](PORTABLE_INSTALL.md)
- [字段兼容性](FIELD_CODEC_COMPATIBILITY.md)
- [旧接口支持范围](LEGACY_API_PORT_MATRIX.md)
- [配置示例](config/zoop_wecom_zhycit.json.example)
- [安全策略](SECURITY.md)

工具、Schema 管理和字段处理的详细说明，请按需要查看上述文档和源码。

许可证文本见 [LICENSE](LICENSE)。
