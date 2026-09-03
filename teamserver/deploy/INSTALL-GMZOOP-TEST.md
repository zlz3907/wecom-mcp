# gmzoop 测试安装与 OAuth 隔离验收

本包仅适用于 Linux amd64 测试机。它不包含 GNAS Secret、企业微信实例配置或 Schema 镜像；这些值不能从 example 推断或重建。

## 1. 校验并解包

在服务器的临时目录执行：

```sh
sha256sum wecom-mcp-team_gmzoop-test_<revision>_linux_amd64.tar.gz
tar -xzf wecom-mcp-team_gmzoop-test_<revision>_linux_amd64.tar.gz
cd wecom-mcp-team-gmzoop-<revision>
sha256sum -c SHA256SUMS
```

## 2. 安装不可变程序和 systemd 单元

```sh
sudo useradd --system --user-group --home-dir /nonexistent --shell /usr/sbin/nologin wecom-mcp-gmzoop 2>/dev/null || true
sudo install -d -o root -g root -m 0755 /home/product/services/mcp/wecom/releases/<revision>
sudo install -d -o root -g wecom-mcp-gmzoop -m 0750 /home/product/services/mcp/wecom/instances/gmzoop/config
sudo install -d -o wecom-mcp-gmzoop -g wecom-mcp-gmzoop -m 0700 /home/product/services/mcp/wecom/instances/gmzoop/data
sudo install -o root -g root -m 0755 wecom-mcp-team /home/product/services/mcp/wecom/releases/<revision>/wecom-mcp-team
sudo install -o root -g root -m 0755 wecom-mcp-instance-init /home/product/services/mcp/wecom/releases/<revision>/wecom-mcp-instance-init
sudo ln -s /home/product/services/mcp/wecom/releases/<revision> /home/product/services/mcp/wecom/current.next
sudo mv -Tf /home/product/services/mcp/wecom/current.next /home/product/services/mcp/wecom/current
sudo install -o root -g root -m 0644 wecom-mcp@.service /etc/systemd/system/wecom-mcp@.service
sudo systemctl daemon-reload
```

`<revision>` 必须替换为当前交付包目录名中的 revision。切换 `current` 只替换程序版本，不覆盖实例配置或状态。

## 3. 在服务器生成 Connector Key

```sh
sudo ./create-gmzoop-env.sh /etc/wecom-mcp/gmzoop.env
sudoedit /etc/wecom-mcp/gmzoop.env
```

第二条命令只填写由服务器 Secret 管理提供的 `GNAS_APP_ID` 和 `GNAS_APP_SECRET`。不得把它们写回 Git、压缩包、WorkBuddy 配置或聊天。

从服务器受保护终端读取 `TEAM_MCP_CONNECTOR_API_KEY`，在 WorkBuddy 企业后台创建自定义连接器：认证方式选 API Key，Header Name 填 `Authorization`，Header Value 填 `Bearer <Connector Key>`，MCP Server URL 填 `https://mcp.jyiai.com/gmzoop/mcp`。

## 4. 投递固定实例配置和 Schema

服务器必须已有以下受保护文件，且内容属于 `zoop_wecom_gm` 当前实例：

```text
/home/product/services/mcp/wecom/instances/gmzoop/config/instance.json
/home/product/services/mcp/wecom/instances/gmzoop/config/schema-mirror.json
```

实例配置的 `schema_admin_user` 必须为 `wecom-mcp-gmzoop`，`schema_mirror_path` 与 `state_path` 必须指向服务器的 gmzoop 路径。不要复制其他租户配置、Schema 或文档 ID。

## 5. 启动与只读验收

若 gmzoop 尚无既有 Registry 与九表，先由服务器本地受保护账号执行一次 dry-run；只在它输出可执行 preview 后才加 `--apply`。该命令不开放 HTTP 端口，也不使用 WorkBuddy 的共享 API Key：

```sh
sudo -u wecom-mcp-gmzoop /home/product/services/mcp/wecom/current/wecom-mcp-instance-init \
  --config /home/product/services/mcp/wecom/instances/gmzoop/config/instance.json

sudo -u wecom-mcp-gmzoop /home/product/services/mcp/wecom/current/wecom-mcp-instance-init \
  --config /home/product/services/mcp/wecom/instances/gmzoop/config/instance.json --apply
```

初始化器会创建或恢复固定的 Registry 与九张 Zoop 表、回读字段、生成本地 Schema 并原子写回实例配置。任何不确定创建状态都会保留 journal 并停止，不能重复执行 `--apply` 另建一套表。

## 6. 启动与全能力目录验收

```sh
sudo systemctl start wecom-mcp@gmzoop.service
sudo systemctl --no-pager --full status wecom-mcp@gmzoop.service
sudo journalctl -u wecom-mcp@gmzoop.service -n 100 --no-pager
curl -fsS http://127.0.0.1:7702/healthz
curl -fsS http://127.0.0.1:7702/readyz
```

确认回环端点通过后，再复核已有 Nginx 代理配置：`sudo nginx -t`，然后从外部读取 `https://mcp.jyiai.com/gmzoop/healthz` 与 `/readyz`。先调用 `initialize` 和 `tools/list`，确认 `TEAM_MCP_CONNECTOR_ROLE=admin` 时当前二进制实现的全部工具均可发现；目录验收不得顺带执行真实写入。随后再按单项业务用例执行带幂等键、写后回读和审计核验的受控写入烟测。

## 7. OAuth 候选必须另开隔离入口

不要在现有 Connector API Key 服务上原地切换。安装包已包含 `gmzoop.oauth21.env.example` 和只读的 `oauth21-contract-check`；OAuth 候选使用独立回环端口、独立 systemd 实例名和独立公网测试路径。先安装该 env 示例的受保护副本，再由 GNAS 管理员提供资源服务器 client id/secret、租户键及已审批的客户端预注册或 CIMD 精确白名单。DCR 保持关闭。

候选启动后先运行不带 Token 的只读合同检查，确认 GNAS metadata、gmzoop protected-resource metadata 和 401 challenge 一致；再由测试员工完成一次企业微信扫码，把取得的短期访问令牌写入 0600 临时文件，仅追加 `-token-file` 验证 `initialize + tools/list`。检查器不调用业务工具、不输出令牌。验证结束立即删除临时令牌文件并撤销测试授权。

只有以下结果全部独立通过，才可另行申请生产启用：PKCE S256、精确 redirect URI、本人身份映射、按员工 `effective_tools` 过滤、撤权下一请求生效、审计不含 userid/Token/Secret、旧 Connector 模式回归无变化。测试通过本身不授权切换生产入口。
