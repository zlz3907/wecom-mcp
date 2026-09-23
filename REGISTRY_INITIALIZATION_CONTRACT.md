# SMART_SHEETS_IDS 初始化完成契约

创建索引表不是完成初始化。`wecom_registry_bootstrap` 与完整实例初始化共享以下规则；不得依赖调用者额外提醒或后续人工补行。

1. 验证固定租户、操作者及该文档管理员权限；只续接相同文档与初始化状态。
2. 全量读取字段和记录。对 journal 能证明由本次创建的文档，识别完整平台默认字段模板；确认没有数据或属性改动，保存本地受保护快照。
3. 复用默认文本主字段作为 `registry_key`。旧版已经额外添加同名标准列时，仅删除该空的重复目标列；不尝试删除主字段。清理已确认的其他默认空列与空记录。
4. 补齐并回读标准字段。实际字段总数必须恰好20，不能只统计“找到20个要求字段”。任何未知字段、字段/记录格式异常、分页不全或非空内容均停止自动清理。
5. 用 `smart_sheets_ids_registry_v1` 唯一登记自身，`document_role=registry`、`type=smart_sheet`、`doc_type=10`、`lifecycle_status=active`。真实 docid、真实创建/分享 URL、固定 Source、创建及变更时间、操作者、Registry schema version/revision 与最终字段指纹均需写入并回读。此保留键不得用作实例业务 `registry_key`。
6. `add_records` 前持久化 self journal。响应不确定时只读取同一目标恢复；不可盲目再次新增。正常重跑不新增记录、不改时间、不递增 revision。内容冲突、重复自身行或回执不匹配均停止。
7. 持久化配置前后核验自身行与结构，再标记 verified/ready。旧版 owned bootstrap 已有 docid 时执行同一核验及修复；无创建证明的导入文档不自动清理。

当前可清理的默认模板是原样的“文本/单选/人员/数字/日期”列及对应原始属性，以及最多5条完全空白记录；具体结构以代码的严格比对及线上回读为准，不是按中文名称批量删除的规则。仅发现自定义或已修改数据时不扩大删除范围。

标准字段：`business_domain`、`created_at`、`created_by`、`doc_type`、`docid`、`document_role`、`last_change_at`、`last_change_by`、`last_change_reason`、`last_verified_at`、`lifecycle_status`、`mcp_source`、`name`、`notes`、`registry_key`、`registry_revision`、`schema_fingerprint`、`schema_version`、`type`、`url`。

专用 bootstrap capability 见配置示例。完整初始化的分享 URL 查询需要 `get_doc_share_url`；默认空列清理需要 `delete_fields`。初始化工具不会自行扩大实例白名单。

回归覆盖：原始默认模板、旧版25列5空行修复、主字段保留、标准20列实际计数、自登记唯一性、业务路由隔离、幂等重跑、未决新增恢复、未知/非空模板拒绝、分页失败、导入文档保护、完整实例 ready 条件。

线上 API 字段删除/改名形态依据企业微信团队维护的[智能表格接口说明](https://github.com/WecomTeam/wecom-unified/blob/main/skills/wecom-unified/references/wecom-smartsheet.md)，实际写入仍须绑定固定实例、受控初始化所有权和回读结果。
