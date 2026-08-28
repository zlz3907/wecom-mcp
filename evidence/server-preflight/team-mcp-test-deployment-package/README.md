# mcp.jyiai.com 测试部署包（review-only）

本目录只生成待审配置，不执行服务器变更。目标为 Ubuntu 22.04.5 amd64、原生静态二进制、systemd 和现有 Nginx。

## 固定事实

- A=`47.104.67.114`，无 AAAA，指向目标服务器。
- 当前 HTTPS 证书为已过期的 `*.caeri.com.cn`，SAN 不包含 `jyiai.com`；禁止使用。
- 不得关闭 TLS 校验，不得使用 `curl -k/--insecure`。
- 不得覆盖现有 Nginx 站点，不得安装 Docker。

## TLS 二选一

### A. 复用 Owner 已有 jyiai.com 证书

Owner 授权并仅提供服务器路径。启用前必须回读：

```bash
openssl x509 -in <FULLCHAIN_PATH> -noout -subject -issuer -dates -ext subjectAltName
openssl x509 -in <FULLCHAIN_PATH> -pubkey -noout | openssl pkey -pubin -outform DER | sha256sum
openssl pkey -in <PRIVATE_KEY_PATH> -pubout -outform DER | sha256sum
```

两个公钥摘要必须相同；SAN 必须包含 `mcp.jyiai.com` 或合法 `*.jyiai.com`，证书必须未过期且链受信任。然后把 `nginx-mcp.jyiai.com-multitenant.conf.template` 中两个路径占位符替换成服务器路径。

### B. Owner 授权 ACME/Let's Encrypt

先使用 `nginx-mcp.jyiai.com-acme-http.conf.template`，只开放独立 HTTP-01 webroot。`nginx -t` 通过后才 reload。随后由运维使用受控 ACME 客户端执行 webroot `certonly`，禁止 ACME 工具自动编辑 Nginx 站点。证书签发并回读 SAN/有效期后，再启用 TLS 模板。

## 固定执行顺序

1. 保存 `nginx -T` 基线和当前 systemd 状态。
2. 校验发行归档和包内 `SHA256SUMS`，确认二进制为 amd64 静态 ELF。
3. 分别建立 `wecom-mcp-gmzoop`、`wecom-mcp-zhyczoop` 两个无登录用户；各自只能读取本租户 env/config/state。建立不可变 release 目录和分租户状态目录。
4. 审阅并安装 `wecom-mcp-team@.service`，分别加载 `gmzoop.env` 与 `zhyczoop.env`；先验证回环 healthz/readyz。
5. 经 Owner 选择 TLS 方案后，新建 `/etc/nginx/sites-available/wecom-mcp-team-test`，不得编辑既有文件。
6. `nginx -t` 成功后才新增 sites-enabled 链接和 reload。
7. 严格验证 TLS、OAuth、initialize、角色化 tools/list、只读 tools/call、WorkBuddy。
8. 失败按 `rollback.sh.review-only` 回滚；脚本默认不执行。

## 当前状态

`installed=no`、`configured=no`、`https_reachable=no`、`oauth_verified=no`、`loaded=no`、`workbuddy_verified=no`、`production_deployed=no`。

## 固定租户路由

| 租户 | 外部 MCP URL | 回环后端 | 前缀处理 |
| --- | --- | --- | --- |
| gmzoop | `https://mcp.jyiai.com/gmzoop/mcp` | `127.0.0.1:7701/mcp` | strip `/gmzoop` |
| zhyczoop | `https://mcp.jyiai.com/zhyczoop/mcp` | `127.0.0.1:7702/mcp` | strip `/zhyczoop` |

health、ready 与 OAuth metadata 同样只开放各租户的精确前缀路径；其他租户子路径返回 404。完整映射和发布物兼容性检查见 `ROUTING.md`。
