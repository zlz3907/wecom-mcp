# wecom-mcp-v2

这是一个连接**企业微信智能表格**的 MCP 服务。

安装后，Codex 或 TRAE 可以用自然语言查询 Zoop 的需求、任务等记录，也可以在明确指令下受控维护记录。它适合已经使用 Zoop 管理研发工作，希望直接在 AI 客户端里查看和更新企业微信智能表格的个人或团队。

## 30 秒 Quick Start

从源码本地构建：

```sh
git clone git@github.com:zlz3907/wecom-mcp.git
cd wecom-mcp
go build -o bin/wecom-mcp-v2 ./cmd/wecom-mcp-v2
cp config/zoop_wecom_zhycit.json.example config/zoop_wecom_zhycit.local.json
```

需要 Go 1.23 或更高版本。复制出的示例配置不能直接运行，请先编辑 `config/zoop_wecom_zhycit.local.json`：

- 将 `registry_document_id` 填为已有登记表的文档 ID；留空时只能先由 MCP 客户端显式调用 `wecom_registry_bootstrap` 创建并写回登记表，普通查询不能使用空值。
- 将 `schema_mirror_path` 改为已有 Schema 镜像文件的绝对路径。
- 将 `state_path` 改为本机可写位置的绝对路径。

再通过运行环境提供 `GNAS_BASE_URL`、`GNAS_APP_ID` 和 `GNAS_APP_SECRET`，不要把凭据写入仓库。可以用下面的只读调用检查二进制、环境变量和配置是否能正常加载：

```sh
printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"wecom_schema_status","arguments":{}}}' | \
  ./bin/wecom-mcp-v2 --config "$PWD/config/zoop_wecom_zhycit.local.json"
```

返回包含 `result` 且没有 `isError` 后，把下面的 stdio 命令注册到 Codex 或 TRAE；请将 `/path/to/wecom-mcp` 替换为仓库的真实绝对路径：

```text
/path/to/wecom-mcp/bin/wecom-mcp-v2 --config /path/to/wecom-mcp/config/zoop_wecom_zhycit.local.json
```

更完整的本地安装、校验和回滚方法见 [便携安装说明](PORTABLE_INSTALL.md)。

## 安装后怎么用

重新加载客户端中的 MCP 后，可以先从只读问题开始：

- “查看当前企业微信 Schema 的状态。”
- “读取 Zoop 需求表的前 10 条记录。”
- “读取 Zoop 任务表的前 20 条记录，并帮我概括当前进展。”
- “列出 Zoop 协作主体表中的记录。”
- “告诉我这个 MCP 当前有哪些可用工具，不要执行写入。”

实际可见内容取决于本机配置和企业微信权限。需要修改记录时，请明确说明目标和改动内容，并先确认客户端展示的操作。

## 支持范围

- 系统：macOS、Linux。
- 客户端：Codex、TRAE。
- WorkBuddy：当前未支持。
- Windows：当前未验证。

本服务使用 stdio JSON-RPC，每个进程连接一个由本机配置固定的企业微信实例。

## 更多文档

- [便携安装、校验与回滚](PORTABLE_INSTALL.md)
- [字段兼容性](FIELD_CODEC_COMPATIBILITY.md)
- [旧接口支持范围](LEGACY_API_PORT_MATRIX.md)
- [配置示例](config/zoop_wecom_zhycit.json.example)
- [项目补充资料](evidence/README.md)

工具、Schema 管理和字段处理的详细说明，请按需要查看上述文档和源码。

许可证状态见 [LICENSE-CANDIDATE.md](LICENSE-CANDIDATE.md)。
