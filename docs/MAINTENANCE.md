# 维护清单（Maintenance）

面向"一个人也能长期跑下去"的最小运维清单：不依赖监控平台、不引入自动化系统，全部为可直接执行的命令。
下文用占位符表示自己的环境，请按实际替换。

| 占位符 | 含义 | 示例 |
|---|---|---|
| `<host>` | 服务器地址 | `server.example.com` |
| `<port>` | relay 监听端口（仅回环） | `8081` |
| `/srv/kibitz` | 部署目录 | 二进制在 `/srv/kibitz/bin/kibitz` |
| `kibitz.service` | systemd 单元名 | — |

---

## 一、日常检查（每周一次即可）

```sh
systemctl is-active kibitz.service && systemctl is-enabled kibitz.service
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:<port>/healthz    # 期望 200
systemctl status kibitz.service --no-pager | head -12
journalctl -u kibitz.service --since '7 days ago' --no-pager | grep -Eic 'panic|fatal'   # 期望 0
```

对外可达性（从外部网络执行，确认反向代理与证书均正常）：

```sh
curl -sI https://<host>/ | head -3
```

---

## 二、日志

```sh
journalctl -u kibitz.service -f                  # 实时
journalctl -u kibitz.service -n 200 --no-pager   # 最近 200 行
journalctl -u kibitz.service --since today | grep -Ei 'error|warn|panic'
```

关注点：`panic`、`connection lost` 大量出现、`429`（限流）、进程重启次数。
客户端异常在浏览器控制台，服务端日志里看不到——用户报障时先问浏览器 Console 是否有 `pageerror`。

反向代理日志（若使用 nginx）：`/var/log/nginx/<site>.access.log`、`<site>.error.log`。

---

## 三、备份与版本记录

**只有二进制需要备份**：前端（WASM + JS/CSS）都嵌在二进制里，回滚二进制即回滚界面。

```sh
BK=/root/kibitz-backup-$(date +%Y%m%d-%H%M)
mkdir -p "$BK" && cp -a /srv/kibitz/bin/kibitz "$BK/kibitz"
echo "$BK" > /root/.last-kibitz-backup
sha256sum "$BK/kibitz"
```

版本记录表（建议直接维护在服务器上的一个小文件里）：

| 日期 | 版本/tag | 提交 | sha256(前 12 位) | 备份路径 | 备注 |
|---|---|---|---|---|---|
| | | | | | |

---

## 四、更新

```sh
# 1) 本地从源码构建（交叉编译到服务器平台）
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o kibitz ./cmd/kibitz

# 2) 上传（先落 /tmp，校验后再安装）
scp kibitz <host>:/tmp/kibitz-new
ssh <host> 'sha256sum /tmp/kibitz-new'        # 与本地比对

# 3) 备份 → 安装 → 重启
ssh <host> '
  BK=/root/kibitz-backup-$(date +%Y%m%d-%H%M); mkdir -p "$BK"
  cp -a /srv/kibitz/bin/kibitz "$BK/kibitz"; echo "$BK" > /root/.last-kibitz-backup
  install -m 0755 /srv/kibitz/bin/kibitz /srv/kibitz/bin/kibitz.prev
  install -m 0755 /tmp/kibitz-new /srv/kibitz/bin/kibitz
  systemctl restart kibitz.service && sleep 2
  systemctl is-active kibitz.service
  curl -s -o /dev/null -w "%{http_code}\n" http://127.0.0.1:<port>/healthz
  rm -f /tmp/kibitz-new
'
```

更新后 30 秒内做一次真实开局（手机 + 电脑各一）比看日志更有效：能同时验证反向代理、WebSocket、
WASM 加载与加密握手。

**注意**：重启会断开所有进行中的会话（状态不落盘）。尽量选低峰期。

---

## 五、回滚

```sh
ssh <host> '
  install -m 0755 /srv/kibitz/bin/kibitz.prev /srv/kibitz/bin/kibitz
  systemctl restart kibitz.service
  curl -s -o /dev/null -w "%{http_code}\n" http://127.0.0.1:<port>/healthz
'
# 需要更早的版本
install -m 0755 "$(cat /root/.last-kibitz-backup)/kibitz" /srv/kibitz/bin/kibitz
```

回滚后确认首页能打开、能创建房间即可，不必等完整验收。

---

## 六、资源检查

```sh
df -h /                       # 磁盘（日志与备份是主要增长点）
free -m                       # 内存（每个会话常驻开销很小，主要看是否异常增长）
uptime                        # 负载
ss -ltnp | grep -E ':80|:<port>'   # 端口监听：relay 只应监听回环
systemctl status kibitz.service | grep -i memory   # 单进程内存峰值
```

阈值建议：磁盘 > 80% 时清理旧备份与日志（`journalctl --vacuum-time=30d`）；
进程内存持续增长且不回落 → 记录开始时间，作为排查线索。

---

## 七、已知缺陷 / 待办跟踪

| 项 | 现状 | 影响 | 计划 |
|---|---|---|---|
| 无持久化 | 进程重启丢会话 | 升级需选低峰期 | 设计上如此，不计划改 |
| 默认 HTTP | 页面与静态资源明文（对局内容仍 E2EE） | 浏览器提示"不安全" | 见第八节 |
| 金拉米无电脑对手 | 需两真人联机 | 单人玩不了 | 上游如实现则跟进 |
| 悔棋仅一手、仅真人联机 | 对电脑/同屏不提供 | 无 | 设计选择 |
| 观战者不能开局 | 服务端规则 | 无 | 不放宽 |
| 上游 release/pages 工作流 | 已加 `if: github.repository == 'richardwooding/kibitz'`，Fork 上自动跳过 | 无 | 无需处理；发版用 `gh release create` 手动建 |

新增问题按同一格式追加一行，修复后保留记录并标注版本号。

---

## 八、HTTPS 升级计划（后续）

现状：应用层已端到端加密，但传输层为明文 HTTP；浏览器会提示"不安全"，部分网络环境会干扰明文站。

建议步骤（一次做完，避免半成品）：

1. 准备证书（Let's Encrypt 或云厂商证书），确认自动续期（`certbot renew --dry-run`）；
2. 反向代理增加 `listen 443 ssl http2`，同一个 server 块同时代理 `/` 与 `/ws`；
3. 80 端口改为 301 跳转到 443（保留 ACME 校验路径）；
4. 确认 `proxy_set_header X-Forwarded-For $remote_addr`（覆盖式）保持不变——限流按真实 IP 分桶；
5. 确认 WebSocket 在 TLS 下正常：页面是 https 时客户端会自动用 `wss`，无需改代码；
6. 验证：`curl -sI https://<host>/`、浏览器开局一局、观战端同时在线；
7. 更新证书到期提醒（日历），或确认续期 timer 正常。

回滚：保留改动前的反向代理配置备份，`nginx -t && systemctl reload nginx` 前先备份，失败即还原。

---

## 九、故障速查

| 症状 | 先看这里 |
|---|---|
| 页面打不开 / 502 | `systemctl is-active kibitz`；反向代理 upstream 端口是否与 `--listen` 一致 |
| 页面能开但进不了房间 | `/ws` 的 `Upgrade`/`Connection` 头；`proxy_read_timeout` 是否过小 |
| 第 6 个人打开就 429 | 限流桶是否退化成全站共用：`X-Forwarded-For` 是否覆盖式、`--trusted-proxies` 是否只含本机代理 |
| 改前端不生效 | 是否忘了 `make wasm`（`.br/.gz` 与二进制内嵌资源都会是旧的） |
| 局面两端不一致 | 收集两端浏览器 Console 与操作时序；这类问题属协议层，先回滚再排查 |
| 重启后所有人掉线 | 预期行为：无持久化 |
