# Agent 安装提示词（Public 版本，唯一规范版本）

将以下完整提示词粘贴给任何具备本机终端与本地 stdio MCP 管理能力的 Agent。它不依赖对话历史中的安装路径，也不让用户选择系统、CPU 架构或客户端类型。

```text
为当前用户安装并注册 wecom-mcp-v2 的固定实例 zoop_wecom_zhycit。只能以 GitHub Release 为来源：仓库 zlz3907/wecom-mcp。

按以下安全流程执行：
1. 探查本机 OS、GOARCH 等价 CPU 架构、可用 shell、curl、SHA-256 工具、当前宿主客户端及其真实授权工作空间；用户不负责选择平台、产物、安装前缀或客户端类型。客户端身份只以当前产品界面/宿主明确暴露的产品名、Agent 运行时元数据或父应用可执行文件身份为证据：Codex Desktop、ChatGPT desktop app 的本地 Codex 宿主、Codex CLI 或 Codex IDE extension 使用 `codex`；产品名为 TRAE SOLO CN 才使用 `trae-solo-cn`，产品名为 TRAE Work/TraeWork 才使用 `trae-work-cn`；WorkBuddy 使用 `workbuddy`；普通独立终端使用 `standalone`。其他产品（例如豆包 Work）只要当前 Agent 能确认宿主支持本地 stdio MCP，就使用 `generic`，不得仅因产品名未预置或暂时找不到注册配置路径而阻断二进制安装。当前目录、项目规则、存在 `.codex`/`.trae`、发现某个无归属的历史配置文件或安装目录名称不能单独证明客户端身份或注册契约。证据冲突、宿主不支持本地 stdio MCP、或 Agent 连安装所需终端能力都没有时，才输出 `client=other`、`result=agent_blocked`。不要给出“新开对话重试”这种不会改变宿主能力的无效建议。
2. 从 https://github.com/zlz3907/wecom-mcp/releases/latest 解析重定向得到固定 release tag；只从该固定 tag 通过公开 HTTPS 下载 RELEASE-MANIFEST.txt、SHA256SUMS、当前平台安装器和资产，不要求 GitHub 登录态，也不得索要、创建或复制任何 token。先用 SHA256SUMS 校验 manifest、安装器与资产，并确认 manifest 的 version、platform 和当前 tag 一致。不得执行未经校验的安装器或 latest URL 直接给出的二进制，也不得从对话历史猜测二进制路径。
3. macOS/Linux：下载并校验 install.sh；Codex 与 TRAE SOLO CN 使用安装器已有的对应 `--client` 适配器；豆包 Work 等其他已确认支持本地 stdio MCP 的客户端直接使用安装器正式支持的 `--client generic`；WorkBuddy 使用 `--client workbuddy`。`generic` 与 `workbuddy` 都会先把通用二进制安装到默认前缀 `~/.mcp/wecom-mcp-v2`，且不会调用不匹配的内置注册器，再由当前 Agent 按第 5 步使用宿主自身契约注册。不得再把 `generic` 改写成 `none`、`standalone` 或 `other`。Windows amd64 下载并校验同一固定 tag 的 `install.ps1` 后，由 Agent 直接执行。Windows 二进制一律放在当前用户可写范围，不依赖项目位于哪个盘：通用 MCP 客户端使用 `-Client generic`，安装到 `%LOCALAPPDATA%/wecom-mcp-v2/clients/generic/releases/<tag>-windows-amd64`；Codex 使用 `-Client codex`；TRAE SOLO CN 使用 `-Client trae-solo-cn`；TRAE Work CN 使用 `-Client trae-work-cn -Workspace <absolute-authorized-workspace>`；WorkBuddy 使用 `-Client workbuddy`；只有真实普通终端才使用 `-Client standalone`。不得因为项目盘不可写而改装到项目目录。`install.ps1` 会在 Release 下载前验证实际用户级安装目标的创建、重命名和删除；预检失败必须立即输出 `agent_blocked`，不得回退、不得准备 `.cmd`/wizard、不得让用户执行 PowerShell或手动清理、不得重试。权限预检通过后，安装器只自动清理超过 15 分钟且名称严格匹配自身格式的旧 staging/权限探针。所有 Windows 模式都不创建 `current`、符号链接或 junction，保留旧 releases 且不覆盖未知文件。
4. 安装后由 Agent 自动发现组织配置，普通用户不提供、选择、复制、移动或确认任何路径：
   - 发现顺序固定且有界：(1) 当前客户端已有 `zoop_wecom_zhycit` 注册项中 `--config` 引用的绝对路径；(2) Windows 固定受保护位置 `%LOCALAPPDATA%/wecom-mcp-v2/config/zoop_wecom_zhycit.local.json`；(3) macOS/Linux 固定位置 `${XDG_CONFIG_HOME:-$HOME/.config}/wecom-mcp-v2/zoop_wecom_zhycit.local.json`；(4) 仅为兼容旧安装，检查 `$HOME/.trae/mcp-config/wecom/zoop_wecom_zhycit.local.json`。不得递归搜索磁盘、不得在 workspace 中寻找或放置配置、不得询问用户“文件在哪里”。
   - 找到实例配置后，只验证它是绝对路径下的可读普通文件且非链接，JSON 结构有效、`instance_name=zoop_wecom_zhycit`。从配置内部自动读取 `schema_mirror_path` 并验证其为绝对路径下的可读普通文件和有效 JSON；不得再次要求用户提供 Schema 路径。只确认三项 GNAS 运行环境是否已由组织持久化配置，不输出或复制其值，不输出配置内容、凭据、token、secret、doc ID 或 sheet ID。
   - 未找到配置或缺少 Schema/三项 GNAS 环境时，二进制安装仍可为 `installed=yes`，但不得注册半成品。面向普通用户只输出：“程序已经安装，但组织配置尚未部署，请联系本组织技术人员部署 GNAS 本机配置包；部署后重新粘贴原安装指令即可。”不得列出参数让用户收集，不得要求用户把文件放到某处、告诉 Agent 路径、粘贴凭据或执行命令。
   - 技术人员负责把完整配置包部署到上述固定受保护位置：实例配置内部已写好 Schema 绝对路径，并通过组织批准机制持久化三项 GNAS 环境；配置和 Schema 不放入 Git、共享盘或 workspace，权限限制为当前用户可读。Release 中的 `.json.example` 只是技术模板，Agent 不复制、不补全、不直接运行。全新租户的开通和 Schema/registry 初始化由管理员另行完成，本流程不执行这些业务写入。
5. 配置资产齐备后再注册客户端：
   - macOS/Linux 上的 Codex 与 TRAE SOLO CN 只使用安装器内置的最小、备份后、幂等注册器。
   - Windows 只注册 `install.ps1` 输出的 `binary_path`，不创建 `current`，也不将 `--config` 传给 `install.ps1`。配置位于固定受保护位置时，包内受校验的 `wecom-mcp-v2-configure.exe` 会自动使用该位置，Agent 不向用户询问路径；兼容已有客户端引用时，由 Agent 使用自动发现的既有绝对路径。Codex 在实例配置、内部 Schema 引用和组织持久化的三项 GNAS 环境都存在时，调用配置助手的 `codex` 模式备份并合并当前用户 `~/.codex/config.toml`；TRAE SOLO CN 同样调用配置助手合并 `%APPDATA%/TRAE SOLO CN/User/mcp.json`。TRAE Work CN 则显式传入项目级 `.trae/mcp.json`。缺少任一组织资产时只提示联系技术人员部署配置包，不写半成品注册项。
   - TRAE Work CN 由 Agent 使用官方“设置 > MCP”入口或已确认的项目级契约合并 `<workspace>/.trae/mcp.json`；配置项使用 `command`、`args` 和 `env`。WorkBuddy 由 Agent 使用当前客户端官方 MCP 管理入口合并用户级 `$HOME/.codebuddy/.mcp.json`；配置项使用 `type: stdio`、`command`、`args` 和 `env`。普通用户不粘贴 JSON、不选择配置路径，也不提供环境变量。三项 GNAS 凭据由组织技术人员通过客户端官方私密入口或组织批准的持久化环境机制预置；不得将真实凭据写入项目级可提交文件、输出或对话。若客户端支持环境变量引用，优先引用而非存储明文。
   - 对 `generic` 客户端，Agent 优先直接操作当前宿主自己提供的“添加 MCP/工具”入口，注册名称 `zoop_wecom_zhycit`、传输类型 `stdio`、安装器已验证的 `binary_path` 以及参数 `--config <自动发现的配置路径>`。若宿主没有可操作界面，但 Agent 能从宿主内置帮助、运行时 API 或明确归属于该宿主的现有配置确认格式与路径，则先备份、仅合并固定实例并保留其他服务。不得把其他客户端的配置格式或路径套用过来，不得仅凭网络文章、相似文件名或目录猜测。能安装但无法确认注册契约时，保留已验证二进制并输出 `result=passed`、`installed=yes`、`configured=no`，用普通语言提示“程序已安装，但当前客户端未提供可确认的 MCP 注册入口”；不得退回 `installed=no`，也不得要求用户换客户端或执行命令。
   - 修改配置前先备份，只合并 `zoop_wecom_zhycit` 并保留其他服务；不要猜其它配置路径，不要覆盖整个配置。当且仅当 `command`、`args`、本地配置/Schema 和三项运行环境都已完成时，`configured=yes`。无法确认客户端契约时输出 `agent_blocked`。
6. 注册完成后提示重启对应客户端，并告知用户在 MCP 面板查看 `zoop_wecom_zhycit` 的启动状态。只有在当前客户端 MCP runtime 中完成 initialize、tools/list，且真实调用一次只读 wecom_schema_status 后，才可把 loaded/verified 置为 yes。静态配置只能证明 configured；tools/list 不能代替 tools/call。若当前 Agent 无法访问客户端自身的 MCP runtime，configured 可以是 yes，但 loaded/verified 必须保持 unknown 或 no，同时给出用户在该客户端内执行只读验证的具体下一步。
7. 全程不得读取或写入企业微信业务表，不调用 wecom_record_apply、schema sync/migration、reconcile 或计划任务。

最终按以下机器可读契约输出，不要省略任何字段；安装器已经输出的字段应原样保留，路径只可使用 `$HOME/...`、`$PREFIX/...` 或 `<absolute-path>` 形式：
result=passed|agent_blocked|failed
operation=install|rollback|uninstall
release_version=<fixed tag or unknown>
platform=<detected OS/arch>
client=codex|trae-solo-cn|trae-work-cn|workbuddy|standalone|generic|other
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

在公开 Release 无法访问、缺少 SHA-256 工具、安装目标不可写、平台不支持、或宿主不具备本地 stdio MCP/终端能力时，停止并输出 result=agent_blocked。未知客户端的“注册契约”本身不是二进制安装阻断条件：按通用模式安装成功后输出 result=passed、installed=yes、configured=no；只有注册步骤保持未完成。`result=passed` 必须至少满足 `installed=yes`，严禁出现 `passed` 与 `installed=no`。不得给用户 PowerShell、`.cmd`、白名单修改、staging 清理或 standalone 降级步骤。无本地配置不是二进制安装失败：版本目录真实落盘并通过包内校验后，可输出 result=passed、installed=yes、configured=no、loaded=no、verified=no，并只用普通语言提示联系组织技术人员部署 GNAS 本机配置包、部署后重新粘贴原安装指令。不得要求普通用户提供配置或 Schema 路径，不得填写不存在的占位路径，不得删除旧版本、不得伪造成功、不得索要或复制凭据、不得重试不确定的写入。
```

状态含义：`installed` 只表示受校验的本地版本目录已经落盘且 `binary_path` 指向其中的二进制；不要求存在 `current` 链接。`configured` 只表示客户端配置已安全写入；`loaded` 需要当前客户端运行时握手与工具发现；`verified` 必须有真实的只读 `wecom_schema_status` tools/call。四项不可互相推导。

测试示例：在没有本地 `*.local.json` 时，六个 Windows 模式都应输出 `result=passed`、`installed=yes`、`configured=no`、`loaded=no`、`verified=no`。generic、Codex、TRAE SOLO CN 与 TRAE Work CN 的 `binary_path` 必须分别位于 `%LOCALAPPDATA%/wecom-mcp-v2/clients/generic`、`%LOCALAPPDATA%/wecom-mcp-v2/clients/codex`、`%LOCALAPPDATA%/wecom-mcp-v2/clients/trae-solo-cn` 和 `%LOCALAPPDATA%/wecom-mcp-v2/clients/trae-work-cn`。Codex 的 `config_paths` 必须是当前用户 `~/.codex/config.toml`，SOLO 必须是 `%APPDATA%/TRAE SOLO CN/User/mcp.json`；WorkBuddy 必须位于用户 `.codebuddy/mcp-servers` 下；generic 在注册前保持 `configured=no`；standalone 才使用用户 `.mcp`。macOS/Linux 还必须直接验证 `--client generic` 与 `--client workbuddy` 均能安装且不会误报注册。自动化覆盖见 `scripts/test-github-installer.sh` 与 `scripts/test-windows-installer.ps1`。
