# 便携安装、校验、版本与回滚

## 前提与边界

- Go 1.23 或更高兼容版本；POSIX shell；本地已有仓库检出。
- 安装脚本只构建本地源码并写入指定 `--prefix`，不联网、不读取或复制凭据、不修改 Codex 配置。
- 配置模板仅作为示例保存。真实 `*.local.json` 与 `GNAS_*` 环境变量由安装目标环境另行管理。
- 本流程不依赖 GitHub Release、包注册表或公开仓库。

## 复现远端基线

目标基线固定为：

```text
commit 80a3f13f47c8524ad97323859741fc320f33b48c
tree   104206c41571341492c0dfe2115ec1f146853385
```

非破坏性获取并在新目录检出（不会移动或清理当前工作树）：

```sh
git fetch --no-tags origin main
test "$(git rev-parse origin/main)" = 80a3f13f47c8524ad97323859741fc320f33b48c
test "$(git rev-parse origin/main^{tree})" = 104206c41571341492c0dfe2115ec1f146853385
git worktree add --detach ../wecom-mcp-baseline-80a3f13 origin/main
```

若目标目录已存在，选择另一个空目录；不得用 `reset`、`clean` 或覆盖现有工作树。

## 安装与校验

在候选源码根目录执行：

```sh
./scripts/install-portable.sh --prefix /absolute/path/to/wecom-mcp-install
./scripts/verify-portable-install.sh --prefix /absolute/path/to/wecom-mcp-install
```

生命周期验证需要安装一个旧的干净 checkout 时，可由当前受控脚本显式指定源码；manifest 仍记录被安装源码的 commit/tree：

```sh
./scripts/install-portable.sh \
  --prefix /absolute/path/to/wecom-mcp-install \
  --source /absolute/path/to/clean/previous-checkout
```

安装结果采用不可变版本目录与 `current` 符号链接：

```text
<prefix>/
  current -> releases/<version>
  releases/<version>/
    bin/wecom-mcp-v2
    config/zoop_wecom_zhycit.json.example
    INSTALL-MANIFEST.txt
```

脚本拒绝相对 prefix。它不会创建本地密钥配置；操作者应复制示例到 prefix 之外的受保护位置，再自行填入非密钥路由配置。密钥值只能由运行环境注入。

## 版本规则

- 干净提交：`git-<12位commit>`，manifest 同时记录完整 commit 与 tree。
- 有本地候选改动：`candidate-<UTC时间>-tree-<12位基线tree>`，manifest 记录基线 commit/tree、`candidate_dirty=yes`，以及 tracked diff 与各 untracked 文件内容形成的 SHA-256 摘要。
- 同一 version 目录已存在时安装失败，不覆盖历史版本。重新安装须产生新版本或由人工选择新的空 prefix。
- 正式版本号或 tag 只能在 Owner 另行批准发布流程后定义；本规则不创建 tag 或 Release。

## 回滚规则

回滚不依赖远端 Release，只切换本地 `current` 链接：

```sh
./scripts/rollback-portable.sh \
  --prefix /absolute/path/to/wecom-mcp-install \
  --version git-0123456789ab
./scripts/verify-portable-install.sh --prefix /absolute/path/to/wecom-mcp-install
```

回滚目标必须已存在且通过结构校验；脚本不会删除当前或历史版本。切换前应停止使用该 MCP 的进程，切换后重新加载 MCP 并执行安装校验。配置与幂等状态应保存在版本目录之外，因此二进制回滚不会回滚业务数据；若新旧版本存在配置或状态格式迁移，必须先按对应变更说明执行专门回滚，不能只切链接。

## 可保存验证证据

`scripts/capture-verification.sh` 在指定源码树运行 `go test`、`go vet`、`go build`，并把命令、UTC 时间、结果、源码 commit/tree、工作树状态与输出保存为文本。证据脚本不会伪造或回填历史执行：

```sh
./scripts/capture-verification.sh --output /absolute/path/to/evidence.txt
```

对远端目标 commit 的证据应在上述 detached worktree 内执行；对未提交候选的证据会明确标记 dirty，不能声称绑定于远端 commit。

形成新提交后，应从该提交的干净 detached checkout 运行完整生命周期证据，并把输出与安装目录写到源码树之外：

```sh
./scripts/capture-portable-lifecycle.sh \
  --previous-source /absolute/clean/previous-checkout \
  --prefix /absolute/new/install-path \
  --output /absolute/new/evidence.txt
```

脚本拒绝非干净 checkout、已存在的安装目录和已存在的证据文件。它记录当前提交的完整 Git tree 对象清单，并实际执行 `go test`、`go vet`、`go build`、前后两个版本安装与 manifest 回读、checksum 成功校验、配置样例篡改拒绝、恢复后校验、回滚及再次切回。该证据只证明指定本地安装前缀内的执行结果，不代表 Release、部署、生产验证、秘密扫描或供应链审计。
