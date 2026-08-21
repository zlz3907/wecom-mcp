# Agent 安装提示词（唯一规范版本）

将以下完整提示词粘贴给 TRAE、WorkBuddy 或其他具备本机终端与 MCP 管理能力的 Agent。它不依赖对话历史中的安装路径，也不让用户选择系统或 CPU 架构。

```text
为当前用户安装并注册 wecom-mcp-v2 的固定实例 zoop_wecom_zhycit。只能以 GitHub Release 为来源：仓库 zlz3907/wecom-mcp。

按以下安全流程执行：
1. 探查本机 OS、GOARCH 等价 CPU 架构、可用 shell、curl、SHA-256 工具，以及已安装客户端；用户不负责选择平台或产物。
2. 从 https://github.com/zlz3907/wecom-mcp/releases/latest 解析重定向得到固定 release tag；只从该固定 tag 下载 install.sh、RELEASE-MANIFEST.txt 与 SHA256SUMS。先用 SHA256SUMS 校验 install.sh 和 RELEASE-MANIFEST.txt，再确认 manifest 的 version、installer、checksums 与当前 tag 一致，最后执行已校验的 install.sh。不得执行 latest URL 直接给出的二进制，也不得从对话历史猜测二进制路径。
3. 调用已校验的 install.sh，使用 --client auto。安装器必须自行按 OS/架构下载同一固定 tag 的匹配产物并校验 SHA-256；默认前缀为 ~/.mcp/wecom-mcp-v2，保留旧 releases，并原子切换 current。
4. 只在本机已有 zoop_wecom_zhycit.local.json 时将其传给安装器；可以探查已存在的客户端配置及其引用，但不得输出配置内容、凭据、token、secret、doc ID 或 sheet ID。找不到本地配置时仍可安装二进制，但 configured 必须为 no，且不得虚构可用服务。
5. 对 Codex 与 TRAE SOLO CN，只使用安装器内置的最小、备份后、幂等注册器。WorkBuddy 的 MCP 配置路径或 JSON 契约若不能由当前安装与本机证据确认，禁止猜路径或写文件，输出 agent_blocked。
6. 注册完成后提示重启对应客户端。只有在当前客户端 MCP runtime 中完成 initialize、tools/list，且真实调用一次只读 wecom_schema_status 后，才可把 loaded/verified 置为 yes。静态配置只能证明 configured；tools/list 不能代替 tools/call。
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

在权限不足、缺少 SHA-256 工具、私有 Release 无访问权、无本地配置、未知客户端契约或平台不支持时，停止并输出 result=agent_blocked；无本地配置允许仅完成二进制安装，但 configured 必须为 no。不得删除旧版本、不得伪造成功、不得重试不确定的写入。
```

状态含义：`installed` 只表示受校验的本地二进制已切换；`configured` 只表示客户端配置已安全写入；`loaded` 需要当前客户端运行时握手与工具发现；`verified` 必须有真实的只读 `wecom_schema_status` tools/call。四项不可互相推导。

测试示例：在没有本地 `*.local.json` 的临时 HOME 中，安装器应输出 `installed=yes`、`configured=no`、`loaded=no`、`verified=no`；面对 WorkBuddy 未确认的配置契约，应输出 `agent_blocked` 并保留现状。自动化覆盖见 `scripts/test-github-installer.sh`。
