# Contributing

感谢贡献。提交 Issue 或 PR 前，请先确认：

- 不包含凭据、token、个人数据、真实企业微信文档/表格 ID 或生产日志；
- 变更有对应测试或清楚说明为什么不适用；
- 安装器、配置和权限边界的变更同时更新文档；
- 不把 `configured`、`loaded`、`verified` 互相推导；
- 不在测试中访问或写入真实企业微信业务表。

## 本地验证

```sh
sh -n install.sh scripts/*.sh
go test ./...
go vet ./...
go build -trimpath ./...
./scripts/test-github-installer.sh
```

PR 描述请说明测试结果、影响范围以及是否涉及安装器、凭据处理或业务写入边界。
