# Blue/green executor（安装候选，未安装/未部署）

`mcp_blue_green_executor.py` 是唯一允许启动蓝色独立 unit、验证本地 `7703`、调用原子入口 switch 并回退的候选控制器。它与现有 `mcp_release_controller.py` 分离：后者继续拒绝 schema v3/blue-green 工作，且永远不 restart shared green。

## 固定拓扑

- green：`wecom-mcp@sharedzoop.service`，`127.0.0.1:7702`，现有 state root；只读基线，`never-restart`。
- blue：`wecom-mcp@sharedzoop-blue.service`，`127.0.0.1:7703`，`/home/product/services/mcp/wecom/state/discovery-blue`；blue drop-in 强制 `--gnas-ready-worker-isolation`、独立 state root、`KillMode=control-group`、30 秒停止界限。
- 入口：root-owned `wecom-mcp-loopback-switch` 只原子替换 `/var/lib/wecom-mcp-release/entrypoint/active.json` 中的 green/blue loopback receipt。`wecom-mcp-loopback-gateway` 是唯一的受控 receipt consumer：每一个请求都重新验证该 root-owned receipt，再只代理到 green `7702` 或 blue `7703`，并添加固定、无敏感内容的 `X-WeCom-MCP-Loopback-Color` response header。executor 必须在公开向量中读到该颜色，因而旧的直连 route 不能伪造切换成功。它监听候选 `127.0.0.1:7704`；`7701` 已被现有服务占用，绝不复用。它不读写 Nginx。

blue executor 在启动 blue 后先验证冻结向量 `[ready, tenant_unavailable, ready]` 的 **7703 本地**合同；只有成功后才调用 switch。切换后对每个 host 的 `healthz`、`readyz`、`mcp`、resource metadata（包括 unavailable 503 路由）逐项验证状态和 gateway color header，不能只凭 `/healthz` 代表真实 MCP 路由。失败时先切回 green、读回 green 公开向量和原 green 基线，再停止 blue；任一 green 回读失败时 blue 保持运行并显式失败，不能把 receipt 文件本身当作切回成功。正常 rollback 在同样的 green 验证、blue stop 后删除 managed blue drop-in 并 daemon-reload，确保下一次 deploy 不会撞到旧 override。若失败发生在 blue start 之前，事务会恢复 blue drop-in 并 daemon-reload，不触碰 green/blue 进程。

## 安装前硬门槛

这份仓库内容本身不授权安装。管理员必须另行完成并回读：

1. root-owned executor、switcher、blue unit、gateway unit/gateway 程序的同提交 SHA；candidate manifest 精确绑定这些 SHA、release-specific drop-in SHA，以及 ACTIVE parent 的固定 root-owned 路径/权限策略摘要。manifest 和一次性 approval receipt 还逐字绑定 `git_commit`、`git_tree` 与 exact `ci_provenance`（run URL + 同一 commit/tree）；没有 exact CI 的本地构建只能 `ci_pending`，不能 verify/deploy。安装时 executor 会重新读取 systemd FragmentPath/DropInPaths 和每个文件 SHA，不能以名字或 unit ActiveState 代替。
2. blue 的独立 cgroup、`KillMode=control-group`、7703 listener、state root ownership 和系统级 parent-stop 行为。
3. gateway `127.0.0.1:7704` 已被预留为 green/blue 唯一可选后端；其公开入口 readback 能证明确实随 receipt 原子变化。若既有 public route 仍直连 `7702`，这条门槛不成立；executor 不会也不能修改 Nginx 来补救。
4. 新 schema v1 blue candidate、同 commit CI、一次性精确 receipt、GNAS readiness、三租户向量和撤权读回。

受控 gateway 已有离线 request-by-request receipt-consumer 回归（green→blue 的两个独立后端和失败关闭），但当前实际 unit/7704 listener/public route receipt consumption 仍全部 **NOT VERIFIED**。因此不允许上传/安装 executor、启动 blue、调用 switch、切流、开启 flag 或写 Binding。离线单元测试中的模拟 `systemctl`/switch 只验证顺序和失败回退，不是生产运行证据。
