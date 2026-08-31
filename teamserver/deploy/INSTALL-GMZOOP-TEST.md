# gmzoop WorkBuddy Connector API Key 测试安装

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

```sh
sudo systemctl start wecom-mcp@gmzoop.service
sudo systemctl --no-pager --full status wecom-mcp@gmzoop.service
sudo journalctl -u wecom-mcp@gmzoop.service -n 100 --no-pager
curl -fsS http://127.0.0.1:7702/healthz
curl -fsS http://127.0.0.1:7702/readyz
```

确认回环端点通过后，再复核已有 Nginx 代理配置：`sudo nginx -t`，然后从外部读取 `https://mcp.jyiai.com/gmzoop/healthz` 与 `/readyz`。首次 WorkBuddy 验收只调用 `initialize`、`tools/list` 和一个只读工具；API Key 模式不允许写工具。
