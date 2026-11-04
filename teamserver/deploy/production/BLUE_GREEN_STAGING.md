# Blue/green 无流量 staging（候选，未执行）

`prepare-blue-green-staging.py` 提供两个受控动作：

- `--check`：root-only 只读门槛。读取 green 基线、blue/gateway 的 inactive 状态、工件 SHA、state/receipt 目标和 `MemAvailable`；只校验 `/etc/wecom-mcp/gmzoop.env` 的路径/所有权，**绝不读取、复制或打印**它的内容或任何凭据。
- `--apply-inert <approval-id>`：仅在精确 root-owned approval 后写入静态 executor/switch/gateway/unit 文件，创建 blue state root 与 receipt parent。它明确不执行 `daemon-reload`、不启动 blue/gateway、不创建 active receipt、不写 Nginx/Binding/数据库，故没有公网或 MCP 流量。

执行前需满足：green 基线稳定、blue/gateway inactive、blue drop-in 不存在、source/target SHA 一致、`MemAvailable >= 256MiB`。blue unit 本身固定为 `MemoryHigh=96M`、`MemoryMax=128M`、`CPUQuota=20%`，内存上限不是预分配；不满足资源门槛或出现 `memory.events` 的 `oom`/`oom_kill`，只停止 blue，绝不改 green。

已完成的一次独立、无入口流量的 60 秒 transient 样本只能作为容量下界：blue 的 `memory.current` 约 20MiB、`memory.events` 的 `oom`/`oom_kill` 均为 0，三企业四路径与冻结向量一致，结束后 7703 无监听且 cgroup 已释放。它**不**验证真实 Token、工具调用、并发或峰值，因此不能被解释为生产容量验收。

`--apply-inert` 后仍不能切入口、写 Binding 或启动服务。后续顺序是独立 readback → 单独受控 128MiB loopback sample → 无流量 blue local vector → 明确批准的入口/三企业/撤权验证；任何一步失败均保持入口 green、Binding 不写。
