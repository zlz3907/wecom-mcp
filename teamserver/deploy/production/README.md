# MCP hybrid 主模式与同版本静态恢复

本目录提供受控发布候选。Owner 已明确正常运行和失败恢复均使用新二进制，不要求旧二进制兼容新增 Binding。历史 schema v1 暂存包不得套用本流程，必须重新构建 schema v2。准备候选不授权上传、安装、重启或切换。

## 固定范围

仅 `zhycit-prod-01/wecom-mcp-gmzoop` 的 `wecom-mcp@gmzoop.service`，固定 SSH 别名 `zhycit.com`。原服务用户/组、环境文件、GNAS、数据库、凭据、Nginx、实例配置、Schema 和 state 保留。只通过受审控制器原子替换 `zz-managed-release.conf`；不修改 current/previous。实际运行版本以 `/proc/PID/exe`、SHA、unit/config 指纹为准。

- 主模式：新二进制 + 原 `fleet-runtime-20260916.json` + 共享 discovery policy + 30 秒刷新。原映射国脉完整能力，新租户只读。
- 恢复模式：**同一路径、同 SHA 的新二进制**和上述参数，再加 `--gnas-static-only https://mcp.wesiyu.com`。runtime manifest 必须恰好一个静态映射，其权威 URL 必须完全匹配。
- 恢复仅发布国脉，尖品客及其它未映射 Host 为 421；不会新增静态租户映射或删除 DB Binding。

## 为什么不用普通 --fleet

既有 `--fleet .../gmzoop/config/fleet.json --check-config` PASS 仅证明配置可加载。普通 fleet 使用既有 OAuth Basic introspection，不包含 hybrid 的 Service JWT + 单 Binding digest、配置代际快照及动态撤路由语义。它不能作为满足这些强制规则的恢复基线。

最小新版本恢复复用 hybrid 的鉴权和 `BoundRuntime`，每 30 秒读取完整 GNAS 权威 Binding，保留 Source/Registry 匹配、operator、AI 执行主体、Schema/state、固定租户和写入门禁。未映射租户跳过 Registry/Z-S00 发现；完整 payload 的结构、摘要、重复 ID/Host/Source 检查仍执行。删除国脉 Binding 或合法空集合会撤路由；配置漂移或刷新失败已知 Host=503、未知 Host=421，成功完整刷新后恢复。

此恢复用于隔离未映射租户发现/Registry 故障。**GNAS 不可用、权威 payload 无效、国脉本身配置错误、同二进制通用崩溃都不能靠换模式修好。** 遇到这些情况保持失败关闭、保留证据，再准备新的修复版；不能回用旧二进制或放松鉴权。单次 check-config 不证明真实员工工具调用或产品验收。

## 工程与发布门禁

1. 隔离干净源码，root/teamserver `go test ./...`、`go test -race ./...`、`go vet ./...`，控制器离线测试通过；HIGH 风险独立 Verifier 和 Reviewer PASS。
2. PR/main 同 commit CI 成功。无 CI 的本地候选允许构建但 stage/deploy 拒绝；URL 是证据索引，管理员必须独立核对成功结果和 commit，不可只凭字段存在。
3. 控制器升级独立批准。升级不重启服务，不可把业务 deploy receipt 用作升级 receipt。
4. GNAS 配对版本与已开启刷新必须按 GNAS 独立流程完成；原 OAuth client 保留。`/etc/wecom-mcp/gnas-discovery-readiness.json` 由独立受控流程签发，绑定实际 release/SHA/PID、refresh_enabled=true、contracts_verified=true 和四小时内 UTC 时间。Agent 不得代填真实合同 PASS。
5. 新控制器验证完整 schema v2 候选、基线、恢复参数及 SHA；`preflight-recovery` 使用**新二进制**、原服务用户/环境、35 秒 oneshot，执行只读 `--check-config`，不监听且丢弃输出。此操作会创建 transient unit，属于发布阶段，本次只准备候选时不得执行。
6. deploy 消费精确一次性批准，保留旧 override 的审计备份，切 hybrid 并验证双域名。本次切换失败自动切同版本静态恢复，且验证国脉健康和尖品客421。原配置备份仅用于审计，绝不作为运行目标。
7. hybrid `observe` 或静态 `observe-recovery` 均 11 样本、10×30秒；有失败不得报告稳定。真实员工 initialize/tools/list/只读 tools/call 及完整写能力目录另行验证，不能通过生产业务写入“试上线”。Owner 产品验收单列。

## 本地构建

```sh
python3 -B -m unittest discover -s teamserver/deploy/production -p 'test_*.py'
python3 -B teamserver/deploy/production/build-candidate.py \
  --output /absolute/new/candidate \
  --expected-runtime-path /home/product/services/mcp/wecom/releases/<actual-release>/wecom-mcp-team \
  --expected-binary-sha256 <actual-process-sha> \
  --expected-unit-fingerprint <all-effective-unit-files-digest> \
  --expected-unmanaged-unit-fingerprint <effective-unit-files-excluding-managed-override-digest> \
  --expected-runtime-config-fingerprint <runtime-and-static-config-digest> \
  --expected-gnas-release-id <paired-release> \
  --expected-gnas-binary-sha256 <paired-sha> \
  --ci-url <same-commit-successful-hosted-ci-url>
```

无 hosted CI 时省略 `--ci-url`，制品仅供本地审核，不得暂存或部署；CI 完成后用新的输出目录重建并重新签发批准。制品包含 binary、policy、service.conf、recovery.conf、manifest.json、SHA256SUMS。不包含租户配置或凭据。release目录不可覆盖。

## 控制器升级候选

`install-controller.py` 仍只允许首次安装；既有控制器必须走 `upgrade-controller.py`。管理员将审查过的源文件置于 root-owned、非组/其它用户可写目录后：

```sh
python3 -I upgrade-controller.py --check
python3 -I upgrade-controller.py --apply <issued-upgrade-approval-id>
```

`--check` 只读并持与部署相同的 release.lock。升级绑定原控制器 SHA、新控制器 SHA、升级器 SHA、当前进程 PID/路径/SHA、unit/config 指纹。批准消费后保留旧控制器和批准绑定，原子替换，再核验服务事实完全一致；后验失败且目标仍为该新控制器时恢复原控制器。不会回退 MCP 二进制，不安装/修改 systemd，不执行 restart 或 daemon-reload。外部并发漂移只拒绝，不覆盖他人控制器。

## 精确批准字段

所有 receipt 均由授权管理员签发：`schema_version=1, environment, approval_id, action, approved_by, approved_at, expires_at`；root-owned 只读文件，有效期最多4小时，不能未来签发，单次消费，不允许额外字段。缺字段草案不是批准，不得由 Agent 猜批准人、ID或时间。

| action | 额外精确字段 |
|---|---|
| `upgrade-controller` | controller_sha256, upgrader_sha256, previous_controller_sha256, expected_main_pid, expected_runtime_path, expected_binary_sha256, expected_unit_fingerprint, expected_runtime_config_fingerprint |
| `deploy` | release_id, binary_sha256, manifest_sha256, expected_runtime_path, expected_binary_sha256, expected_unit_fingerprint, expected_unmanaged_unit_fingerprint, expected_runtime_config_fingerprint, expected_gnas_release_id, expected_gnas_binary_sha256, recovery_mode=`same-version-static`, recovery_hosts=`["mcp.wesiyu.com"]`, recovery_sha256 |
| `rollback` | 与 deploy 相同，但 expected_unit_fingerprint 改为待恢复当前 unit 指纹，另加 source_override_sha256（当前hybrid或recovery override）；其余字段仍绑定同候选 |

`deploy` 仅预授权这一次失败时切同版本国脉恢复。`rollback` 名称保留以便操作一致，但其含义已变为**同二进制恢复模式**，不接受 v1 receipt 或旧目标。手工恢复与失败后的再次恢复均要求新的精确 receipt。恢复后的重新启用 hybrid 应重新读基线、构建候选和批准，不能复用已消费 receipt。

## 后续获批时的操作与验证

```sh
# 升级完成后才允许受控上传，不可在本次准备阶段执行
python3 -B teamserver/deploy/production/stage-candidate.py --candidate /absolute/candidate
sudo /usr/local/sbin/wecom-mcp-release-controller verify <release-id>
sudo /usr/local/sbin/wecom-mcp-release-controller preflight-recovery <release-id>
sudo /usr/local/sbin/wecom-mcp-release-controller deploy <release-id> <issued-deploy-approval-id>
sudo /usr/local/sbin/wecom-mcp-release-controller observe <release-id>
# hybrid失败/需手动恢复时
sudo /usr/local/sbin/wecom-mcp-release-controller rollback <release-id> <issued-recovery-approval-id>
sudo /usr/local/sbin/wecom-mcp-release-controller observe-recovery <release-id>
```

国脉 local/public healthz/readyz=200，无token mcp=401，新 resource-specific metadata=200；静态恢复另要求尖品客本地各端点421和公网/mcp=421。MCP进程路径/SHA必须始终为新候选。审计与恢复证据保留于 `/var/lib/wecom-mcp-release/rollback/<release-id>`；receipt消费在 `used/`，升级证据在 `controller-upgrades/`。失败不会返回成功，不修改/清理业务或审计资产。
