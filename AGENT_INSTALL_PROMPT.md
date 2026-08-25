# Agent 安装提示词（Public 版本，唯一规范版本）

将以下完整提示词粘贴给 TRAE、WorkBuddy 或其他具备本机终端与 MCP 管理能力的 Agent。它不依赖对话历史中的安装路径，也不让用户选择系统或 CPU 架构。

```text
为当前用户安装并注册 wecom-mcp-v2 的固定实例 zoop_wecom_zhycit。只能以 GitHub Release 为来源：仓库 zlz3907/wecom-mcp。

按以下安全流程执行：
1. 探查本机 OS、GOARCH 等价 CPU 架构、可用 shell、curl、SHA-256 工具、当前宿主客户端及其真实授权工作空间；用户不负责选择平台、产物或安装前缀。必须准确区分 TRAE SOLO CN、TRAE Work CN、WorkBuddy 与普通独立终端，不能把 TRAE SOLO CN 当成 standalone，也不能跨客户端复用配置路径。
2. 从 https://github.com/zlz3907/wecom-mcp/releases/latest 解析重定向得到固定 release tag；只从该固定 tag 通过公开 HTTPS 下载 RELEASE-MANIFEST.txt、SHA256SUMS、当前平台安装器和资产，不要求 GitHub 登录态，也不得索要、创建或复制任何 token。先用 SHA256SUMS 校验 manifest、安装器与资产，并确认 manifest 的 version、platform 和当前 tag 一致。不得执行未经校验的安装器或 latest URL 直接给出的二进制，也不得从对话历史猜测二进制路径。
3. macOS/Linux：下载并校验 install.sh，再调用它并使用 --client auto；默认前缀为 ~/.mcp/wecom-mcp-v2。Windows amd64 下载并校验同一固定 tag 的 `install.ps1` 后，由 Agent 直接执行：TRAE SOLO CN 使用 `-Client trae-solo-cn -Workspace <absolute-authorized-workspace>`，二进制安装到 `<workspace>/.trae/mcp-servers/wecom-mcp-v2/releases/<tag>-windows-amd64`，后续注册目标固定为 `%APPDATA%/TRAE SOLO CN/User/mcp.json`；TRAE Work CN 使用 `-Client trae-work-cn -Workspace <absolute-authorized-workspace>`，二进制使用同一项目级子目录并注册到 `<workspace>/.trae/mcp.json`；WorkBuddy 使用 `-Client workbuddy`；只有真实普通终端才使用 `-Client standalone`。`install.ps1` 会在 Release 下载前验证目标范围的创建、重命名和删除；预检失败必须立即输出 `agent_blocked`，不得回退、不得准备 `.cmd`/wizard、不得让用户执行 PowerShell或手动清理、不得重试。权限预检通过后，安装器只自动清理超过 15 分钟且名称严格匹配自身格式的旧 staging/权限探针。所有 Windows 模式都不创建 `current`、符号链接或 junction，保留旧 releases 且不覆盖未知文件。
4. 安装后必须主动判断是“已有本地配置”还是“首次配置”，不得只说“请提供配置文件路径”就结束：
   - 已有配置：可以探查已存在的客户端配置及其引用，仅确认 `zoop_wecom_zhycit.local.json` 的真实绝对路径、普通文件、可读性和 JSON 结构，不得输出配置内容、凭据、token、secret、doc ID 或 sheet ID。macOS/Linux 才将该路径作为 `--config` 传给 `install.sh`；Windows `install.ps1` 不接受也不处理实例配置，该路径只能稍后写入客户端 MCP 的 `args`。
   - 首次配置：必须用面向非技术人员的普通语言明确提示“程序已经安装，但还需要联系本组织技术人员获取 GNAS 配置”。Release 中的 `config/zoop_wecom_zhycit.json.example` 只是模板，不能直接运行。现有 Zoop 租户的用户应向技术人员获取：(1) 已填写的 `zoop_wecom_zhycit.local.json` 实例配置，(2) 与其匹配的只读 Schema 镜像，(3) `GNAS_BASE_URL`、`GNAS_APP_ID`、`GNAS_APP_SECRET` 三项运行凭据的组织批准私密配置方式。不得让普通用户猜测参数，不得让用户把三项凭据粘贴进对话，也不得让用户操作命令或脚本文件。若是全新租户、尚无这些资产，说明需要由管理员单独完成租户开通和 Schema/registry 初始化；本安装流程不得自动执行这些业务写入。
   - 安全落盘：用户获得上述资产后，将实例配置和 Schema 镜像放到受保护的本机目录，不放入 Git 、共享盘或项目中可被提交的位置；限制为当前用户可读。Agent 只记录经用户确认的绝对路径。在此之前 `configured=no`，但已校验的二进制安装仍可为 `installed=yes`。
5. 配置资产齐备后再注册客户端：
   - macOS/Linux 上的 Codex 与 TRAE SOLO CN 只使用安装器内置的最小、备份后、幂等注册器。
   - Windows 只注册 `install.ps1` 输出的 `binary_path`，`args` 为 `["--config", "<existing-absolute-local-config-path>"]`，不创建 `current`，也不将 `--config` 传给 `install.ps1`。TRAE SOLO CN 在实例配置、Schema 镜像和组织持久化配置的三项 GNAS 环境都存在时，调用包内受校验的 `wecom-mcp-v2-configure.exe -client trae-solo-cn`，备份并合并 `%APPDATA%/TRAE SOLO CN/User/mcp.json`。TRAE Work CN 则显式传入项目级 `.trae/mcp.json`。缺少任一配置资产时只提示联系技术人员，不写半成品注册项。
   - TRAE Work CN 使用官方“设置 > MCP”入口合并项目级 `<workspace>/.trae/mcp.json`；配置项使用 `command`、`args` 和 `env`。WorkBuddy 使用官方 MCP 管理入口合并用户级 `$HOME/.codebuddy/.mcp.json`；配置项使用 `type: stdio`、`command`、`args` 和 `env`。三项 GNAS 凭据必须由用户在客户端官方私密入口或组织批准的环境变量机制中提供；不得将真实凭据写入项目级可提交文件、输出或对话。若客户端支持环境变量引用，优先引用而非存储明文。
   - 修改配置前先备份，只合并 `zoop_wecom_zhycit` 并保留其他服务；不要猜其它配置路径，不要覆盖整个配置。当且仅当 `command`、`args`、本地配置/Schema 和三项运行环境都已完成时，`configured=yes`。无法确认客户端契约时输出 `agent_blocked`。
6. 注册完成后提示重启对应客户端，并告知用户在 MCP 面板查看 `zoop_wecom_zhycit` 的启动状态。只有在当前客户端 MCP runtime 中完成 initialize、tools/list，且真实调用一次只读 wecom_schema_status 后，才可把 loaded/verified 置为 yes。静态配置只能证明 configured；tools/list 不能代替 tools/call。若当前 Agent 无法访问客户端自身的 MCP runtime，configured 可以是 yes，但 loaded/verified 必须保持 unknown 或 no，同时给出用户在该客户端内执行只读验证的具体下一步。
7. 全程不得读取或写入企业微信业务表，不调用 wecom_record_apply、schema sync/migration、reconcile 或计划任务。

最终按以下机器可读契约输出，不要省略任何字段；安装器已经输出的字段应原样保留，路径只可使用 `$HOME/...`、`$PREFIX/...` 或 `<absolute-path>` 形式：
result=passed|agent_blocked|failed
operation=install|rollback|uninstall
release_version=<fixed tag or unknown>
platform=<detected OS/arch>
client=trae-solo-cn|trae-work-cn|workbuddy|standalone|other
installed=yes|no
configured=yes|no
loaded=yes|no|unknown
verified=yes|no|unknown
binary_path=<path or missing>
binary_sha256=<sha256 or missing>
config_paths=<redacted paths only>
rollback_target=<version or none>
evidence=<release manifest/checksum, installer output, and runtime call result; do not include secrets>
next_action=<one concrete action>

在公开 Release 无法访问、缺少 SHA-256 工具、未知客户端契约、正式目标不可写或平台不支持时，停止并输出 result=agent_blocked。`result=passed` 必须至少满足 `installed=yes`；严禁出现 `passed` 与 `installed=no`。不得给用户 PowerShell、`.cmd`、白名单修改、staging 清理或 standalone 降级步骤。无本地配置不是二进制安装失败：客户端范围内版本目录真实落盘并通过包内校验后，才可输出 result=passed、installed=yes、configured=no、loaded=no、verified=no，并用普通语言提示联系组织技术人员获取两个本地文件和三项私密运行环境。不得填写不存在的占位配置路径，不得删除旧版本、不得伪造成功、不得索要或复制凭据、不得重试不确定的写入。
```

状态含义：`installed` 只表示受校验的本地版本目录已经落盘且 `binary_path` 指向其中的二进制；不要求存在 `current` 链接。`configured` 只表示客户端配置已安全写入；`loaded` 需要当前客户端运行时握手与工具发现；`verified` 必须有真实的只读 `wecom_schema_status` tools/call。四项不可互相推导。

测试示例：在没有本地 `*.local.json` 时，四个 Windows 模式都应输出 `result=passed`、`installed=yes`、`configured=no`、`loaded=no`、`verified=no`。TRAE SOLO CN 与 TRAE Work CN 的 `binary_path` 必须位于所传工作空间的 `.trae/mcp-servers` 下；SOLO 的 `config_paths` 必须是 `%APPDATA%/TRAE SOLO CN/User/mcp.json`；WorkBuddy 必须位于用户 `.codebuddy/mcp-servers` 下；standalone 才使用用户 `.mcp`。自动化覆盖见 `scripts/test-windows-installer.ps1`。
