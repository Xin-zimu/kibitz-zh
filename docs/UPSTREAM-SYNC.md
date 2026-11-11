# 与上游同步（Upstream Sync）

上游是 [richardwooding/kibitz](https://github.com/richardwooding/kibitz)，它才是 kibitz 的正典。
本仓库（`kibitz-zh`）是一个 Fork，只叠加中文界面与本地修复；**任何不专属于中文/本次修复的改动，
都应该提交到上游，而不是长期停留在 Fork 里**。

本文只讲"如何跟进"，不代表本轮做过合并。

---

## 一、Remote 的用途

标准克隆（`git clone https://github.com/Xin-zimu/kibitz-zh.git`）后应有两个 remote：

| remote | 地址 | 用途 | 可否推送 |
|---|---|---|---|
| `origin` | `https://github.com/Xin-zimu/kibitz-zh.git` | 自己的 Fork，日常提交与发布 | ✅ 可以 |
| `upstream` | `https://github.com/richardwooding/kibitz.git` | 上游只读参考，用于比对与合并 | ❌ **永不推送** |

一次性配置（克隆后若只有 `origin`）：

```sh
git remote add upstream https://github.com/richardwooding/kibitz.git
git remote set-url --push upstream DISABLED   # 双保险：误执行 push upstream 会直接失败
git fetch upstream --tags
```

> 维护者本地工作副本里可能还有一个指向"上游本地镜像"的 `origin`，那是历史遗留的离线比对路径，
> 与公开仓库的约定无关。请以上表为准。

---

## 二、检查上游是否有更新

```sh
git fetch upstream main --no-tags                      # 只取分支，不碰 tag（见第六节）
git log --oneline HEAD..upstream/main | head -30     # 上游有、我们没有的提交
git rev-list --count HEAD..upstream/main             # 落后多少个提交
git log --oneline upstream/main..HEAD                # 我们独有的提交（应只含中文与修复）
git describe --tags --abbrev=0 upstream/main         # 上游最新 tag
```

需要某个具体的上游 tag 时按名取，不要整批拉：

```sh
git fetch upstream tag v0.33.0
```

判断是否需要跟进：

- 上游只有 CI/依赖/文档类提交 → 可以不跟；
- 涉及 `internal/`、`web/src/`、`go.mod` 的行为变更或安全修复 → 应跟进；
- 上游发布新 tag（如 v0.33.0）→ 记录基线，下一次定版时把 README 的"基线"与
  `docs/RELEASE-*.md` 一并更新。

---

## 三、评估冲突风险

先看**上游改了哪些文件**，再对照本 Fork 自己拥有的文件。重叠越多，越要逐块阅读。

```sh
# 上游这次改了什么
git diff --stat $(git describe --tags --abbrev=0 upstream/main)..upstream/main

# 我们自己改了什么（相对合并基点）
git diff --stat upstream/main...HEAD
```

本 Fork 重点维护的文件（合并后必须逐个人工确认）：

| 文件/目录 | 本 Fork 的改动 | 冲突风险 |
|---|---|---|
| `web/src/i18n.js`、`web/src/i18n-core.js` | 新增整套 i18n 机制与词典 | 上游若自行引入 i18n，需整体替换而非合并 |
| `web/src/*board.js`、`app.js`、`index.html` | 文案改为 `window.T(...)`；面板内重开；悔棋显隐 | 上游改同一段 UI 时冲突密集，**以语义为准**（保留上游逻辑，套上中文键） |
| `internal/service/gomokup/*` | 四项并发修复 + 竞态测试 | 上游同类修复若已合入，直接采用上游版本并删掉本地重复修复 |
| `internal/relaycfg/`、`internal/clientip/` | 反向代理下的每客户端限流 | 上游若改了限流模型，需重新对齐 nginx 建议值 |
| `internal/service/chess/chess.go` | 首手悔棋（PGN `*` 视为开局） | 小改动，冲突时保留语义即可 |
| `.github/workflows/`、`.goreleaser.yaml` | 未改（沿用上游） | 见第六节：发布流水线在 Fork 上需要额外处理 |

评估结论写进提交信息或发布说明：**采纳上游 / 保留本地 / 两者合并**，避免下次再猜。

---

## 四、安全合并流程

```sh
git switch -c sync/upstream-$(date +%Y%m%d)     # 1. 独立分支，别在 main 上试
git fetch upstream --tags
git merge --no-ff upstream/main                 # 2. 合并（或先 git log 评审再合并）

# 3. 冲突处理原则
#    - 逻辑/协议/引擎：以上游为准
#    - 文案：保留 window.T(...)，把上游新增的英文串补进 i18n-core.js
#    - 自己独有的修复：若上游已修，删除本地版本，不要留双份

# 4. 本地验证
go build ./... && go vet ./...
go test ./internal/service/gomokup/ ./internal/service/chess/ ./internal/relaycfg/ ./internal/clientip/
make wasm && make serve      # 起本地实例点一遍

# 5. 中文文案完整性（每个键应恰好出现两次：en + zh）
grep -o '"[a-z0-9_.]*":' web/src/i18n-core.js | sort | uniq -c | awk '$1 != 2'

# 6. 合并到 main 并推送自己的 Fork
git switch main && git merge --no-ff sync/upstream-$(date +%Y%m%d)
git push origin main
```

合并后至少手工点一遍：首页语言切换 → 五子棋（含观战与重开）→ 五子棋派对（入座/重开）→
国际象棋对电脑与真人悔棋 → 一个"其他游戏"随机抽查。发现问题就回退该合并分支，不要在 main 上修。

### 回退一次合并

```sh
git switch main
git revert -m 1 <merge-commit>   # 生成一次反向提交，历史保留
```

---

## 五、把修复回馈上游

本地修复若与中文无关（例如 `gomokup` 的并发修复、`clientip` 限流、国际象棋首手悔棋），
建议向上游提 PR：

1. 从 `upstream/main` 切一个干净分支（不要带上 i18n 提交）；
2. 只挑相关提交：`git cherry-pick <sha>`；
3. 跑 `make test`，按上游的提交信息风格与 PR 模板提交；
4. 上游合入后，下一次同步时删除本 Fork 里的重复实现。

这样能避免 Fork 与上游长期分叉、每次合并都要重新解冲突。

---

## 六、注意（Fork 专属）

- **不要** `git push upstream`；已通过 `set-url --push upstream DISABLED` 阻断。
- **上游专属工作流已加仓库归属保护**：`.github/workflows/release.yml`（tag 触发的 goreleaser，
  会向 `ghcr.io/richardwooding/kibitz` 推镜像）与 `pages.yml`（发布 `docs/` 为静态站）的 job 都带
  `if: github.repository == 'richardwooding/kibitz'`，在 Fork 上推送 tag 或改动 `docs/**` 时会被
  跳过（显示为 skipped，不会失败）。文件本身保留，便于同步上游时不产生冲突。
- 因此本仓库可以正常 `git push origin v0.1.0` 打标：只有 `ci.yml` 会在 `main` 上跑测试，
  发布 Release 需手动创建（`gh release create`）。
- **标签同名提示**：上游历史里也有一个 `v0.1.0`（2026-07-23 的极早期版本），与本 Fork 的
  `v0.1.0` 毫无关系。本地若同时存在两个远程的 tag，`git fetch --tags` 会报 "would clobber
  existing tag" —— 这是预期的：日常同步用 `git fetch upstream main --no-tags`，需要上游某个
  tag 时按名取（`git fetch upstream tag v0.33.0`），不要整批覆盖。
