# kibitz-zh — Kibitz 中文版

> **本仓库是 [richardwooding/kibitz](https://github.com/richardwooding/kibitz) 的中文 Fork。**
> 原始设计、全部游戏引擎与协议实现均为 Richard Wooding 的作品，MIT 许可（见 [LICENSE](LICENSE)）。
> 本 Fork 只做两件事：**简体中文界面**，以及**修复若干并发/交互缺陷**；不专属于中文与这些修复的
> 改动应当回馈上游。如果你要的是 kibitz 本身，请去上游仓库。

一句话：`kibitz` 是 [croc](https://github.com/schollz/croc) 式的配对方式，用来开长期、
端到端加密的**聊天 + 13 款回合制桌游**会话。主持人拿到一个口令（如 `lion-42-maple`）和
分享链接，朋友点开或输入口令即可加入；中转服务器（relay）只转发密文，读不到任何内容。

---

## 一、与上游的差异

基线：上游 `main@5897967`（即 `v0.32.0` 之后 3 个提交）。本 Fork 在其上增加了 13 个提交。

### 1. 简体中文界面

上游客户端原本没有任何 i18n 机制，这里补了一套：

- 每种语言 211 条键值，**默认简体中文**，英文完整保留，可在首页「语言 / Language」下拉切换并记忆；
- 覆盖全部 13 款游戏的标题、状态行、按钮、提示与 Toast，不只是落地页；
- 关键约束：棋盘比较用的结果字符串（`"black wins"`、`"draw"` 等）是**协议值而非文案**，
  一律不改，只在渲染边界映射为中文，因此不会与引擎状态失配；
- 着法记录、PGN、房间口令等语言中立内容保持原样。

### 2. 多人五子棋（Gomoku Party）并发缺陷

`internal/service/gomokup` 的广播-回滚协议里有四处缺陷，均已在本地复现并修复，含竞态测试：

- `gen` 计数器只统计本地动作，所有入站路径写入状态却不递增，导致回滚看不到对端的写入，
  用旧快照覆盖掉对端刚建立的新局或座位表；
- `ring.Departed` 之前会被快照/恢复，导致「离开」这一单调事实被回滚抹掉，之后的新局把已离开的
  玩家重新排进座位；
- 广播失败时本地状态不回滚，与对端分叉；
- 房主迁移时未清理已离开的旧房主，新宿主无法开局。

一处反直觉但必要的语义：**离开故意不递增 `gen`**。本地落子先在本地推进回合再广播，中途到达的
离开如果按「新回合」判断就会与任何对端都不一致；让回滚照常发生、再把离开重放一遍，才是两端收敛的
唯一方式。`service.go` 的注释写明了原因，`race_test.go` 用测试钉住了它。

### 3. Relay 反向代理限流修复

上游 relay 的每客户端限流按 `r.RemoteAddr` 分桶，且刻意不读 `X-Forwarded-For`（它假定自己直接
暴露在公网）。放在 nginx 之后时，所有玩家都来自代理的回环地址，于是**全站共用一个桶**：第 6 个
打开页面的人就会拿到 HTTP 429。

修复：`internal/clientip` 在限流器之前把 `RemoteAddr` 改写为 nginx 上报的真实客户端地址；
`internal/relaycfg` 把出厂限额收敛为**唯一事实来源**，二进制与验收测试共用同一份数值
（默认每 2 秒 1 次连接、burst 20、会话上限 1000），降低限额会让测试失败而不是悄悄上线。

### 4. 对局结束后的交互修复（v0.1.0）

- **五子棋 / 五子棋派对**：一局结束后可在**原房间、原面板**直接「再来一局」，不再需要退回游戏
  列表重建房间；无开局权限的观战者会看到明确的等待提示，而不是以为只能退出。
- **国际象棋**：悔棋是双方协商，对电脑或同屏双人时没有任何一方能接受，因此不再显示该按钮
  （避免"点了没反应"）；共享悔棋条固定预留一行高度，按钮出现/消失不再推动棋盘、造成页面抖动。
- **国际象棋悔棋**：修复了「撤回全局第一手」静默失效的上游缺陷（开局 PGN 为 `*` 时回退被跳过，
  提议被消耗、棋子留在棋盘上）。

未改动：其余 12 款游戏的规则、联机协议、模块结构与加密实现。

---

## 二、13 款游戏

一局游戏从大厅「游戏选择」里按需开始，多款游戏可在同一会话中并行；再来一局会交换/轮转先后手。

| # | 游戏 | 人数 | 对电脑 | 说明 |
|---|------|------|--------|------|
| 1 | 国际象棋 Chess | 2 | ✅ | 完整规则、升变、和棋、PGN 复制 |
| 2 | 双陆棋 Backgammon | 2 | ✅ | 承诺-揭示骰子，作弊可检测 |
| 3 | 四子棋 Connect Four | 2 | ✅ | — |
| 4 | 五子棋 Gomoku | 2 | ✅ | 15×15，五连即胜 |
| 5 | 五子棋派对 Gomoku Party | 2–4 | ✅ 1–3 个电脑 | 19×19，多人轮转，带座次大厅 |
| 6 | 六角棋 Hex | 2 | ✅ | — |
| 7 | 点格棋 Dots & Boxes | 2 | ✅ | — |
| 8 | 围棋 Go | 2 | ✅ | — |
| 9 | 象棋 Xiangqi | 2 | ✅ | — |
| 10 | 金拉米 Gin Rummy | 2 | ❌ | "心理扑克"洗牌，双方共同洗牌、摊牌可验证 |
| 11 | 跳棋 Checkers | 2 | ✅ | — |
| 12 | 黑白棋 Reversi | 2 | ✅ | — |
| 13 | 海战棋 Battleship | 2 | ✅ | 逐格承诺-揭示，终局互相验证整张布阵 |

观战：任意会话都可被观战，观战者与玩家共用同一加密聊天，但不能落子、开局或重开。

---

## 三、中文 / 英文切换

- 首页「语言 / Language」下拉：`简体中文`（默认） / `English`；
- 选择写入 `localStorage`，刷新后保持；首次访问默认中文（`<html lang="zh-CN">`）；
- 切换即时生效，静态文案、面板状态行、按钮与 Toast 一并重绘，**不影响进行中的对局**；
- 协议值（结果字符串、着法、PGN）不参与翻译。

---

## 四、本地构建

### 环境要求

| 项 | 要求 | 说明 |
|---|---|---|
| Go | **≥ 1.27**（见 `go.mod` 的 `go 1.27.0`） | 编译 relay 与 WASM 客户端 |
| 操作系统 | Linux / macOS / Windows 均可 | WASM 由 Go 自带工具链产出 |
| Node / tinygo | **不需要** | `wasm_exec.js` 取自 `$(go env GOROOT)/lib/wasm/` |
| make | 可选 | 也可按下文手动执行 |

### 常用命令

```sh
make wasm     # 复制 web/src → web/dist，编译 WASM，并预压缩 .br/.gz
make serve    # 构建后在本机 :8080 启动
make test     # go test -race ./...（含 headless 集成测试）
make lint     # go vet + golangci-lint（与 CI 一致）
```

**关键点**：Web 客户端通过 `go:embed web/dist` 编进二进制，静态资源还会预压缩成 `.br`/`.gz`
（服务端优先发送压缩副本）。因此**改完前端必须重新 `make wasm` 再编译二进制**，否则线上仍是
旧资源；只替换 `web/dist` 下的 `.js` 而留下旧的 `.br` 同样会被浏览器命中旧版本。

手动等价步骤（无 make）：

```sh
cp web/src/* web/dist/
cp "$(go env GOROOT)/lib/wasm/wasm_exec.js" web/dist/
GOOS=js GOARCH=wasm go build -trimpath -ldflags="-s -w" -o web/dist/kibitz.wasm ./cmd/kibitz-wasm
go run ./cmd/compress-assets web/dist
go build -trimpath -ldflags="-s -w" -o kibitz ./cmd/kibitz   # 本机平台
```

交叉编译到 Linux 服务器（在任意平台均可）：

```sh
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o kibitz ./cmd/kibitz
```

---

## 五、部署

产物是**单个二进制**，内含 relay、Web 客户端与全部静态资源；房间状态在内存中，不需要数据库。

```sh
./kibitz --listen 127.0.0.1:8080        # 只监听回环，交给反向代理
./kibitz --listen :8080 --max-sessions 200
./kibitz --version
```

### systemd（示例，按需调整路径与用户）

```ini
[Unit]
Description=Kibitz game relay
After=network-online.target
Wants=network-online.target

[Service]
User=kibitz
Group=kibitz
ExecStart=/srv/kibitz/bin/kibitz --listen 127.0.0.1:8081 --trusted-proxies 127.0.0.0/8,::1/128
Restart=on-failure
RestartSec=2s
NoNewPrivileges=yes
PrivateTmp=yes
ProtectSystem=strict
ProtectHome=yes
RestrictAddressFamilies=AF_INET AF_INET6
MemoryDenyWriteExecute=yes

[Install]
WantedBy=multi-user.target
```

```sh
systemctl daemon-reload && systemctl enable --now kibitz.service
systemctl status kibitz.service
journalctl -u kibitz.service -f
```

`--trusted-proxies` 必须只包含**本机反向代理**的地址：它决定哪些 `X-Forwarded-For` 被采信，
直接对外暴露时不要放宽。

### Nginx 反向代理要求

| 要求 | 原因 |
|---|---|
| `/` → `http://127.0.0.1:<port>` | 普通页面与静态资源 |
| `/ws` 转发并设置 `Upgrade` / `Connection` 升级头 | 实时会话走 WebSocket |
| `proxy_read_timeout` 调大（如 3600s） | 会话是长连接，默认 60s 会被切断 |
| `proxy_set_header X-Forwarded-For $remote_addr`（**覆盖式**） | 限流按真实客户端 IP 分桶；用追加式会把伪造值带进来 |
| `limit_req` 与 `internal/relaycfg` 保持一致 | 边缘限流是主防线，应用内限额是兜底；两者不自洽会出现"nginx 放行、应用 429" |
| 启用 TLS 时同时转发 `wss` | 客户端按页面地址推导 WebSocket 地址，HTTPS 页面必须配 `wss` |

健康检查：`GET /healthz` 返回 200。

---

## 六、更新与回滚

```sh
# 更新：保留上一版，替换二进制后重启
install -m 0755 kibitz.new /srv/kibitz/bin/kibitz.prev.tmp && \
  install -m 0755 /srv/kibitz/bin/kibitz /srv/kibitz/bin/kibitz.prev && \
  install -m 0755 kibitz.new /srv/kibitz/bin/kibitz
systemctl restart kibitz.service
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8081/healthz

# 回滚：换回备份并重启
install -m 0755 /srv/kibitz/bin/kibitz.prev /srv/kibitz/bin/kibitz
systemctl restart kibitz.service
```

因为客户端嵌在二进制里，**回滚二进制即回滚前端**，不需要单独处理静态资源。
重启会断开所有进行中的会话（房间状态不落盘）。

---

## 七、已知问题与限制

- **无持久化**：会话与对局状态只在内存，进程重启即丢失；没有账号、没有历史记录。
- **默认 HTTP**：加密发生在应用层（PAKE + 组密钥，relay 只见密文），但页面与静态资源本身是明文，
  除非在前面加 TLS。生产部署建议启用 HTTPS。
- **观战者不能开局/重开**：这是服务端 `AuthorizeStart` 的规则（只有房主与被排座的对手可以），
  界面只提供等待提示，不放宽权限。
- **悔棋只在真人联机对局中提供**：对电脑或同屏双人时没有能接受提议的对端，因此不显示该按钮；
  悔棋只回退一手，需对方接受。
- **金拉米没有电脑对手**，只能两真人联机（庄家less 洗牌需要双方在线）。
- **海战棋不支持同屏对战**（秘密布阵），但支持对电脑。
- **限流**：连接按真实客户端 IP 分桶（默认每 2 秒 1 次、burst 20、会话上限 1000）。同一 NAT 出口
  的大型活动需同时调高 `relaycfg` 与 nginx `limit_req`。
- 房间口令即密钥：口令泄露等于会话泄露，转发分享链接前请确认对象。

---

## 八、上游同步与维护

- 上游同步流程（remote 用途、检查更新、冲突评估、安全合并）：[`docs/UPSTREAM-SYNC.md`](docs/UPSTREAM-SYNC.md)
- 日常维护清单（状态、日志、备份、更新回滚、资源检查、HTTPS 升级）：[`docs/MAINTENANCE.md`](docs/MAINTENANCE.md)
- 版本说明：[`docs/RELEASE-v0.1.0.md`](docs/RELEASE-v0.1.0.md)
- 威胁模型：[`docs/THREAT-MODEL.md`](docs/THREAT-MODEL.md)

---

## 九、License 与致谢

MIT（见 [LICENSE](LICENSE)）。原始版权归 **Richard Wooding** 所有。
棋类引擎分别来自 `corentings/chess`、`richardwooding/{backgammon,checkers,reversi,weiqi,
xiangqi,fairdice,mentalpoker,ginrummy,shipcommit,parley}` 等项目，同样遵循各自许可。

---

## English

`kibitz-zh` is a Simplified-Chinese fork of [richardwooding/kibitz](https://github.com/richardwooding/kibitz)
(MIT). It adds a Chinese UI (default `zh`, English switchable), fixes four concurrency defects in the
four-player Gomoku Party service, repairs per-client rate limiting behind a reverse proxy, and makes
finished games rematchable in place. Game rules, the wire protocol and the E2EE core are unchanged;
upstream remains the canonical project and the reference documentation.
