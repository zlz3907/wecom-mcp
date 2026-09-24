# 单一共享服务改名

将 `wecom-mcp@gmzoop.service` 迁移为 `wecom-mcp@sharedzoop.service`，同时使用支持动态租户未就绪隔离的新版本。不是别名，不并行运行第二个 MCP 进程。用户已选择此名称并授权切换；签发机器 receipt 时引用该决定，不再要求重复选择名称。

现有逻辑环境 ID `zhycit-prod-01/wecom-mcp-gmzoop` 保留，以保持发布审计连续。Linux 用户/组 `wecom-mcp-gmzoop`、`/etc/wecom-mcp/gmzoop.env`、原 runtime/config/data、discovery state 保留。新 unit 是独立实例文件，保留源 unit 的安全和资源约束；迁移时逐项比较实际 effective properties，只有 state 的可写路径是已声明的新增项。Nginx、GNAS、数据库和业务资产不修改。

## 候选与前置

1. 新版本必须包含未就绪租户隔离。国脉为必须健康的静态租户；尖品客、劲得可以返回精确的租户未就绪 503，必须同时满足 `Cache-Control: no-store`、无鉴权挑战和固定响应体。整个 fleet 故障的 503 不能通过。允许未就绪不代表这些租户可在客户端使用。
2. 独立 Verifier、Reviewer、同 commit CI 通过。`build-candidate.py` 的 `expected-*` 除 `expected-unmanaged-unit-fingerprint` 外取源服务新鲜事实；该字段取新独立 unit 的目标指纹（迁移器的 `target_fingerprint()`）。保持旧实际 binary 路径/SHA 为源基线，不能虚构新 unit 已运行。
3. 迁移工具目录由管理员安装为 root-owned、非组/其它用户可写，包含同 commit 的 `migrate-shared-service.py`、`mcp_release_controller.py` 和 `wecom-mcp@sharedzoop.service`。不可先用普通 `upgrade-controller.py` 安装新 controller，否则会先把控制器指向尚不存在的目标。
4. 候选上传至既有 incoming 后，迁移器 `stage RELEASE` 在同一个 release.lock 下以显式 source context 校验并暂存不可变候选。不会启动服务或改名。
5. `preflight RELEASE` 验证源/目标状态、两个新 binary 模式的只读 check-config，并复核源基线未变。会创建两个临时 oneshot，不监听，不初始化，不输出上游内容。
6. GNAS 独立流程提供有效的 `gnas-discovery-readiness.json`，现有 README 的精确、只读、一次性管理员 receipt 规则仍有效；不能代填合同验证成功。

## 签发与执行

```sh
python3 -I migrate-shared-service.py check RELEASE
python3 -I migrate-shared-service.py preflight RELEASE
python3 -I migrate-shared-service.py apply RELEASE ISSUED_APPROVAL_ID
/usr/local/sbin/wecom-mcp-release-controller observe RELEASE
```

`check` 输出 `required_approval_fields`。receipt 的 action 为 `migrate-shared-service`，所有这些字段必须逐一完全匹配，并添加既有 README 的 schema/environment/approval_id/approved_by/approved_at/expires_at。字段绑定候选/manifest/recovery、源 PID/path/SHA/unit/config、安全资源策略、源与目标 unit 名、新 unit SHA、新旧 controller SHA、迁移器 SHA 和 GNAS 配对身份。机器凭据由授权管理员签发；缺字段草案不是批准。

迁移器先验证全部前置、保存持久 journal 并消费批准，再安装尚未运行的目标 unit。有效配置核对通过后禁用并停止旧 unit，确认旧 PID 消失、7702 释放，再启动新 unit。新服务通过检查后原子安装 controller、开启目标自启动、回读源已禁用、目标已启用，并验证监听 socket 归属。全过程不启动旧二进制。

## 失败和中断

- 停旧进程之前失败且原进程仍健康：只撤销可证明属于本次的目标配置、恢复旧 unit 自启动，不重启源进程。并发文件/controller 变化立即拒绝覆盖。
- 目标 hybrid 启动或检查失败：仅在所有批准指纹仍匹配时，切同一新二进制的国脉静态恢复模式；controller、unit、配置漂移不进行盲目补偿。即使恢复成功，原迁移命令仍返回失败。
- 非正常中断、SIGKILL/主机断电：journal 位于 `/var/lib/wecom-mcp-release/unit-migrations/RELEASE`。先检查实际状态，不重复 apply。
  - 源原 PID 仍健康：`abort-prestop RELEASE` 使用已消费的原批准撤销本次未完成准备；不重新启动任何进程。新的迁移重新构建/批准。
  - 源已停止且完整目标配置存在：`recover-check RELEASE` 输出恢复 receipt 字段；管理员签发 action=`recover-shared-migration` 的新 receipt 后执行 `recover RELEASE ISSUED_APPROVAL_ID`，仅启动新 binary 的静态模式。
  - 不匹配上述条件，或 `.new` 文件/未知额外文件/源或目标身份漂移：失败关闭，保留 journal 与文件，由管理员审查后处理；不猜测应删除或覆盖的内容。
- 恢复后执行 `observe-recovery RELEASE`。正常模式与恢复模式均需五分钟观察。真实员工 MCP 调用与 Owner 验收另列；此迁移不能替代它们。

## 已启动目标的受控续行

Linux `Type=simple` 在 systemd 子进程执行候选 binary 之前就可能返回。启动验证只在头两秒允许 PID 尚未建立/已消失、systemd 自身/其 executor，且配置 ExecStart、重启次数、unit/runtime 指纹完全匹配时短暂等待；外来 executable、错误 SHA 和其他配置错误仍拒绝。

若迁移已停止源服务、启动同版本静态目标，但尚未完成 controller/enable/journal 收尾，可使用 `complete-shared-migration.py check RELEASE` 输出续行精确字段。它核验原已消费批准、原 journal、当前候选 PID/模式、配置和 listener；同提交 CI 的 `completion-provenance.json` 绑定四个脚本/unit 文件 SHA。使用 action=`complete-shared-migration` 的新一次性批准执行 `apply RELEASE APPROVAL`，复用同一不可变候选恢复 hybrid，失败仅恢复同一新二进制 static。原 journal 和原批准保留，续行记录独立追加；不伪装为首次迁移成功。管理员在会话中对已展示候选明确批准后，可由执行代理代为记录其真实批准及原话，不能把代理的技术判断记成人类批准。
