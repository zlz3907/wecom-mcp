# wecom-mcp-v2

这是一个连接**企业微信智能表格**的 MCP 服务。

安装后，Codex 或 TRAE 可以用自然语言查询 Zoop 的需求、任务等记录，也可以在明确指令下受控维护记录。它适合已经使用 Zoop 管理研发工作，希望直接在 AI 客户端里查看和更新企业微信智能表格的个人或团队。

## 非技术用户自动安装

现在用户只需把下面这句话粘贴给 TRAE、WorkBuddy 或其他具备本机终端和 MCP 管理能力的 Agent：

```text
请安装 wecom-mcp-v2，并严格执行：https://raw.githubusercontent.com/zlz3907/wecom-mcp/main/AGENT_INSTALL_PROMPT.md
```

完整安装规则只维护在 [AGENT_INSTALL_PROMPT.md](AGENT_INSTALL_PROMPT.md)，避免 README 与安装规范不一致。非技术用户不需要执行 PowerShell、双击 `.cmd`、选择 CPU 架构、修改白名单或手动清理 staging 目录。

Windows Agent 必须准确区分 TRAE SOLO CN、TRAE Work CN、WorkBuddy 和普通终端。TRAE SOLO CN 的二进制安装在当前授权工作空间的 `.trae/mcp-servers/wecom-mcp-v2`，注册到官方用户配置 `%APPDATA%/TRAE SOLO CN/User/mcp.json`；TRAE Work CN 使用项目级 `.trae/mcp.json`。两者都由 Agent 自动完成，不得把 SOLO 降级为 standalone。若正式目标范围不可写，只能如实报告 `agent_blocked`，不能输出 `result=passed`，也不能把命令或文件操作转交给用户。

如果本机缺少组织实例配置，Agent 会明确提示用户联系本组织技术人员获取：

- `zoop_wecom_zhycit.local.json` 实例配置；
- 与实例配置匹配的只读 Schema 镜像；
- 由组织批准的 `GNAS_BASE_URL`、`GNAS_APP_ID`、`GNAS_APP_SECRET` 私密配置方式。

安装流程不会让非技术用户猜测参数，也不会要求用户把密钥粘贴进对话。配置资产齐备且三项 GNAS 环境已由组织安全配置后，Agent 使用 Release 内受校验的配置助手备份并合并对应客户端配置，然后提示重启 TRAE 完成只读验证。

安装器只接受固定 Release 资产，自动识别 OS/CPU 架构并校验 `SHA256SUMS`。macOS/Linux 保留旧版本并原子切换 `~/.mcp/wecom-mcp-v2/current`；Windows 由 Release 内的 `install.ps1` 按宿主客户端安装。TRAE SOLO CN 和 TRAE Work CN 的二进制使用当前工作空间的 `.trae/mcp-servers`，WorkBuddy 使用用户级 `.codebuddy/mcp-servers`，普通终端才使用 `.mcp`。Windows 不创建链接。它将 `installed`、`configured`、`loaded`、`verified` 分开报告；没有本地受保护配置时可以只安装二进制，但不会伪装服务已可用。

```sh
curl -fsSL https://raw.githubusercontent.com/zlz3907/wecom-mcp/vX.Y.Z/install.sh | sh -s -- --version vX.Y.Z --client auto
```

`vX.Y.Z` 必须替换为实际存在的固定 Release tag。需要审阅时，先下载固定 tag 的 `install.sh`、`RELEASE-MANIFEST.txt`、`LICENSE` 和 `SHA256SUMS`，按校验和验证后再执行脚本。

下面的 PowerShell 命令只用于技术人员审计，普通用户不需要执行。Windows Agent 在固定 Release 中下载并按 `SHA256SUMS` 校验 `install.ps1` 后，按客户端执行其一：

```powershell
# TRAE SOLO CN：二进制使用当前工作空间，MCP 注册到用户级官方配置
powershell -NoProfile -ExecutionPolicy Bypass -File .\install.ps1 -Version vX.Y.Z -Client trae-solo-cn -Workspace C:\absolute\workspace

# TRAE Work CN：Workspace 必须是当前真实授权工作空间
powershell -NoProfile -ExecutionPolicy Bypass -File .\install.ps1 -Version vX.Y.Z -Client trae-work-cn -Workspace C:\absolute\workspace

# WorkBuddy 用户级安装
powershell -NoProfile -ExecutionPolicy Bypass -File .\install.ps1 -Version vX.Y.Z -Client workbuddy

# 普通独立终端
powershell -NoProfile -ExecutionPolicy Bypass -File .\install.ps1 -Version vX.Y.Z -Client standalone
```

它会自动探查既有的本地实例配置；如果该本地配置不存在，安装器只安装二进制并明确报告 `configured=no`，不会复制示例配置、凭据或伪装服务已可用。macOS/Linux 可将受保护的既有配置传给 `install.sh`；Windows `install.ps1` 只安装二进制，配置文件绝对路径在之后的客户端 MCP 注册中作为 `--config` 参数使用。首次安装时所需的本地配置、Schema 镜像、三项私密运行环境及各客户端的后续步骤，以 [AGENT_INSTALL_PROMPT.md](AGENT_INSTALL_PROMPT.md) 为准。

TRAE SOLO CN 使用官方用户级 `%APPDATA%/TRAE SOLO CN/User/mcp.json`；TRAE Work CN 使用项目级 `<workspace>/.trae/mcp.json`；WorkBuddy 使用官方用户级 `~/.codebuddy/.mcp.json`。配置助手只备份并合并固定实例，不覆盖未知 JSON。`mcp-servers/wecom-mcp-v2` 是本项目定义的二进制子目录。当前支持矩阵、回滚规则和发布前门禁见 [便携安装说明](PORTABLE_INSTALL.md)。

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
- Windows：提供 amd64 Release 包；通过 Windows CI 原生构建，并用 PowerShell 5.1 分别验证 standalone、TRAE SOLO CN 自动安装/用户级注册、TRAE Work CN 项目级安装、WorkBuddy 用户级安装及重复安装。

本服务使用 stdio JSON-RPC，每个进程连接一个由本机配置固定的企业微信实例。

## 更多文档

- [便携安装、校验与回滚](PORTABLE_INSTALL.md)
- [字段兼容性](FIELD_CODEC_COMPATIBILITY.md)
- [旧接口支持范围](LEGACY_API_PORT_MATRIX.md)
- [配置示例](config/zoop_wecom_zhycit.json.example)
- [安全策略](SECURITY.md)

工具、Schema 管理和字段处理的详细说明，请按需要查看上述文档和源码。

许可证文本见 [LICENSE](LICENSE)。
