# wecom_instance_initialize 实施计划

状态：Planner / Architecture 候选，供 Executor、Verifier、Reviewer 审查
范围：首次初始化与已有实例迁移；不含生产部署、凭据处理、最终 Owner 验收
基线：`origin/main`（计划形成时分支 `codex/wecom-instance-initialize`）

## 1. 结论

新增一个真正贯通主线的实例初始化协调器，以“只读观察 + 可审计预览 + 幂等执行”替代当前分散的 Registry bootstrap、人工登记、九表补建、Schema 同步和手工配置修改。最小公开工具面为两个工具：

1. `wecom_instance_initialize_status`：纯只读，同时返回现状、冲突、可恢复缺口、计划操作和 `preview_id`；它就是 dry-run，不再另设第三个 preview 工具。
2. `wecom_instance_initialize_apply`：以未过期的 `preview_id`、status 原样返回的 expiry 和固定 Owner 授权为执行门槛，并只接受与该 preview 绑定的受控 import/recovery ID；重新读取线上状态并核对预览后，按 durable journal 逐阶段 reconcile。

`wecom_registry_bootstrap` 与 `wecom_schema_sync` 在一个兼容周期内保留，但改为复用新协调器的局部能力并标注 deprecated，避免出现第二套状态机。

架构审查后的放行边界：在 `zoop-v1` create-time catalog、专用初始化权限、完整分页快照、并发锁与跨平台持久替换原语未由测试证明前，`apply` 必须保持 fail-closed；可以先交付 status/preview、本地 journal 与原子提交 primitives，但不得把“工具已注册”写成“完整初始化已可用”。

## 2. 当前能力证据与 WorkBuddy 流程问题

### 2.1 代码事实

- `internal/mcp/registry_bootstrap.go` 的 `bootstrapRegistry` 只在 `registry_document_id` 为空时创建 `SMART_SHEETS_IDS`，确保 20 个文本字段，保存本地 sentinel，再原子写回单个 `registry_document_id`。
- 同一函数在配置已有 `registry_document_id` 时直接返回 `already_configured`，没有在线校验文档真实性、字段完整性或业务登记唯一性。
- bootstrap 不创建 Zoop 业务文档，不创建 Z-S01 至 Z-S09，不写唯一 active registry row，不同步 Schema，也不做 Z-S01 smoke。
- `internal/mcp/server.go` 的 `syncSchema` / `readOnlineSchemaFields` 先调用 `wecom.ResolveTargets`，再只读九表字段并写本地 JSON/Markdown 镜像。它要求九表已经存在，不负责创建或修复九表。
- `internal/wecom/registry.go` 的 `ResolveTarget` 要求 Registry 至少有 `registry_key`、`docid`、`lifecycle_status` 字段，并要求指定 `registry_key` 恰好有一条 active 记录；随后要求目标业务文档内指定角色前缀的子表恰好一张。
- 当前 `PersistRegistryDocumentID` 只原子填充一个空 docid；它不能把 Registry、生成版 Schema 路径和初始化状态作为同一次本地提交更新。
- `schema_admin_user` 只用于受控 Schema migration 的本机管理员身份校验；它不是 Registry bootstrap 的前置条件。把 bootstrap 失败归因于该字段会误导排障。

### 2.2 昨日 WorkBuddy 暴露的不顺畅点

1. 安装、注册、加载成功后，用户仍被要求理解并手工编辑 `registry_document_id`、`schema_mirror_path`、state 路径等内部参数。
2. bootstrap 失败时只留下 `creating` sentinel，没有“选择已创建资产并绑定回同一操作”的产品恢复入口，普通用户只能等待技术人员清 sentinel 或重新编排提示词。
3. Registry 文档建成后，业务文档、九表、active registry row 和 Schema 同步仍是多个孤立步骤；任一步遗漏都会导致后续 `ResolveTarget` 失败。
4. 诊断曾把无关的 `schema_admin_user` 占位符和凭据作为 bootstrap 根因；真正的 Source/JWT、线上 API、初始化结构没有按层分开。
5. 实例 state 路径与实例名曾混用，证明手工路径编排很容易串租户。
6. 验收报告只清楚区分了 `installed/configured/loaded`，没有把 Registry、九表、Schema sync、真实 `tools/call` 作为独立可回读门禁。
7. 已有配置被当作可信事实；缺少对现有 Registry docid、字段和唯一 active row 的线上验证，也没有安全的已有实例 import/reconcile 流程。

## 3. 公开工具/API 设计

### 3.1 `wecom_instance_initialize_status`

输入（所有字段 `additionalProperties=false`）：

```json
{
  "registry_document_id": "optional-existing-registry-import-id",
  "recovery_registry_document_id": "optional-only-for-matching-unresolved-operation",
  "recovery_business_document_id": "optional-only-for-matching-unresolved-operation",
  "existing_business_document_id": "optional-owner-decision"
}
```

- `registry_document_id` 只用于受控导入已存在且无 unresolved sentinel 的 Registry；`recovery_registry_document_id` 只绑定 journal 中同一 unresolved Registry create operation。两者不会进入普通查询工具，也不会改变固定租户边界。
- `recovery_business_document_id` 是 Owner 已要求的 sentinel 同资产恢复能力，只允许在 journal 存在同一未决业务文档创建操作时绑定；它不是可选产品决策。
- `existing_business_document_id` 用于导入从未登记、也不属于当前 unresolved operation 的历史文档；是否公开由第 13 节的唯一 Owner 决策确定。
- 无写入、无本地持久化；读取初始化候选配置和 GNAS 环境，检查 Registry、业务文档、九表、active row、本地 Schema/config 状态。
- 返回固定结构：

```json
{
  "state": "ready|changes_planned|recovery_required|conflict|environment_unavailable",
  "instance_name": "...",
  "expected_schema_version": "zoop-v1",
  "observed": {},
  "invariants": [],
  "conflicts": [],
  "planned_operations": [],
  "preview_id": "sha256...",
  "expires_at": "RFC3339 timestamp",
  "snapshot_complete": true,
  "recovery_asset_kind": "registry|business|null",
  "recovery_operation_id": "...",
  "next_required_input": "...",
  "online_read_only": true,
  "local_updated": false,
  "enterprise_wecom_updated": false
}
```

`preview_id` 覆盖：config digest、journal digest、catalog version/digest、所有输入 ID、全部分页完成标记、Registry sheet/field IDs、active record ID 与全部受管值、九表 IDs、全部受管字段 property、期望 Schema 版本和计划操作；不覆盖也不泄露秘密。任一分页未完成时不得生成可供 apply 使用的 preview。

### 3.2 `wecom_instance_initialize_apply`

输入：

```json
{
  "preview_id": "64-char-sha256",
  "preview_expires_at": "same-RFC3339-value-returned-by-status",
  "owner_authorization": "initialize_or_reconcile_default_zoop_instance",
  "registry_document_id": "optional-same-as-status",
  "recovery_registry_document_id": "optional-same-as-status",
  "recovery_business_document_id": "optional-same-as-status",
  "existing_business_document_id": "optional-same-as-status-if-owner-approved"
}
```

- 首先重新执行 status，任何线上摘要、输入 ID 或期望 Schema 版本变化都会使 preview 失效。
- apply 还必须检查专用 `instance_initialize` capability group 与本机受保护初始化身份（复用 `schema_admin_user` 同等级的本机身份校验，但使用独立权限语义），并校验 `preview_expires_at` 未过期且已纳入 preview digest。固定授权字符串只负责防误触，不构成权限边界；首版不引入工具参数明文 token 或第二套一次性状态。
- preview TTL 固定为 10 分钟，允许最多 30 秒时钟偏差；apply 必须同时接收 status 原样返回的 `preview_expires_at`，重算 digest 和当前 snapshot 后才能继续。
- 若当前已 `ready`，只返回完整回读证据，不写线上或本地文件。
- 否则按第 4 节状态机推进，所有线上写入后立即回读。
- 输出不返回凭据、原始请求头、完整上游错误体或业务记录值；只返回资产 ID、结构摘要、阶段与验收证据。

### 3.3 不新增单独 dry-run 工具的理由

status 必须读取真实线上状态才能可信；把计划和 `preview_id` 放进 status 即可同时满足 status 和 dry-run。独立 preview 会重复同一只读实现并增加客户端选择成本。apply 只消费同一份状态摘要，保持最小工具面。

## 4. Reconcile 状态机

### 4.1 持久阶段

状态记录在实例 `state_path` 派生的受保护 journal 中，建议版本化为 `instance-initialize-v1`：

status 的 `observing` 是纯只读瞬时状态，不写 journal。apply 的持久阶段从预留副作用开始：

1. `registry_resolving`：验证配置/输入 docid，或为新建 Registry 预留 at-most-once sentinel。
2. `registry_identity_known`：Registry docid 已持久记录，可始终恢复同一资产。
3. `registry_schema_verified`：Registry 智能表与 20 个标准文本字段完整、唯一、类型兼容。
4. `business_resolving`：从唯一 active row、恢复参数或新建回执取得业务 docid。
5. `business_identity_known`：业务 docid 已持久记录，可始终恢复同一资产。
6. `zoop_sheets_reconciled`：Z-S01 至 Z-S09 均恰好一张，期望字段已增量补齐并回读。
7. `registry_row_verified`：当前 `registry_key` 恰好一条 active row，且指向上述业务 docid。
8. `schema_staged`：从在线九表生成新的本地机器 Schema，并在 generation 路径回读验证。
9. `candidate_smoke_verified`：使用候选 runtime/已观察 ID 对 Z-S01 做只读 smoke，尚未切换用户配置。
10. `config_committed`：实例配置原子指向已验证 Registry、generation Schema 和初始化 generation 元数据；配置回读通过。配置内 `initialized_state` 固定写 `config_committed`，绝不提前写 `ready`。
11. `final_smoke_verified`：通过正常 Resolver 对 Z-S01 再执行一次 `get_records limit=1`。
12. `ready`：所有验收证据写入 journal；`ready/tools_call_verified` 只由 journal/status 报告，不反写为配置中的预先声明。

异常状态：

- `recovery_required`：创建请求结果不确定且没有 docid，或需要用户选择既有资产；禁止再次 create。
- `conflict`：重复 active row、重复角色表、字段同名异型、引用目标冲突、已配置 ID 与输入 ID 不一致等；禁止自动修改。
- `environment_unavailable`：GNAS 环境/Source 只读链不可用；不进入任何创建阶段。

### 4.2 全程不变量

- 固定 `instance_name`、`tenant_route`、`registry_key`，初始化工具不能改租户或凭据来源。
- 所有初始化 API 必须来自专用 `instance_initialize` allowlist group；其他分组即使允许同名操作，也不能使初始化写路径可达。
- 配置里的 docid 只是候选，必须以在线 `get_sheet/get_fields/get_records` 回读为准。
- 每种资产只有一个未完成创建操作；任何 create 在调用前都要 durable reserve。
- Registry 的 20 个标准字段标题唯一、类型为文本；登记前中间态允许同 `registry_key` 有 0 或 1 条 active，超过 1 立即冲突；进入 `registry_row_verified` 后必须恰好 1 条。
- 每个 Z-S01 至 Z-S09 角色前缀恰好匹配一张子表。
- 每个写操作都要定向回读；“API 调用成功”不等于阶段完成。
- 仅补缺，不删除线上表、字段、记录，不覆盖不兼容结构，不自动把重复记录改成 inactive。
- 本地配置提交前，Registry、九表、active row 和生成版 Schema 必须全部回读通过。
- 同机只允许一个 initializer apply：使用进程级互斥和跨进程文件锁；预留、阶段转换与 rename 后必须 fsync 文件和父目录。跨机器不能声称强原子，必须依靠 operation_id、完整分页碰撞检测和重复后 fail-closed。

## 5. Registry 导入、验证、创建与 uncertain 恢复

### 5.1 发现优先级

1. 新状态 journal 中已有已知 Registry docid：只恢复该资产。
2. 当前配置已有 Registry docid：线上验证；输入不同 docid 时 fail-closed。
3. status/apply 明确传入 Registry docid：作为 import 候选验证，不立即写配置。
4. 三者均无且不存在未决 sentinel：计划创建 `SMART_SHEETS_IDS`。

真实性验证至少包括：来源仍为固定 `tenant_route`；`get_doc_base_info` 证明 doc type 与预期标题/受控历史标题；`get_doc_auth` 证明初始化主体具备所需管理权限；docid 可访问；Registry 智能子表唯一且 sheet ID 进入 preview；20 个标准字段标题唯一且类型兼容；`registry_key/docid/lifecycle_status` 可用于可靠完整分页读取；同 registry key 的 active 记录数为 0 或 1，绝不能大于 1。历史业务文档不能只因“可补齐 Z-Sxx”就判为真实目标。

### 5.2 新建与默认字段

- 调 `create_smartsheet` 前，在跨进程锁内以 `O_EXCL + file fsync + parent-directory fsync` 写入 `{operation_id, asset_kind=registry, intended_name, phase=creating, attempt=1}`。
- 企业微信创建智能表格会返回新文档 docid 和默认智能子表；docid 必须在收到回执后立刻写入并 fsync，再进行任何后续调用。
- 先 `get_sheet`、`get_fields` 回读默认子表。对新建且经 journal 证明归本操作所有的空白默认字段，优先用 `update_fields` 改为第一个标准字段，再 `add_fields` 添加其余字段，避免遗留无意义默认列。既有文档不自动改名默认字段；出现额外字段允许保留，但标准字段同名异型/重复即冲突。

### 5.3 response uncertain / sentinel 同资产恢复

- create 网络错误、超时、无效响应或缺少 docid 时，将原始错误归一化为安全错误码，journal 进入 `recovery_required`；同一 asset key 的再次 apply 绝不再调用 create。
- 若响应中可可靠提取 docid，即使后续失败也先持久化 ID，下一次从同一文档继续。
- 若完全没有 docid，而上游没有可证明唯一性的“按 operation_id 查创建结果”接口，则不能假装自动恢复。status 必须提示用户在企业微信界面选择刚才创建的候选文档并提供 `recovery_registry_document_id` 或 `recovery_business_document_id`；协调器只在 operation_id/asset_kind/预期名称与结构均匹配时绑定原 sentinel，再从同一资产继续。
- 不提供“删除 sentinel 后重试”给普通用户。放弃未决资产必须是单独的管理员处置流程，保留审计理由；本需求首版不实现自动删除或在线清理。

## 6. Zoop 业务文档、九表与唯一 active row

### 6.1 业务文档发现/创建

- 若 Registry 已有唯一 active row，业务 docid 只能来自该 row；输入不同 docid 直接冲突。
- 若没有 active row，优先使用业务创建 sentinel 中已知 docid；若 sentinel 未知 ID，则允许受控 `recovery_business_document_id` 绑定同一 unresolved operation；其次按第 13 节的 Owner 决策决定是否允许 `existing_business_document_id` 导入任意历史文档；最后才计划创建新业务文档。
- 新建业务文档使用确定性可读名称（例如 `Zoop｜<registry_key>`），并采用与 Registry 相同的 at-most-once sentinel 和 uncertain 恢复语义。

### 6.2 Z-S01 至 Z-S09 reconcile

- 新业务文档的默认智能子表复用为 Z-S01：先回读，确认是本操作新建且为空，再用 `update_sheet` 改为权威标题；Z-S02 至 Z-S09 使用 `add_sheet`。
- 既有业务文档按精确角色前缀解析。缺失角色可新增；同一角色多张表立即冲突，不猜目标、不重命名用户表。
- 每张新表先 `get_fields`，将空白默认主字段改成该角色的首个期望字段，再添加其余字段。禁止直接叠加一列而遗留默认主字段。
- 字段采用两阶段创建：先建文本、数字、日期、单选等不依赖字段；所有 sheet ID/主字段 ID 回读后，再按逻辑角色与字段名解析并创建关联字段。不能把其他租户的 field ID 写入新实例。
- 每个角色完成后回读表标题、字段标题/类型/选项/关联目标；任何不兼容都停在 `conflict`。

### 6.3 唯一 active registry row

- 九表全部验证后才允许登记业务文档。
- 用在线 Registry field ID 构造 `add_records` 的 `records[].values`，最少填写完整标识字段和可审计元数据；禁止把本地字段 ID 当作线上 ID。
- active row 为 0 时新增一条；为 1 且 docid 相同时复用，并只补齐缺失的非身份元数据；为 1 但 docid 不同或大于 1 时 fail-closed。
- 新增回执一旦含 record_id 就立即写 journal；随后用 record_id 精确回读，再用共享的可靠分页器确认同 registry key 仍恰好一条 active；不得沿用单次固定 limit 的 Resolver 假设。
- 写入结果不确定且无 record_id 时，不盲目新增第二条；先按 registry key 完整读取。如果唯一新 active row 已出现且指向预期 docid，则收敛到该 row；否则进入人工恢复。

## 7. 版本化期望 Schema 与冲突策略

### 7.1 来源

当前在线镜像包含租户专属 sheet/field ID，只能描述 observed state，不能作为新实例 create-time desired state。应在仓库新增经 Architecture/Owner 审阅的逻辑 Schema catalog，例如：

```text
internal/zoopschema/catalog/zoop-v1.json
```

并用 `go:embed` 编入二进制。catalog 仅包含：角色、权威表标题、主字段、字段标题、字段类型、完整类型专属 property（日期/用户/数字/单选等）、选项文本、是否多选、逻辑关联目标 `{role, field_title}`、公式或不支持创建的明确标记、必需性、schema version 和 content digest；绝不包含任何租户 doc/sheet/field/option ID。catalog 的规范来源是 Owner 授权的当前 Zoop 九表 Schema 字典，经一次明确、可重现且去租户 ID 的生成/审阅提交固化，不在运行时从任意本地镜像推断。Registry 20 字段与唯一 active row 的精确受管值另作为同版本 contract 固化。

初始化代码必须同时修正本地 Schema loader 的九表强校验：Z-S01 至 Z-S09 缺一不可。任何字段缺少已验证 create-time property（尤其公式）时，catalog 可以用于观察与冲突报告，但在线创建路径必须返回 `catalog_not_creation_complete`，不能猜参数。

### 7.2 增量修复

- 缺失表：新增。
- 缺失字段：新增；新表默认字段可在“本操作创建且仍为空”时改名复用。
- 同名同型字段：复用；同时核对选项和关联属性。
- 同名异型、选项语义冲突、关联目标冲突、重复字段/表：fail-closed，输出最小冲突摘要。
- 首版不删除、不自动重命名既有资产、不收窄用户字段选项、不修改已有记录。
- catalog 升级通过独立版本和迁移规则完成；初始化器只针对固定 expected version reconcile，不能把在线现状反向覆盖 desired catalog。

## 8. 配置备份、原子写回与崩溃恢复

线上企业微信与本地文件无法形成真正的分布式事务，因此采用“线上先回读收敛 + 本地 generation 提交点 + durable redo journal”。

### 8.1 提交顺序

1. 从已验证线上九表生成机器 Schema 到新的不可变 generation 路径，例如 `<state-dir>/schema-generations/<schema-digest>.json`，权限 `0600`；写临时文件、fsync、原子 rename、fsync 父目录，再用正常 loader 回读。
2. 在 `<config>.backups/` 保存带时间戳和摘要的原配置备份，保留原权限；备份不包含任何 GNAS 秘密，因为秘密从不进入实例配置。
3. 生成完整新配置临时文件：保留固定租户和白名单，只更新已验证的 `registry_document_id`、上述 generation Schema 绝对路径和不可变初始化元数据：`initialization_generation`、`schema_version`、`schema_digest`、`registry_sheet_id`、`initialized_state=config_committed`；校验后 fsync。配置不得预先写 `ready` 或 `tools_call_verified`。
4. 在切换用户配置前，使用候选 runtime 与已观察 ID 完成 Z-S01 只读 smoke。
5. 用跨平台安全替换把配置作为唯一的本地 commit point 原子切换。POSIX rename 后 fsync 父目录；Windows 使用 replace-existing/write-through 语义并保留 ACL。备份目录本身权限必须受保护。
6. 重新通过 `Store.Current`、`LoadSchema` 和正常在线 Resolver 做最终 Z-S01 smoke；journal 标为 `ready`。最终 smoke 失败不删除线上资产或盲目重建，journal 记录 `config_committed` 并在下一次前滚核验。

### 8.2 崩溃恢复

- journal 每次阶段转换均采用临时文件、fsync、rename；包含 `journal_version`、operation IDs、已知资产 ID、期望/观察摘要、最后完成阶段和安全错误码。
- 崩溃在配置提交前：旧配置仍可用；下次依据已知线上 ID 恢复同一资产，重建 generation。
- 崩溃在配置提交后、journal 更新前：比较当前配置 digest、generation digest 和线上不变量，识别提交已完成并前滚，不重复写线上。
- 若配置替换失败，保留 `.backup` 和已完成的线上阶段，下一次只重试本地提交。
- 不要求 config/schema/state 三文件物理同时 rename；generation + config 单一提交点消除了跨目录多文件“伪原子”承诺。

## 9. 凭据与敏感信息边界

- `GNAS_BASE_URL/GNAS_APP_ID/GNAS_APP_SECRET` 仅从进程环境读取；不进入工具参数、实例配置、Schema、journal、备份、日志、preview digest 或测试 fixture 快照。
- 状态与错误输出只包含固定实例名、阶段、资产 ID、结构计数、摘要和归一化错误码；不得透传 Authorization、请求头、环境 dump、原始含敏感字段响应。
- 业务 smoke 只返回成功状态和记录计数，不回传 Z-S01 业务记录内容。
- 新增测试对所有结果、日志、journal 和备份执行 secret canary 扫描。

## 10. 已有实例迁移策略

| 现状 | reconcile 行为 |
|---|---|
| 完整 Registry、唯一 active row、九表、Schema/config 均一致 | 只读核验，`planned_operations=[]`，apply 不写入 |
| 配置已有 Registry docid，但在线不可访问/不是合法 Registry | `conflict`，不创建替代 Registry |
| 旧 bootstrap sentinel 有 docid | 导入新 journal，从该 Registry 继续 |
| 旧 bootstrap sentinel 为 `creating` 且无 docid | `recovery_required`，选择现有 Registry 后绑定；禁止 create |
| Registry 合法但无 active row | 复用 sentinel/显式业务 docid，或新建业务文档；九表完成后登记 |
| active row 指向业务文档但缺部分角色表/字段 | 仅增量新增可恢复缺口并回读 |
| active row 重复、角色表重复、字段冲突 | `conflict`，不给自动破坏性修复 |
| 九表完整但本地 Schema 缺失/旧路径 | 生成新 generation，原子提交配置，再 smoke |
| 配置与输入 ID 不一致 | `conflict`，要求管理员先明确迁移目标，绝不覆盖 |

旧 `wecom_registry_bootstrap` 的 state 文件只做一次只读迁移映射，不删除；新 journal 记录其路径与摘要以便审计。旧 `wecom_schema_sync` 可继续作为独立只读镜像刷新工具，但 initializer 内部直接复用同一字段读取/镜像生成函数。

## 11. WorkBuddy 验收矩阵

每一层只能证明自身，不得向上折叠：

| 层级 | 通过证据 | 失败时不得声称 |
|---|---|---|
| `installed` | Release manifest/hash、平台包、binary 路径与版本一致 | configured/loaded |
| `configured` | WorkBuddy MCP 注册项指向预期 binary/config；实例配置能加载且无秘密 | loaded/registry |
| `loaded` | 当前 WorkBuddy 进程完成 MCP `initialize` 和 `tools/list`，可见两个 initializer 工具 | registry/verified |
| `registry` | 中间态可单独报告 `registry_structure_verified=yes`；最终 `registry_verified=yes` 只在当前 key 恰好一条 active 且指向九表核验通过的业务文档时成立 | nine_tables |
| `nine_tables` | active row 指向的业务 doc 可访问；Z-S01..Z-S09 各恰好一张；字段与 catalog 兼容 | schema_sync |
| `schema_sync` | 新 generation 镜像由同一线上快照生成，digest/loader 回读通过；配置原子指向该路径 | tools_call |
| `tools_call` | 经 WorkBuddy 当前 MCP 连接调用 initializer status 为 ready，并通过正常 `wecom_record_query`/内部等价 Resolver 对 Z-S01 做 `limit=1` 只读 smoke | Owner 验收/生产部署 |

最终机器可读汇报至少输出：

```ini
installed=yes|no
configured=yes|no
loaded=yes|no
registry_verified=yes|no
nine_tables_verified=yes|no
schema_synced=yes|no
tools_call_verified=yes|no
owner_accepted=no
production_deployed=no
```

## 12. 测试、任务拆分、Gate 0-4 与回滚

### 12.1 实施任务

1. **Architecture / Schema catalog**：冻结 `zoop-v1` 逻辑 catalog、Registry contract、公开 JSON schema、journal schema 和错误分类；产出 golden fixtures。
2. **Executor / Reconciler core**：实现 observer、planner、preview digest、durable journal、Registry/业务/九表 reconcile、唯一 row 和 local generation commit；复用现有请求与 Schema mirror 代码。
3. **Executor / MCP & compatibility**：注册 status/apply 工具；让旧 bootstrap/schema sync 复用新 primitives；更新 README、安装提示和 WorkBuddy 分层报告。
4. **Verifier / Independent tests**：独立 worktree 对候选提交执行单元、故障注入、stdio 合约、Windows 原生和非生产真实租户只读/受控测试；Verifier 不修改候选实现。
5. **Reviewer / Gate review**：审查租户固定、凭据边界、幂等/uncertain、Schema 冲突、配置提交、回滚和验收声明；只在 Gate 0-4 全绿后建议合并/发布。

### 12.2 必测场景

- 全新实例：两个文档、九表、字段、唯一 row、Schema/config、smoke 一次收敛。
- 已 ready：重复 status/apply 为零写入，create 调用计数为 0。
- 每个阶段崩溃后恢复，验证每种资产 create 最多一次。
- create 超时且有/无 docid、add_records 超时、配置 replace 失败、journal 更新失败。
- stale preview、配置 ID 冲突、重复 active row、重复角色表、同名异型字段、错误引用目标。
- 默认表/字段正确复用，不留下多余默认主列。
- 关联字段在目标 sheet/field ID 回读后才创建。
- 本地生成 Schema 可加载，配置 commit 前后均不存在断裂引用。
- stdout/stderr、MCP result、journal、backup 中不存在 secret canary。
- `go test ./...`、race test（可用平台）、stdio `initialize/tools/list/tools/call` contract、Windows PowerShell 5.1 安装/重复安装矩阵。

### 12.3 Gate 0-4

- **Gate 0｜范围与证据**：Owner 基线、当前能力缺口、固定租户/无凭据/无生产部署边界明确。
- **Gate 1｜架构与安全**：状态机、at-most-once、import/recovery、Schema catalog、事务提交和冲突策略经 Architecture/Security 审查。
- **Gate 2｜实现与自动验证**：Executor 候选在隔离 worktree 完成；全部测试和故障注入通过。
- **Gate 3｜独立验证与 Review**：Verifier 用独立 worktree/提交进行黑盒验证；Reviewer 无 P0/P1 未决问题。
- **Gate 4｜非生产 WorkBuddy 验收候选**：分层证据完整；仅可建议 Release/Owner 验收，不代表已发布、生产部署或 Owner accepted。

### 12.4 回滚

- 代码/客户端：切回上一受校验 Release 和其客户端配置备份。
- 本地配置：仅在新配置无法加载且尚未被后续运行使用时恢复原子备份；generation Schema 不删除，保留审计。
- 线上：不自动删除新建文档、子表、字段或 registry row。配置提交后优先前滚修复；未完成资产由 journal 标记并交管理员审计。
- 绝不通过清 sentinel、重复 create 或删除 active row 来“回滚”。

## 13. 唯一需要 Owner 决策的问题

**问题：当 Registry 合法但没有 active row，且企业微信中存在一个从未登记、也不属于当前 unresolved create operation 的历史 Zoop 业务文档时，初始化工具是否允许通过显式 `existing_business_document_id` 导入该文档？**

推荐方案：**允许，但只开放在 initializer 的 status/apply 中，并受严格 import 校验与 preview 绑定。** status 只读验证文档基础信息、权限、受控历史标题、Z-S01 至 Z-S09 唯一性和字段兼容性；apply 只有在 preview 未失效、没有冲突时才补缺并登记。该参数不得进入普通记录工具，也不得覆盖已有 active row。这样可以迁移已有客户并避免创建第二份 Zoop。

替代方案：**不允许任意历史业务 docid import。** 没有 active row 且不存在 unresolved sentinel 时只允许创建新业务文档；优点是接口更窄，缺点是无法自动复用未登记旧文档，必须由管理员先在 Registry 外部补登记。

无论 Owner 选择哪一项，`recovery_business_document_id` 都必须存在：它只绑定当前 journal 中同一 unresolved create operation，是 Owner 已明确要求的 sentinel 恢复能力，不属于本决策。

除这一点外，其余行为均可由已明确的 Owner 基线决定：创建/复用、仅增量修复、冲突 fail-closed、uncertain 不盲重试、原子本地提交和分层验收都不需要再次询问。

## 14. 成熟模式与官方 API 依据

- 企业微信官方团队公开的 WeCom CLI Smart Sheet skill：<https://github.com/WecomTeam/wecom-cli/blob/main/skills/wecomcli-smartsheet/SKILL.md>。其中明确了 `create_smartsheet(doc_type=10)` 返回新 docid 和默认子表、`get_sheet/add_sheet/update_sheet`、`get_fields/update_fields/add_fields`、`add_records` 的调用形态；新建表应先回读默认字段并改为首个业务字段，再新增其余字段，以免遗留无意义默认列。
- Terraform Resource Import：<https://developer.hashicorp.com/terraform/plugin/framework/resources/import>。成熟做法是先按外部 ID import，再通过 Read 刷新完整状态，而不是把本地配置中的 ID 当作事实。
- Terraform Resource Read：<https://developer.hashicorp.com/terraform/plugin/framework/resources/read>。读取失败时保留既有状态，不用不完整的新状态覆盖旧状态，适用于本计划的 fail-closed 和本地 commit point。
- Kubernetes Controllers：<https://kubernetes.io/docs/concepts/architecture/controller/>；controller-runtime FAQ：<https://github.com/kubernetes-sigs/controller-runtime/blob/main/FAQ.md>。协调器持续比较 desired/observed state，reconcile 必须幂等；先读取全部相关状态，再进行最小写入，并以最终状态而非调用次数验收。
