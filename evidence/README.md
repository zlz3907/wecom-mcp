# 验证证据

本目录只保存实际执行结果，不代表发布、生产验证或 Owner 验收。

- `baseline-80a3f13-20260811T143055Z.txt`：在 `origin/main` 的 detached worktree 中执行，精确绑定远端目标 commit/tree，工作树干净。
- `r1-candidate-20260811T143439Z.txt`：在检出目标 commit 的本地 R1 未提交候选上执行，明确标记 dirty；其 `source_tree` 是 HEAD 的基线 tree，并不是包含未跟踪候选文件的 Git tree。因此这份文件只证明候选工作树当次测试，不把候选冒充为已提交内容。

后续 Executor 获得远端写入授权并形成新 commit 后，必须从该干净 commit 重新运行 `scripts/capture-verification.sh`，生成 `candidate_dirty=no` 且绑定新 commit/tree 的证据；不得复用本次候选证据替代提交后验证。

完整安装生命周期证据必须在新提交的干净 detached checkout 中用 `scripts/capture-portable-lifecycle.sh` 生成，并保存到源码树之外，避免生成证据本身污染被证明的 checkout。输出包含完整 tree 对象清单、安装 manifest、checksum 成功与篡改拒绝、版本切换和回滚回读。
