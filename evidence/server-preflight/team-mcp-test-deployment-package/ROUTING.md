# 固定租户路由核对

## 真实后端端点

当前 teamserver 的 HTTP mux 只有：

| 后端路径 | 方法/用途 |
| --- | --- |
| `/mcp` | Streamable HTTP MCP；POST 为主要协议入口 |
| `/.well-known/oauth-protected-resource` | OAuth protected-resource metadata |
| `/healthz` | 无认证存活检查 |
| `/readyz` | 无认证结构就绪检查 |

后端没有 `/gmzoop` 或 `/zhyczoop` 路由。

## 公网映射与前缀结论

`/gmzoop` 定义为当前唯一实例的基础前缀，不是后端 endpoint。Nginx 采用精确 location，并在转发时 **strip 租户前缀**：

| 租户 | 公网 URL | 后端 |
| --- | --- | --- |
| gmzoop MCP | `https://mcp.jyiai.com/gmzoop/mcp` | `127.0.0.1:7702/mcp` |
| gmzoop health | `https://mcp.jyiai.com/gmzoop/healthz` | `127.0.0.1:7702/healthz` |
| gmzoop ready | `https://mcp.jyiai.com/gmzoop/readyz` | `127.0.0.1:7702/readyz` |
| gmzoop metadata（Owner 固定入口） | `https://mcp.jyiai.com/gmzoop/.well-known/oauth-protected-resource` | `127.0.0.1:7702/.well-known/oauth-protected-resource` |
| gmzoop metadata（RFC 9728 发现入口） | `https://mcp.jyiai.com/.well-known/oauth-protected-resource/gmzoop/mcp` | `127.0.0.1:7702/.well-known/oauth-protected-resource` |

所有其他 `/gmzoop/*` 和根路径返回 404，防止错误路由。`127.0.0.1:7701` 已由 ImgToWebpToOSS 占用，禁止 MCP 使用。本轮不配置、不启动 `zhyczoop`，不占用 `7703`。未来新增实例必须另行审批后再从 `7703–7709` 选择未占用端口，并新增独立 upstream、精确路径、env、systemd 实例、日志和保护区；通用模板本身不预创建任何实例。

## 发布物兼容性门槛

旧构建 `4d1d95d` 明确拒绝 `TEAM_MCP_PUBLIC_URL` 含路径前缀，不能与本配置配套启用。当前隔离 worktree 已实现通用、受控单段 base path：允许 `gmzoop` 这类只含字母、数字、下划线或连字符的单段实例名，拒绝嵌套路径、百分号编码和歧义路径；MCP resource 保留公网前缀，OAuth metadata 使用 RFC 9728 发现路径，后端 mux 仍保持根路径。Owner 指定的租户内 metadata 路径作为同内容兼容入口保留。

必须使用该变更完成合并、发布并通过启动与 metadata 精确回读的新构建后，才能启用本 Nginx 配置。不得用 Nginx `sub_filter`、伪造 metadata 或覆盖 `WWW-Authenticate` 绕过旧构建限制。
