## Summary

<!-- 不要提交凭据、token、个人数据、真实文档/表格 ID 或生产日志。 -->

## Validation

- [ ] `sh -n install.sh scripts/*.sh`
- [ ] `go test ./...`
- [ ] `go vet ./...`
- [ ] `go build -trimpath ./...`
- [ ] `./scripts/test-github-installer.sh`

## Safety boundary

- [ ] 未改变 configured/loaded/verified 的证据含义
- [ ] 未访问或写入真实企业微信业务表
- [ ] 安装器/凭据/权限变更已在描述中说明
