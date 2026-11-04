# Ready-worker 隔离的 blue/green 合同（未部署）

本文件只定义下一阶段受控发布的边界；不会安装 unit、创建 state root、修改 Nginx、切入口或重启任何服务。当前 `mcp_release_controller.py` 仍只能管理唯一的 shared green unit。因此它对 schema v3 的 ready-worker isolation 候选**明确拒绝 deploy**，不能把该候选错误降级为 restart shared green。

## 默认与冻结基线

- 默认保持 schema v2，且不传 `--gnas-ready-worker-isolation`；现有 shared green 生产行为不变。
- 只有构建候选时显式传 `build-candidate.py --ready-worker-isolation` 才生成 schema v3 计划。该计划仅供审核/暂存，不能由当前控制器 deploy。
- manifest 和未来一次性 receipt 必须逐字绑定以下有序状态向量及其 SHA-256：

  ```json
  [
    {"host":"mcp.wesiyu.com","state":"ready"},
    {"host":"mcp.jianpinke.com","state":"tenant_unavailable"},
    {"host":"mcp.rtyouth.com","state":"ready"}
  ]
  ```

  `ready` 要求 local/public `healthz`/`readyz=200`、无 token 的 `mcp=401` 和 resource metadata=200。`tenant_unavailable` 要求所有该租户入口为精确 no-store 503，不能以全局 503 或旧授权缓存替代。

## 必须预先存在的独立拓扑

| 颜色 | systemd unit | loopback | state root | 进程参数 |
| --- | --- | --- | --- | --- |
| green | `wecom-mcp@sharedzoop.service` | `127.0.0.1:7702` | `/home/product/services/mcp/wecom/state/discovery` | 保持当前运行参数；**never restart** |
| blue | `wecom-mcp@sharedzoop-blue.service` | `127.0.0.1:7703` | `/home/product/services/mcp/wecom/state/discovery-blue` | 数据库 discovery + `--gnas-ready-worker-isolation` + 有界 worker timeout |

Blue 不能复用 green 的 unit、listener、state root、PID 或 lock 文件。Blue 必须由独立、root-owned 的安装流程预置；其 service definition、有效安全 sandbox、运行用户、环境文件引用和 state root ownership 都需要独立审查。这里没有也不会给出凭据内容。

公网入口需要一个已经预置的 root-owned **atomic loopback switch**：它只允许在 green `7702` 和 blue `7703` 两个已审计 loopback 后端间切换，并将当前颜色的只读回执写到 `/var/lib/wecom-mcp-release/entrypoint/active.json`。它不是 Nginx include，也不允许编辑 Nginx；当前仓库/控制器没有该 switch，因而没有可执行的切流命令。

## 将来的受控序列

1. 只读确认 shared green 的 PID/SHA/unit 指纹、三个 host 的冻结向量和 GNAS readiness；任一漂移停止。
2. 启动独立 blue；只对 `127.0.0.1:7703` 做完整向量探测及逐请求 introspection/撤权验证。green 全程运行且不 restart。
3. 独立 verifier 对 schema v3 manifest、向量 digest、blue unit/loopback/state-root 和一次性 receipt 复核。
4. switch 将入口回执从 `green` 原子替换为 `blue`，随后回读公开入口、blue PID/SHA 和原子回执。不得把“blue 已启动”视为切换成功。
5. 回退只允许先原子把入口恢复到 green，回读 green 连续性，再有界 drain/停止 blue；回退不得 restart green。

如果 Blue 的 worker 撤权或停止，worker 先封禁新请求；已经进入 proxy 的请求在 timeout 内 drain，正常 drain 走 child 的 graceful shutdown，只有超时才 SIGTERM。撤权仍由每请求 introspection 决定，不能长期保留旧授权。

## 当前 NO-GO

缺少受审的 blue unit、atomic loopback switch、其安装/回退控制器以及针对真实隔离入口的 staged 证据。因此 schema v3 不可部署、不可写 Binding、不可开 flag。上述拒绝是刻意的失败关闭，不是发布阻塞绕过。
