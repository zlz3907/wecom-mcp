# wecom-mcp-v2

一个轻量、固定租户的企业微信 MCP。它以 Go 标准库实现，采用 stdio JSON-RPC；每个进程只服务一个在启动配置中固定的企业微信租户，不启动 Node 或子进程。

## 实例模型

同一代码库可创建多个独立实例。每个实例分别拥有：

- 一个 Codex MCP 实例名，例如 `zoop_wecom_zhycit`；
- 一个固定的企业微信 GNAS 路由；
- 一个固定的 `SMART_SHEETS_IDS` 入口文档；
- 一个允许的 Zoop 登记键；
- 一个本地只读 Schema 镜像；
- 一个可选的本机 Schema 管理员账号；
- 一份可热重载的 API 白名单和幂等状态文件。

调用方不能传入企业微信租户、URL、凭据、文档 ID 或子表 ID。因此多个实例之间没有运行期路由选择，也不会串写。

## 当前工具

- `wecom_registry_bootstrap`：仅当实例配置缺少 `registry_document_id` 时，经 Owner 显式授权创建 `SMART_SHEETS_IDS`、建立标准字段、回读并原子写回配置；创建状态不确定时失败关闭且不重复创建。
- `wecom_schema_status`：读取本地 Schema 镜像状态。
- `wecom_schema_sync`：在 Owner 明确授权后，线上只读同步 Zoop 九表的 Schema 镜像。
- `wecom_schema_migration_preview`：只读生成内置 Schema 增量迁移的当前值 → 目标值、影响和绑定预览。
- `wecom_schema_migration_apply`：仅在本机管理员身份、显式授权和有效预览同时成立时执行内置迁移，随后回读。
- `wecom_field_codec_lab_create`：创建字段编码验证表，并在同一流程中登记到 `SMART_SHEETS_IDS`、回读核验。
- `wecom_field_codec_lab_read`：只读验证表中人工填写后回传的原始单元格值。
- `wecom_field_codec_lab_reference_debug`：对照读取关联字段在不同 `key_type` 下的原始回包。
- `wecom_field_codec_lab_reference_write_probe`：仅在关联字段实际指向的目标子表创建目标记录，并以字符串数组 wire codec 验证来源记录新增、更新与回读。
- `wecom_field_codec_lab_registry_status`：核对验证表在 `SMART_SHEETS_IDS` 的登记状态。
- `wecom_record_read`：按 `Z-S01` 至 `Z-S09` 读取当前实例的记录。
- `wecom_record_apply`：按字段名称写入已通过字段验证的类型，并在一次写入后回读；同时维护 Zoop 需求的任务计数不变量。
- `wecom_requirement_progress_reconcile`：在真实 `applied_progress_sync_pending` 或巡检发现计数漂移时，只重算全部 S01 需求进度，不重复写 S03 任务。

`wecom_record_apply` 只使用本地 Schema 镜像中已登记的字段映射。当前已在字段验证表证明可读、插入、更新和回读的类型见 [字段兼容性矩阵](FIELD_CODEC_COMPATIBILITY.md)。关联字段对调用方接受受控 `{record_id}` 对象，发送企业微信前编译为已验证可落库的 `record_id` 字符串数组。未取得真实样本或官方契约的字段一律拒绝猜测写入。Owner 授权“线上企业微信 → 本地 Schema 字典”同步可以刷新接口实际返回的字段元数据，但不能补出接口本身未返回的编码。

Zoop 进度计数由 MCP 统一维护，不依赖调用方事后补写：

- 新建 `Z-S01` 时，`计划任务基线`、`当前任务总数`、`已完成任务数`、`阻塞任务数` 缺省写入 `0`；
- `Z-S03` 新增或更新并回读成功后，立即重算其新旧主需求；`当前任务总数` 不含“已取消”，完成数只计“已完成”，阻塞数只计“阻塞”；
- `计划任务基线` 不随任务生命周期自动变化，由 Planner 完成初始拆解后显式写入获批的初始计划数；
- 任务已写入但需求计数写入或回读失败时，返回 `applied_progress_sync_pending`，不得报告为完整成功或盲目重试。
- 同机多个 MCP 进程通过共享文件锁串行处理 S03 和幂等状态；写入计数后再次读取 S03，发现并发变化时最多按最新快照重算三次，持续变化则转为待恢复。
- 待恢复时使用新的幂等键调用 `wecom_requirement_progress_reconcile`；该工具只修复 S01 派生计数，不重放原任务写入，也不修改计划任务基线。

## 管理员 Schema 迁移

线上结构修改不是普通记录写入的一部分。普通 AI 不能通过兼容 API 直接调用任何写操作，也不能提供任意 `docid`、`sheet_id` 或字段定义。结构变更必须先登记为代码内置的、可审查的增量迁移，再按以下顺序执行：

1. 固定租户与登记键解析真实目标；
2. `wecom_schema_migration_preview` 只读比较线上现状与迁移目录；
3. `wecom_schema_migration_apply` 校验本机账号等于配置中的 `schema_admin_user`、显式管理员授权与 `preview_id`；
4. 单次应用后回读；预览过期、字段冲突、关联目标不一致或回读不完整时失败关闭；
5. 只有 Owner 另行授权后，才执行线上到本地 Schema 字典同步。

管理员身份使用操作系统本机账号，不使用出现在提示词、日志或表格里的明文口令。当前内置迁移包括：

- `zoop_subject_v1`：只创建 `Z-S09｜协作主体` 及其 16 个目录字段；
- `zoop_subject_links_v1`：只给 `Z-S01` 至 `Z-S07` 新增 11 个已登记的主体关联或会话标识字段。

两个迁移都不删除记录、不改型既有字段、不回填历史记录，也不自动更新本地 Schema 镜像。

字段编码验证表不承载业务数据。它的生命周期固定为：创建表 → 建字段与样本行 → 写入 `SMART_SHEETS_IDS` → 回读核验。登记失败会保留已创建文档作为待处理证据，但 MCP 会停止读取和后续写入，也不会悄悄另建一张表。

## 配置与运行

复制 [配置示例](config/zoop_wecom_zhycit.json.example) 为受保护的本地 `*.local.json`。已有 `SMART_SHEETS_IDS` 时填写其文档 ID；新实例可保持空字符串，并显式调用 `wecom_registry_bootstrap` 完成一次性创建、字段核验和配置写回。若启用管理员 Schema 迁移，填写本机管理员账号 `schema_admin_user` 并仅给 `schema_migration` 白名单组配置迁移所需 API。配置没有密钥；GNAS 凭据只放在 Codex 用户配置的环境变量中。

bootstrap 在调用企业微信创建接口前先创建本地排他哨兵。若进程中断或创建结果不确定，后续调用会停止并要求人工核对，不会自动创建第二张登记表。配置写回只允许从空值变为本次创建并核验的文档 ID，已有不同值时一律拒绝覆盖。

```toml
[mcp_servers.zoop_wecom_zhycit]
command = "$HOME/.codex/mcp/wecom-mcp-v2/bin/wecom-mcp-v2"
args = ["--config", "$HOME/.codex/mcp/wecom-mcp-v2/config/zoop_wecom_zhycit.local.json"]
startup_timeout_sec = 10
tool_timeout_sec = 20
enabled = true

[mcp_servers.zoop_wecom_zhycit.env]
GNAS_BASE_URL = "..."
GNAS_APP_ID = "..."
GNAS_APP_SECRET = "..."
```

修改 JSON 的 API 白名单或 Schema 镜像路径后，服务会在下一次调用加载新配置；无需重启 MCP。修改 Codex 的 `config.toml` 实例定义或替换二进制后，需要重新加载 MCP。

## 校验

```sh
go test ./...
go vet ./...
go build -o bin/wecom-mcp-v2 ./cmd/wecom-mcp-v2
```

便携安装、安装校验、版本/回滚规则及远端基线复现步骤见 [PORTABLE_INSTALL.md](PORTABLE_INSTALL.md)。许可证目前仅有待 Owner / 法务确认的 [Apache-2.0 工程候选](LICENSE-CANDIDATE.md)，尚未形成正式授权。
