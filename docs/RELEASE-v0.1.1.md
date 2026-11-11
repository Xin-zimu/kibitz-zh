# Kibitz 中文版 v0.1.1

**发布对象**：`Xin-zimu/kibitz-zh`
**版本**：`v0.1.1`（维护补丁；版本序列独立于上游）
**类型**：无行为变更的维护更新
**上一个版本**：`v0.1.0`

---

## 一、这个版本做了什么

只有一件事：**让仓库的 CI 重新变绿**。

`v0.1.0` 之后，CI 一直停在 `go fix (check)` 这一步失败——不是测试挂了，而是 Go 1.27 的现代化
检查要求把三处旧写法改成新语法，`go fix -diff` 非空即判失败。此前（自本 Fork 第一个提交起）
每次推送都是同样的红色，与任何功能改动无关。

按"不关闭检查、不掩盖问题"的原则，本版本直接应用这些改造：

| 文件 | 改动 | 性质 |
|---|---|---|
| `internal/clientip/clientip.go` | `for _, raw := range strings.Split(spec, ",")` → `for raw := range strings.SplitSeq(spec, ",")` | 运行时代码，语义等价 |
| `internal/clientip/ratelimit_acceptance_test.go` | `for i := 0; i < 8; i++` → `for i := range 8` | 测试 |
| `internal/integration/gomokup_four_test.go` | 两处 `for` 计数循环 → `for … := range N` | 测试 |

共 3 个文件、4 行，**没有逻辑修改**：`strings.SplitSeq` 返回迭代器而非切片，遍历内容与顺序
完全一致，且不再为短字符串分配切片；`range int` 与 `for i := 0; i < N; i++` 等价。

---

## 二、兼容性

- 游戏规则、联机协议、加密、界面文案、配置项：**全部未变**；
- 与 `v0.1.0` 的对局互通性不受影响（协议层无改动）；
- 线上二进制已重建并替换，`go version -m` 可核对源码提交与 `vcs.modified=false`。

---

## 三、验证

| 项 | 结果 |
|---|---|
| `go fix -diff ./...` | 无剩余差异 |
| `go test ./...` | 全部通过（含 clientip、integration、13 个游戏服务、bot、relaycfg） |
| `go vet ./...` | 干净 |
| WASM 编译（`GOOS=js GOARCH=wasm`） | 通过 |
| Linux 二进制重建（`amd64`, `-trimpath -s -w`） | 通过，sha256 见发布说明 |
| 线上部署 | 备份上一版 → 替换 → 重启 → `/healthz` 200，冒烟见下 |

部署后冒烟（真实公网访问）：首页加载、13 张游戏卡片、单人国际象棋一着往返、两人五子棋
开局与结束后原地重开。

---

## 四、升级与回滚

```sh
# 升级
install -m 0755 /srv/kibitz/bin/kibitz /srv/kibitz/bin/kibitz.prev
install -m 0755 kibitz-v0.1.1 /srv/kibitz/bin/kibitz
systemctl restart kibitz.service
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8081/healthz

# 回滚
install -m 0755 /srv/kibitz/bin/kibitz.prev /srv/kibitz/bin/kibitz
systemctl restart kibitz.service
```

回滚目标同时保留在 `/root/kibitz-backup-<时间戳>/kibitz`（见 `docs/MAINTENANCE.md` 第三节）。

---

## 五、说明

`v0.1.0` 的 Tag 与 Release 未做任何修改（不移动、不删除、不重建）。本版本是一个新的提交与
新的 Tag，历史保持线性可追溯。
