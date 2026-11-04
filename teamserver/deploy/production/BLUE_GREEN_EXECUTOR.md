# Blue/green executor（安装候选，未安装/未部署）

`mcp_blue_green_executor.py` 是唯一允许启动蓝色独立 unit、验证本地 `7703`、调用原子入口 switch 并回退的候选控制器。它与现有 `mcp_release_controller.py` 分离：后者继续拒绝 schema v3/blue-green 工作，且永远不 restart shared green。

## 固定拓扑

- green：`wecom-mcp@sharedzoop.service`，`127.0.0.1:7702`，现有 state root；只读基线，`never-restart`。
- blue：`wecom-mcp@sharedzoop-blue.service`，`127.0.0.1:7703`，`/home/product/services/mcp/wecom/state/discovery-blue`；blue drop-in 强制 `--gnas-ready-worker-isolation`、独立 state root、`KillMode=control-group`、30 秒停止界限。
- 入口：root-owned `wecom-mcp-loopback-switch` 只原子替换 `/var/lib/wecom-mcp-release/entrypoint/active.json` 中的 green/blue loopback receipt。它不读写 Nginx。

blue executor 在启动 blue 后先验证冻结向量 `[ready, tenant_unavailable, ready]` 的 **7703 本地**合同；只有成功后才调用 switch。切换后验证所有公开入口（包括 resource metadata）。失败时先切回 green、读回 green 公开向量，再停止 blue；green 不 restart。

## 安装前硬门槛

这份仓库内容本身不授权安装。管理员必须另行完成并回读：

1. root-owned executor、switcher、blue unit 和 gateway 的同提交 SHA；gateway 已经静态消费 active receipt，且 Nginx 不需要再改。
2. blue 的独立 cgroup、`KillMode=control-group`、7703 listener、state root ownership 和系统级 parent-stop 行为。
3. gateway `127.0.0.1:7701` 已是 green/blue 唯一可选后端；其公开入口 readback 能证明确实随 receipt 原子变化。
4. 新 schema v1 blue candidate、同 commit CI、一次性精确 receipt、GNAS readiness、三租户向量和撤权读回。

这些事实现在全部 **NOT VERIFIED**。因此不允许上传/安装 executor、启动 blue、调用 switch、切流、开启 flag 或写 Binding。离线单元测试中的模拟 `systemctl`/switch 只验证顺序和失败回退，不是生产运行证据。
