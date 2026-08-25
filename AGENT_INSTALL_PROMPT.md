# Agent 安装提示词（Public 版本，唯一规范版本）

将以下完整提示词粘贴给 TRAE、WorkBuddy 或其他具备本机终端与 MCP 管理能力的 Agent。它不依赖对话历史中的安装路径，也不让用户选择系统或 CPU 架构。

```text
为当前用户安装并注册 wecom-mcp-v2 的固定实例 zoop_wecom_zhycit。只能以 GitHub Release 为来源：仓库 zlz3907/wecom-mcp。

按以下安全流程执行：
1. 探查本机 OS、GOARCH 等价 CPU 架构、可用 shell、curl、SHA-256 工具、当前宿主客户端及其真实授权工作空间；用户不负责选择平台、产物或安装前缀。必须区分 TRAE Work CN、WorkBuddy 与普通独立终端，不能跨客户端复用安装或配置路径。
2. 从 https://github.com/zlz3907/wecom-mcp/releases/latest 解析重定向得到固定 release tag；只从该固定 tag 通过公开 HTTPS 下载 RELEASE-MANIFEST.txt、SHA256SUMS、当前平台安装器和资产，不要求 GitHub 登录态，也不得索要、创建或复制任何 token。先用 SHA256SUMS 校验 manifest、安装器与资产，并确认 manifest 的 version、platform 和当前 tag 一致。不得执行未经校验的安装器或 latest URL 直接给出的二进制，也不得从对话历史猜测二进制路径。
3. macOS/Linux：下载并校验 install.sh，再调用它并使用 --client auto；默认前缀为 ~/.mcp/wecom-mcp-v2。Windows amd64：下载并用 PowerShell Get-FileHash 校验同一固定 tag 的 `install.ps1`，Windows 安装、外层与包内 SHA-256 校验、版本目录解压和重复安装检查全部交给该脚本，不要由 Agent 临时拼装命令。按宿主调用：TRAE Work CN 使用 `powershell -NoProfile -ExecutionPolicy Bypass -File <verified-install.ps1> -Version <fixed-tag> -Client trae-work-cn -Workspace <absolute-authorized-workspace>`，安装到官方项目级 `.trae` 配置范围内的 `<workspace>/.trae/mcp-servers/wecom-mcp-v2/releases/<tag>-windows-amd64`；WorkBuddy 使用 `... -Client workbuddy`，安装到官方用户级 `.codebuddy` 配置范围内的 `$HOME/.codebuddy/mcp-servers/wecom-mcp-v2/releases/<tag>-windows-amd64`，遇到官方权限确认时展示准确目标并等待用户批准；其他普通终端使用 `... -Client standalone`，安装到 `$HOME/.mcp/wecom-mcp-v2/releases/<tag>-windows-amd64`。`mcp-servers/wecom-mcp-v2` 是本项目定义的二进制子目录，不冒充客户端官方内置目录。所有模式都不创建 `current`、符号链接或 junction，保留旧 releases 且不覆盖未知文件。
4. 只在本机已有 zoop_wecom_zhycit.local.json 时将其传给安装器；可以探查已存在的客户端配置及其引用，但不得输出配置内容、凭据、token、secret、doc ID 或 sheet ID。找不到本地配置时仍可安装二进制，但 configured 必须为 no，且不得虚构可用服务。
5. macOS/Linux 上的 Codex 与 TRAE SOLO CN 只使用安装器内置的最小、备份后、幂等注册器。Windows 上只注册安装器输出的 `binary_path`，参数为 `--config` 和既有配置文件绝对路径，不创建 `current`。TRAE Work CN 使用官方“设置 > MCP”入口并合并项目级 `<workspace>/.trae/mcp.json`，配置项使用 `command` 与 `args`；WorkBuddy 使用官方 MCP 管理入口并合并用户级 `$HOME/.codebuddy/.mcp.json`，配置项明确使用 `type: stdio`、`command` 与 `args`。修改配置前先备份，只合并 `zoop_wecom_zhycit` 并保留其他服务；不要猜其它配置路径，不要覆盖整个配置。无法确认客户端契约时输出 agent_blocked。
6. 注册完成后提示重启对应客户端。只有在当前客户端 MCP runtime 中完成 initialize、tools/list，且真实调用一次只读 wecom_schema_status 后，才可把 loaded/verified 置为 yes。静态配置只能证明 configured；tools/list 不能代替 tools/call。若 WorkBuddy 只能完成配置写入而当前 Agent 无法访问其运行时，configured 可以是 yes，但 loaded/verified 必须保持 unknown 或 no。
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

在公开 Release 无法访问、对应客户端的受管目标无权限、缺少 SHA-256 工具、未知客户端契约或平台不支持时，停止并输出 result=agent_blocked。TRAE Work CN 的授权项目级 `.trae` 路径不是“回退到项目工作区”，而是该客户端的正式 project scope；不得改写到项目根目录的其它影子 `.mcp` 或 `.runtime`。无本地配置不是二进制安装失败：只要 Release 和包内文件校验通过且客户端范围内的版本目录已经落盘，就输出 result=passed、installed=yes、configured=no、loaded=no、verified=no，next_action 只说明需要先提供既有 `zoop_wecom_zhycit.local.json` 的绝对路径后才能注册，不得填写不存在的占位配置路径。不得删除旧版本、不得伪造成功、不得索要或复制凭据、不得重试不确定的写入。
```

状态含义：`installed` 只表示受校验的本地版本目录已经落盘且 `binary_path` 指向其中的二进制；不要求存在 `current` 链接。`configured` 只表示客户端配置已安全写入；`loaded` 需要当前客户端运行时握手与工具发现；`verified` 必须有真实的只读 `wecom_schema_status` tools/call。四项不可互相推导。

测试示例：在没有本地 `*.local.json` 时，三个 Windows 模式都应输出 `result=passed`、`installed=yes`、`configured=no`、`loaded=no`、`verified=no`。TRAE Work CN 的 `binary_path` 必须位于所传工作空间的 `.trae/mcp-servers` 下；WorkBuddy 必须位于用户 `.codebuddy/mcp-servers` 下；standalone 才使用用户 `.mcp`。三个模式都不得创建 `current`、符号链接或 junction。自动化覆盖见 `scripts/test-windows-installer.ps1`。
