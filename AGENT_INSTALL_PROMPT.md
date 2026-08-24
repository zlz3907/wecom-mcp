# Agent 安装提示词（Public 版本，唯一规范版本）

将以下完整提示词粘贴给 TRAE、WorkBuddy 或其他具备本机终端与 MCP 管理能力的 Agent。它不依赖对话历史中的安装路径，也不让用户选择系统或 CPU 架构。

```text
为当前用户安装并注册 wecom-mcp-v2 的固定实例 zoop_wecom_zhycit。只能以 GitHub Release 为来源：仓库 zlz3907/wecom-mcp。

按以下安全流程执行：
1. 探查本机 OS、GOARCH 等价 CPU 架构、可用 shell、curl、SHA-256 工具，以及已安装客户端；用户不负责选择平台或产物。
2. 从 https://github.com/zlz3907/wecom-mcp/releases/latest 解析重定向得到固定 release tag；只从该固定 tag 通过公开 HTTPS 下载 RELEASE-MANIFEST.txt、SHA256SUMS 和当前平台资产，不要求 GitHub 登录态，也不得索要、创建或复制任何 token。先校验 manifest 与资产的 SHA-256，并确认 manifest 的 version、platform 和当前 tag 一致。不得执行 latest URL 直接给出的二进制，也不得从对话历史猜测二进制路径。
3. macOS/Linux：同时下载并校验 install.sh，再调用它并使用 --client auto；默认前缀为 ~/.mcp/wecom-mcp-v2。Windows amd64：下载同一固定 tag 的 windows_amd64.zip，用 PowerShell Get-FileHash 校验后解压到 `$HOME/.mcp/wecom-mcp-v2/releases/<tag>-windows-amd64`；确认包内 INSTALL-MANIFEST.txt 的 version、platform、source_commit 及两个 exe 哈希，然后直接使用该版本目录内 `bin/wecom-mcp-v2.exe` 的绝对路径。Windows 不要求创建 `current`、符号链接或 junction，也不得因默认目录写入失败而自行改装到项目工作区。目标版本目录已存在时只允许在完整校验通过后复用，否则停止并保留现场。保留旧 releases，不覆盖未知文件。
4. 只在本机已有 zoop_wecom_zhycit.local.json 时将其传给安装器；可以探查已存在的客户端配置及其引用，但不得输出配置内容、凭据、token、secret、doc ID 或 sheet ID。找不到本地配置时仍可安装二进制，但 configured 必须为 no，且不得虚构可用服务。
5. macOS/Linux 上的 Codex 与 TRAE SOLO CN 只使用安装器内置的最小、备份后、幂等注册器。Windows 上通过客户端官方 MCP 设置入口注册已校验版本目录内 `bin/wecom-mcp-v2.exe` 的绝对路径，参数为 `--config` 和既有配置文件绝对路径；不要为了获得稳定路径而创建 `current` 链接。修改配置文件前先备份，只合并 `zoop_wecom_zhycit` 并保留其他服务。WorkBuddy 同样使用官方“设置 > MCP”入口或官方 `mcp.json` 编辑器。不要猜配置路径，不要覆盖整个配置；无法确认客户端契约时输出 agent_blocked。
6. 注册完成后提示重启对应客户端。只有在当前客户端 MCP runtime 中完成 initialize、tools/list，且真实调用一次只读 wecom_schema_status 后，才可把 loaded/verified 置为 yes。静态配置只能证明 configured；tools/list 不能代替 tools/call。若 WorkBuddy 只能完成配置写入而当前 Agent 无法访问其运行时，configured 可以是 yes，但 loaded/verified 必须保持 unknown 或 no。
7. 全程不得读取或写入企业微信业务表，不调用 wecom_record_apply、schema sync/migration、reconcile 或计划任务。

最终按以下机器可读契约输出，不要省略任何字段；安装器已经输出的字段应原样保留，路径只可使用 `$HOME/...`、`$PREFIX/...` 或 `<absolute-path>` 形式：
result=passed|agent_blocked|failed
operation=install|rollback|uninstall
release_version=<fixed tag or unknown>
platform=<detected OS/arch>
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

在公开 Release 无法访问、安装目标无权限、缺少 SHA-256 工具、未知客户端契约或平台不支持时，停止并输出 result=agent_blocked。无本地配置不是二进制安装失败：只要 Release 和包内文件校验通过且版本目录已经落盘，就输出 result=passed、installed=yes、configured=no、loaded=no、verified=no，next_action 只说明需要先提供既有 `zoop_wecom_zhycit.local.json` 的绝对路径后才能注册，不得填写不存在的占位配置路径。WorkBuddy 若没有官方 MCP 入口，就把“打开设置 > MCP，新增 `zoop_wecom_zhycit`，粘贴检测到的 command/args 后保存并重启”作为 next_action，不得猜路径。不得删除旧版本、不得伪造成功、不得索要或复制凭据、不得重试不确定的写入。
```

状态含义：`installed` 只表示受校验的本地版本目录已经落盘且 `binary_path` 指向其中的二进制；不要求存在 `current` 链接。`configured` 只表示客户端配置已安全写入；`loaded` 需要当前客户端运行时握手与工具发现；`verified` 必须有真实的只读 `wecom_schema_status` tools/call。四项不可互相推导。

测试示例：在没有本地 `*.local.json` 的临时 HOME 中，安装器应输出 `result=passed`、`installed=yes`、`configured=no`、`loaded=no`、`verified=no`。Windows 测试必须直接报告版本目录中的 exe 路径，不创建 `current`、符号链接或 junction，也不得回退到项目工作区。低层安装器遇到 WorkBuddy 未确认的本机文件契约时，应输出 `agent_blocked` 并保留现状，但官方“设置 > MCP”入口可由 Agent 引导用户完成配置。自动化覆盖见 `scripts/test-github-installer.sh`。
