# Agent 安装提示词（Public 版本，唯一规范版本）

将以下完整提示词粘贴给 TRAE、WorkBuddy 或其他具备本机终端与 MCP 管理能力的 Agent。它不依赖对话历史中的安装路径，也不让用户选择系统或 CPU 架构。

```text
为当前用户安装并注册 wecom-mcp-v2 的固定实例 zoop_wecom_zhycit。只能以 GitHub Release 为来源：仓库 zlz3907/wecom-mcp。

按以下安全流程执行：
1. 探查本机 OS、GOARCH 等价 CPU 架构、可用 shell、curl、SHA-256 工具、当前宿主客户端及其真实授权工作空间；用户不负责选择平台、产物或安装前缀。必须区分 TRAE Work CN、WorkBuddy 与普通独立终端，不能跨客户端复用安装或配置路径。
2. 从 https://github.com/zlz3907/wecom-mcp/releases/latest 解析重定向得到固定 release tag；只从该固定 tag 通过公开 HTTPS 下载 RELEASE-MANIFEST.txt、SHA256SUMS、当前平台安装器和资产，不要求 GitHub 登录态，也不得索要、创建或复制任何 token。先用 SHA256SUMS 校验 manifest、安装器与资产，并确认 manifest 的 version、platform 和当前 tag 一致。不得执行未经校验的安装器或 latest URL 直接给出的二进制，也不得从对话历史猜测二进制路径。
3. macOS/Linux：下载并校验 install.sh，再调用它并使用 --client auto；默认前缀为 ~/.mcp/wecom-mcp-v2。Windows amd64：下载并用 PowerShell Get-FileHash 校验同一固定 tag 的 `install.ps1`，Windows 安装、外层与包内 SHA-256 校验、版本目录解压和重复安装检查全部交给该脚本，不要由 Agent 临时拼装命令。按宿主调用：TRAE Work CN 使用 `powershell -NoProfile -ExecutionPolicy Bypass -File <verified-install.ps1> -Version <fixed-tag> -Client trae-work-cn -Workspace <absolute-authorized-workspace>`，安装到官方项目级 `.trae` 配置范围内的 `<workspace>/.trae/mcp-servers/wecom-mcp-v2/releases/<tag>-windows-amd64`；WorkBuddy 使用 `... -Client workbuddy`，安装到官方用户级 `.codebuddy` 配置范围内的 `$HOME/.codebuddy/mcp-servers/wecom-mcp-v2/releases/<tag>-windows-amd64`，遇到官方权限确认时展示准确目标并等待用户批准；其他普通终端使用 `... -Client standalone`，安装到 `$HOME/.mcp/wecom-mcp-v2/releases/<tag>-windows-amd64`。`mcp-servers/wecom-mcp-v2` 是本项目定义的二进制子目录，不冒充客户端官方内置目录。所有模式都不创建 `current`、符号链接或 junction，保留旧 releases 且不覆盖未知文件。
4. 安装后必须主动判断是“已有本地配置”还是“首次配置”，不得只说“请提供配置文件路径”就结束：
   - 已有配置：可以探查已存在的客户端配置及其引用，仅确认 `zoop_wecom_zhycit.local.json` 的真实绝对路径、普通文件、可读性和 JSON 结构，不得输出配置内容、凭据、token、secret、doc ID 或 sheet ID。macOS/Linux 才将该路径作为 `--config` 传给 `install.sh`；Windows `install.ps1` 不接受也不处理实例配置，该路径只能稍后写入客户端 MCP 的 `args`。
   - 首次配置：明确告知用户 Release 中的 `config/zoop_wecom_zhycit.json.example` 只是模板，不能直接运行。现有 Zoop 租户的用户应向本组织的管理员获取：(1) 已填写的本地实例配置，(2) 与其匹配的只读 Schema 镜像，(3) `GNAS_BASE_URL`、`GNAS_APP_ID`、`GNAS_APP_SECRET` 三项运行凭据的私密配置方式。不得让普通用户猜测 `tenant_route`、`registry_key`、`registry_document_id` 或 Schema 路径，不得让用户把三项凭据粘贴进对话。若是全新租户、尚无这些资产，说明需要由管理员单独完成租户开通和 Schema/registry 初始化；本安装流程不得自动执行这些业务写入。
   - 安全落盘：用户获得上述资产后，将实例配置和 Schema 镜像放到受保护的本机目录，不放入 Git 、共享盘或项目中可被提交的位置；限制为当前用户可读。Agent 只记录经用户确认的绝对路径。在此之前 `configured=no`，但已校验的二进制安装仍可为 `installed=yes`。
5. 配置资产齐备后再注册客户端：
   - macOS/Linux 上的 Codex 与 TRAE SOLO CN 只使用安装器内置的最小、备份后、幂等注册器。
   - Windows 只注册 `install.ps1` 输出的 `binary_path`，`args` 为 `["--config", "<existing-absolute-local-config-path>"]`，不创建 `current`，也不将 `--config` 传给 `install.ps1`。
   - TRAE Work CN 使用官方“设置 > MCP”入口合并项目级 `<workspace>/.trae/mcp.json`；配置项使用 `command`、`args` 和 `env`。WorkBuddy 使用官方 MCP 管理入口合并用户级 `$HOME/.codebuddy/.mcp.json`；配置项使用 `type: stdio`、`command`、`args` 和 `env`。三项 GNAS 凭据必须由用户在客户端官方私密入口或组织批准的环境变量机制中提供；不得将真实凭据写入项目级可提交文件、输出或对话。若客户端支持环境变量引用，优先引用而非存储明文。
   - 修改配置前先备份，只合并 `zoop_wecom_zhycit` 并保留其他服务；不要猜其它配置路径，不要覆盖整个配置。当且仅当 `command`、`args`、本地配置/Schema 和三项运行环境都已完成时，`configured=yes`。无法确认客户端契约时输出 `agent_blocked`。
6. 注册完成后提示重启对应客户端，并告知用户在 MCP 面板查看 `zoop_wecom_zhycit` 的启动状态。只有在当前客户端 MCP runtime 中完成 initialize、tools/list，且真实调用一次只读 wecom_schema_status 后，才可把 loaded/verified 置为 yes。静态配置只能证明 configured；tools/list 不能代替 tools/call。若当前 Agent 无法访问客户端自身的 MCP runtime，configured 可以是 yes，但 loaded/verified 必须保持 unknown 或 no，同时给出用户在该客户端内执行只读验证的具体下一步。
7. 全程不得读取或写入企业微信业务表，不调用 wecom_record_apply、schema sync/migration、reconcile 或计划任务。

最终按以下机器可读契约输出，不要省略任何字段；安装器已经输出的字段应原样保留，路径只可使用 `$HOME/...`、`$PREFIX/...` 或 `<absolute-path>` 形式：
result=passed|agent_blocked|failed
operation=install|rollback|uninstall
release_version=<fixed tag or unknown>
platform=<detected OS/arch>
client=trae-work-cn|workbuddy|standalone|other
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

在公开 Release 无法访问、对应客户端的受管目标无权限、缺少 SHA-256 工具、未知客户端契约或平台不支持时，停止并输出 result=agent_blocked。TRAE Work CN 的授权项目级 `.trae` 路径不是“回退到项目工作区”，而是该客户端的正式 project scope；不得改写到项目根目录的其它影子 `.mcp` 或 `.runtime`。无本地配置不是二进制安装失败：只要 Release 和包内文件校验通过且客户端范围内的版本目录已经落盘，就输出 result=passed、installed=yes、configured=no、loaded=no、verified=no。此时必须在机器可读契约之前先输出“首次配置清单”，说明需向组织管理员获取的两个本地文件和三项私密运行环境，以及获得后应返回当前客户端的“设置 > MCP”继续注册。`next_action` 必须是这一个可执行动作，不得只说“提供既有配置路径”，不得填写不存在的占位配置路径。不得删除旧版本、不得伪造成功、不得索要或复制凭据、不得重试不确定的写入。
```

状态含义：`installed` 只表示受校验的本地版本目录已经落盘且 `binary_path` 指向其中的二进制；不要求存在 `current` 链接。`configured` 只表示客户端配置已安全写入；`loaded` 需要当前客户端运行时握手与工具发现；`verified` 必须有真实的只读 `wecom_schema_status` tools/call。四项不可互相推导。

测试示例：在没有本地 `*.local.json` 时，三个 Windows 模式都应输出 `result=passed`、`installed=yes`、`configured=no`、`loaded=no`、`verified=no`。TRAE Work CN 的 `binary_path` 必须位于所传工作空间的 `.trae/mcp-servers` 下；WorkBuddy 必须位于用户 `.codebuddy/mcp-servers` 下；standalone 才使用用户 `.mcp`。三个模式都不得创建 `current`、符号链接或 junction。自动化覆盖见 `scripts/test-windows-installer.ps1`。
