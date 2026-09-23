# MCP 混合发现生产发布

此目录是可审核的发布基础设施候选。它尚未安装到生产。不得把测试安装文档中的 Nginx、初始化或手工重启步骤用于本次发布。

## 已确认的部署边界

固定组件 `wecom-mcp@gmzoop.service`，环境 `zhycit-prod-01/wecom-mcp-gmzoop`，主机 SSH 别名 `zhycit.com`。既有服务用户/组 `wecom-mcp-gmzoop`、环境文件 `/etc/wecom-mcp/gmzoop.env`、静态 runtime manifest `/home/product/services/mcp/wecom/instances/gmzoop/config/fleet-runtime-20260916.json` 保留。该 manifest 的既有静态实例继续使用原 operator/AI execution subject、白名单、schema、state；新发现实例只读。

2026-09-23 只读预检：真实进程为 `releases/20260916T073038Z-4cfd3a3/wecom-mcp-team`，SHA `760f8c6e0a0a057ace850322f1b256c0dc1340a6daed1902b44131503772478d`。`current` 指向另一个旧目录，**不能据此判断运行版本或回滚目标**。控制器只以实际 `/proc/PID/exe`、二进制 SHA 和 systemd 生效文件摘要为准，不修改 current/previous 链接。

只处理单个 unit，不改数据库、凭据、现有环境文件、Nginx 或其它实例。控制器固定生成新增 `zz-managed-release.conf`，只覆盖 ExecStart/ExecStartPre 并追加 discovery state 可写路径；原 drop-in 和文件保留。候选端口固定复用 7702，不能在生产旁路启动第二份实例。

## 顺序与门禁

1. GNAS 配对版本先完成同提交 CI、构建、校验及暂存。MCP 兼容门禁通过前不切 GNAS。
2. MCP 混合模式的静态能力、动态只读、重复 ID/Host/Source、单 Binding 摘要、配置漂移、删除、503/421、恢复及并发回归通过；独立 Verifier 与 Reviewer PASS；PR/main CI 同 commit 通过。
3. 分别准备 MCP 基础设施安装和业务切换的精确批准包。安装控制器不会重启服务；业务批准不能被当作安装批准。
4. GNAS 新二进制单独按其 root-owned controller 和精确 receipt 发布，保持刷新开关关闭及原 OAuth clients；旧功能 smoke 和 11×30s 观察通过。
5. GNAS 刷新功能启用是单独配置变更，不包含在本目录或 GNAS 二进制 receipt 中。保留原 clients；记录旧员工 token 需重新登录。它必须通过对应受控配置流程，禁止临时 SSH 改 environment。真实身份/授权和双服务合同通过后才允许 MCP 切换。该独立受控配置流程须签发 root-owned 非秘密 `/etc/wecom-mcp/gnas-discovery-readiness.json`，包含 release_id、binary_sha256、main_pid、refresh_enabled=true、contracts_verified=true、verified_at（UTC，四小时内）；绑定实际 GNAS 进程。控制器不读取 GNAS 环境内容，并实时检查双域名 AS metadata。该证明尚未签发，不能由 Agent 将预期值冒充验证结果。
6. 已安装控制器先执行 `preflight-rollback <release-id>`，由固定 systemd oneshot 使用旧二进制、原用户与 EnvironmentFile 执行只读 `--check-config`，35 秒超时且不监听，丢弃输出。检查失败时保留旧进程，另行审批兼容回滚方案；不能补静态租户或删 Binding 绕过。deploy 临切前再次检查，不接受历史健康代替可重启证据。预检只证明当次当前 Binding 集合；后续 DB 变化必须重新核验回滚兼容性。
7. MCP 控制器 `deploy` 精确核对 GNAS release/SHA、健康、刷新已启用，以及 MCP 实际基线；执行原子 drop-in 替换、受控重启和本地/公网合同。失败只恢复保存的 drop-in 状态与原二进制，不回退数据库或 GNAS。
8. MCP `observe <release-id>` 完成 11 样本、10×30 秒观察；记录两个域名 healthz/readyz=200、无 token mcp=401，metadata 正确。真实员工 initialize/tools/list/只读 tools/call 及授权写能力目录另行验收，不用真实业务写入证明上线。Owner 产品验收单列。

## 混合配置与故障语义

同时指定 `--gnas-fleet-runtime`、`--gnas-discovery-policy`、`--gnas-state-root`。一次完整 GNAS payload 决定全部路由。manifest 中匹配的 Binding 选择完整静态实例；其 Source/Registry 不匹配或文件失效时拒绝，绝不静默降级为 reader。其余 Binding 从 Registry/Z-S00 发现为只读。所有实例使用 Service JWT + 单 Binding digest，无 Basic fallback；保留原 Basic 配置用于回滚，但不在新模式读取密钥值。

受保护静态配置在每个实际 Store 读取处绑定完整配置摘要，不使用 mtime 缓存。外部漂移必须等下一次完整 refresh 重新验证；已有初始化通过同 Store 合法写回时更新本代快照，避免成功写回被当成失败。数据库删除任意 Binding（包括静态）撤路由；保留本地配置不会复活已删除租户。

重复 ID/Host/Source、重复实例名/状态/Schema/config路径、路径别名或不完整新租户均失败关闭。刷新失败保留最后有效快照仅用于识别已知 Host，已知 Host=503，未知 Host=421；不使用旧权限继续服务。完整恢复后原子发布；合法空集合撤下所有路由。当前为全组失败关闭，不宣称新租户错误不会影响旧租户可用性。最长传播为 Source 既有缓存叠加 fleet 刷新窗口；监控不得将预期503变成无限重启。

## 离线验证与本地构建

```sh
python3 -B -m unittest discover -s teamserver/deploy/production -p 'test_*.py'
go test -race ./...
(cd teamserver && go test -race ./... && go vet ./...)
```

`build-candidate.py` 只读取干净仓库并编译 Linux/amd64，不访问生产。调用方先取得真实基线及同提交 CI URL：

```sh
python3 -B teamserver/deploy/production/build-candidate.py \
  --output /absolute/new/candidate \
  --expected-runtime-path /home/product/services/mcp/wecom/releases/<actual-release>/wecom-mcp-team \
  --expected-binary-sha256 <actual-runtime-sha> \
  --expected-unit-fingerprint <effective-unit-fingerprint> \
  --expected-runtime-config-fingerprint <runtime-and-static-config-fingerprint> \
  --expected-gnas-release-id <paired-gnas-release> \
  --expected-gnas-binary-sha256 <paired-gnas-sha> \
  --ci-url <same-commit-successful-ci-url>
```

输出二进制、共享只读 policy、精确 service.conf、manifest 和 SHA256SUMS；不包含租户配置或凭据。`manifest`、各资产 SHA、rollback 基线共同进入批准包。已有候选不可覆盖；失败构建不可提升。

## 安装批准（独立管理员）

仅管理员可把审查过的 `install-controller.py` 和 `mcp_release_controller.py` 放入 root-owned 不可由组/其它用户写的目录。先执行 `python3 -I install-controller.py --check`，只读取状态并给出精确字段。管理员在 `/etc/wecom-mcp/release-approvals/` 以 root:root 0400 创建 receipt。字段为：

- 通用：schema_version=1、environment、approval_id、action、approved_by、approved_at、expires_at。
- install-controller：controller_sha256、installer_sha256、expected_runtime_path、expected_binary_sha256、expected_unit_fingerprint、expected_runtime_config_fingerprint。
- action 固定 `install-controller`；approval_id 由授权管理员生成并返回，形如 `APR-<UTC>-<unique-suffix>`；不得由 Agent 把未批准模板冒充 receipt。有效期不超过4小时，批准时间不能在未来，每个 receipt 单次使用。

```sh
python3 -I install-controller.py --apply <issued-approval-id>
```

安装 root-only `/usr/local/sbin/wecom-mcp-release-controller`，创建专用控制目录和服务用户的 discovery state 目录。不会创建/轮换 credential、修改 sudo 权限、unit 或重启。安装前后状态精确一致；若已安装则拒绝覆盖，升级需单独流程。

## 暂存、切换与回滚

2026-09-23 只读 `ssh zhycit.com id -u` 返回 0；上传入口仍显式要求该固定账户为 root，否则在传输前拒绝。控制器安装后才可执行仓库 `stage-candidate.py --candidate <absolute-dir>`；它先读取已安装控制器基线，经固定 incoming 目录传输和 SHA 校验，最终调用 controller stage。不允许任意目标主机、重用目录或借传输命令切换服务。控制器将验证资产复制到新的只读 release 目录。

业务 receipt 的 action=`deploy`，附加字段：release_id、binary_sha256、manifest_sha256、expected_runtime_path、expected_binary_sha256、expected_unit_fingerprint、expected_runtime_config_fingerprint、expected_gnas_release_id、expected_gnas_binary_sha256。全部由候选和实时基线具体化后交给管理员，不让用户猜 approval_id。

```sh
sudo /usr/local/sbin/wecom-mcp-release-controller status
sudo /usr/local/sbin/wecom-mcp-release-controller verify <release-id>
sudo /usr/local/sbin/wecom-mcp-release-controller deploy <release-id> <issued-approval-id>
sudo /usr/local/sbin/wecom-mcp-release-controller observe <release-id>
```

deploy receipt 只预授权该次切换失败时恢复保存的原 override 和经校验旧二进制。历史旧版 resource-specific metadata 的404不作为回滚失败；旧版验证使用原基础 metadata 路径。所有正常版本验证仍要求新 resource-specific metadata。

手动回滚要求新 receipt，action=`rollback`，附加字段：release_id（当前需撤销版本）、binary_sha256（当前SHA）、expected_unit_fingerprint（当前unit）、rollback_runtime_path、rollback_binary_sha256（保存的精确目标）。调用 `controller rollback <release-id> <issued-approval-id>`；不删除 release、实例、state、审计或业务资产。

批准消费和回滚基线保存在 `/var/lib/wecom-mcp-release/used`、`rollback/<release-id>`，成功命令审计在 `events.jsonl`。控制器失败永远不能宣称发布成功；观察失败保持未稳定并准备精确回滚动作，不自动复用已消费 receipt。整个过程中 GNAS 和 MCP 的合并、CI、候选、暂存、切换、观察、功能启用及产品验收分别记录。
