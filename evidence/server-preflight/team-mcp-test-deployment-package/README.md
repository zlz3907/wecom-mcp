# mcp.jyiai.com 测试部署包（review-only）

本目录只生成待审配置，不执行服务器变更。目标为 Ubuntu 22.04.5 amd64、原生静态二进制、systemd 和现有 Nginx。

## 固定事实

- A=`47.104.67.114`，无 AAAA，指向目标服务器。
- `mcp.jyiai.com` 此前落入默认 TLS vhost，因而返回已过期的错域 `*.caeri.com.cn` 证书；该证书禁止使用。
- 服务器已有 `/root/nginx/cert/jyiai.com.pem` 与 `/root/nginx/cert/jyiai.com.key`；证书 CN=`*.jyiai.com`，SAN 含 `*.jyiai.com` 与 `jyiai.com`，有效期至 2027-01-02，可覆盖 `mcp.jyiai.com`。
- `127.0.0.1:7701` 已由 ImgToWebpToOSS 使用，且现有站点 `/iwto/` 正在反代该端口；MCP 禁止占用。
- 不得关闭 TLS 校验，不得使用 `curl -k/--insecure`。
- 不得覆盖现有 Nginx 站点，不得安装 Docker。

## TLS 固定方案

复用现有 `jyiai.com` 通配证书。部署窗口启用前仍须只读回读并核对证书/私钥匹配；不得读取或输出私钥内容：

```bash
openssl x509 -in /root/nginx/cert/jyiai.com.pem -noout -subject -issuer -dates -ext subjectAltName
openssl x509 -in /root/nginx/cert/jyiai.com.pem -pubkey -noout | openssl pkey -pubin -outform DER | sha256sum
openssl pkey -in /root/nginx/cert/jyiai.com.key -pubout -outform DER | sha256sum
```

两个公钥摘要必须相同；SAN、有效期和信任链必须仍有效。`nginx-mcp.jyiai.com-gmzoop.conf` 已引用上述路径，不再包含 ACME 或证书占位方案。

## 固定执行顺序

1. 保存 `nginx -T` 基线和当前 systemd 状态。
2. 校验发行归档和包内 `SHA256SUMS`，确认二进制为 amd64 静态 ELF。
3. 建立 `wecom-mcp-gmzoop` 无登录用户；只能读取 gmzoop 的 env/config/data。共享程序放入 `/home/product/services/mcp/wecom/releases/<version>/`，`current` 原子链接到选定版本；实例仅放入 `/home/product/services/mcp/wecom/instances/gmzoop/{config,data}`。
4. 将 Secret 注入 `/etc/wecom-mcp/gmzoop.env`（root:root、0600，禁止放项目目录），审阅并安装通用 `wecom-mcp@.service`，本轮只启动 `wecom-mcp@gmzoop.service`；先验证回环 healthz/readyz，日志使用 journald。
5. 只读复核现有证书后，新建 `/etc/nginx/sites-available/wecom-mcp-team-test`，不得编辑既有文件。
6. `nginx -t` 成功后才新增 sites-enabled 链接和 reload。
7. 严格验证 TLS、OAuth、initialize、角色化 tools/list、只读 tools/call、WorkBuddy。
8. 失败按 `rollback.sh.review-only` 回滚；脚本默认不执行。

## 唯一目录基线

```text
/home/product/services/mcp/wecom/
├── releases/<version>/wecom-mcp-team
├── current -> releases/<version>
└── instances/gmzoop/
    ├── config/instance.json
    └── data/

/etc/wecom-mcp/gmzoop.env  # root:root 0600
```

`releases` 与 `current` 由 root 管理并对服务只读；`instances/gmzoop/config` 对 `wecom-mcp-gmzoop` 只读；仅 `instances/gmzoop/data` 可写。Secret 不得复制到 `/home/product/services/mcp/wecom`、项目仓库或任何镜像构建上下文。服务 stdout/stderr 进入 journald，排障使用 `journalctl -u wecom-mcp@gmzoop`。

## 当前状态

`installed=no`、`configured=no`、`https_reachable=no`、`oauth_verified=no`、`loaded=no`、`workbuddy_verified=no`、`production_deployed=no`。

## 固定租户路由

| 租户 | 外部 MCP URL | 回环后端 | 前缀处理 |
| --- | --- | --- | --- |
| gmzoop | `https://mcp.jyiai.com/gmzoop/mcp` | `127.0.0.1:7702/mcp` | strip `/gmzoop` |

health、ready 与 OAuth metadata 只开放 gmzoop 的精确前缀路径；其他路径返回 404。`zhyczoop` 及其他实例仅作为未来扩展规则，本包不包含其 upstream、env、systemd 实例或端口占用。完整映射和发布物兼容性检查见 `ROUTING.md`。
