# 旧 MCP 企业微信 API 移植矩阵

本矩阵以旧 MCP 的 `SMARTSHEET_OPERATIONS` 和成员目录调用为唯一来源。Go 版保留固定租户、API 白名单和响应去敏；不包含 GNAS JWT 获取接口，因为它是受管传输认证，不是企业微信业务 API。

| 编号 | Go operation | 方法 | 企业微信路径 | 类别 |
| --- | --- | --- | --- | --- |
| 01 | `list_employees` | GET | `/cgi-bin/user/list?department_id=1&fetch_child=1` | 成员目录读取 |
| 02 | `get_doc_base_info` | POST | `/cgi-bin/wedoc/get_doc_base_info` | 文档读取 |
| 03 | `get_doc_share_url` | POST | `/cgi-bin/wedoc/doc_share` | 文档读取 |
| 04 | `get_doc_auth` | POST | `/cgi-bin/wedoc/doc_get_auth` | 文档读取 |
| 05 | `get_sheets` | POST | `/cgi-bin/wedoc/smartsheet/get_sheet` | 子表读取 |
| 06 | `get_views` | POST | `/cgi-bin/wedoc/smartsheet/get_views` | 视图读取 |
| 07 | `get_fields` | POST | `/cgi-bin/wedoc/smartsheet/get_fields` | 字段读取 |
| 08 | `get_records` | POST | `/cgi-bin/wedoc/smartsheet/get_records` | 记录读取 |
| 09 | `create_smartsheet` | POST | `/cgi-bin/wedoc/create_doc` | 文档写入 |
| 10 | `rename_document` | POST | `/cgi-bin/wedoc/rename_doc` | 文档写入 |
| 11 | `lock_down_doc_access` | POST | `/cgi-bin/wedoc/mod_doc_join_rule` | 文档权限写入 |
| 12 | `grant_doc_readers` | POST | `/cgi-bin/wedoc/mod_doc_member` | 文档权限写入 |
| 13 | `harden_doc_security` | POST | `/cgi-bin/wedoc/mod_doc_safty_setting` | 文档安全写入 |
| 14 | `add_sheet` | POST | `/cgi-bin/wedoc/smartsheet/add_sheet` | 子表写入 |
| 15 | `update_sheet` | POST | `/cgi-bin/wedoc/smartsheet/update_sheet` | 子表写入 |
| 16 | `delete_sheet` | POST | `/cgi-bin/wedoc/smartsheet/delete_sheet` | 子表写入 |
| 17 | `add_view` | POST | `/cgi-bin/wedoc/smartsheet/add_view` | 视图写入 |
| 18 | `update_view` | POST | `/cgi-bin/wedoc/smartsheet/update_view` | 视图写入 |
| 19 | `delete_views` | POST | `/cgi-bin/wedoc/smartsheet/delete_views` | 视图写入 |
| 20 | `add_fields` | POST | `/cgi-bin/wedoc/smartsheet/add_fields` | 字段写入 |
| 21 | `update_fields` | POST | `/cgi-bin/wedoc/smartsheet/update_fields` | 字段写入 |
| 22 | `delete_fields` | POST | `/cgi-bin/wedoc/smartsheet/delete_fields` | 字段写入 |
| 23 | `add_records` | POST | `/cgi-bin/wedoc/smartsheet/add_records` | 记录写入 |
| 24 | `update_records` | POST | `/cgi-bin/wedoc/smartsheet/update_records` | 记录写入 |
| 25 | `delete_records` | POST | `/cgi-bin/wedoc/smartsheet/delete_records` | 记录写入 |

## 调用方式

所有 25 项通过 `wecom_api_call` 暴露。调用参数只允许 `operation` 与 `payload`；实例白名单决定某项是否启用。`get_sheet` 是旧 Go 草案留下的内部兼容别名，不是第二个企业微信 API。

`create_smartsheet` 的成功回执必须保留企业微信原始 `docid` 与 `url`，不得以本地规则拼接访问链接。

## 受管应用消息扩展

`send_app_message` 不属于旧 MCP 的 25 项兼容矩阵，也不进入通用 `wecom_api_call`。它只通过独立的
`wecom_send_app_message` 工具开放：调用方提供一个已启用成员的 `userid`、UTF-8 文本与幂等键；
GNAS 从同一份加密自建应用凭据注入 `agentid`，MCP 不接受群发、租户、路由、凭据或调用方指定的
应用身份。只有企业微信返回 `errcode=0`、无无效接收人且存在 `msgid` 时才完成幂等状态。

图片和普通文件通过独立的 `wecom_send_app_media_message` 工具开放，不进入通用
`wecom_api_call`。调用方提供一个启用成员 `userid`、`image|file`、安全文件名、规范 Base64、
内容 SHA-256 与幂等键；MCP 通过 GNAS 受管 multipart 执行器调用 `/cgi-bin/media/upload`，取得
有效 `media_id` 后再调用 `/cgi-bin/message/send`。上传或发送结果不确定时保留幂等占位，禁止
盲目重试。该工具不接受 URL、部门、标签、群聊或全员目标。
