# Lingxi 交接文档(给接手 agent)

> ⚠️ **本文档已过时（2026-06-13）**。当前权威交接请读 **`HANDOFF_20260617.md`**。下文仅作旧跨所 perp-perp 时代的历史参考。

> 最后更新:2026-06-13 ~18:05 (UTC+8) / 10:05 UTC
> 项目:`/root/lingxi`,Go 单进程 + systemd(`lingxi` 服务)+ 外部看门狗(`lingxi-watchdog.timer`)
> 四交易所资金费率套利:binance / bybit / okx / gate,每所约 100U,总 ~400U

---

## 0. 一眼看懂当前状态

| 项 | 状态 |
|---|---|
| 运行二进制 | 构建于 06-13 09:26 UTC(含 qty 修复 + 币安跨所集成 + healthz idle)。**早于 HEAD,未含账本修复** |
| HEAD 提交 | `a0f7821 lingxi-v10.1`(**已含账本记账修复**:helper + 调用点,已验证可编译)|
| **未提交** | `cross_venue_qty.go` + `cross_venue_qty_test.go` 的增量微调(+23/-11),非新功能 |
| **未部署** | 账本修复(§3):HEAD + 未提交微调都还没编进运行二进制,重新部署即可 |
| 急停 `.halt-live` | 无(系统正常运行)|
| healthz | ready(任一所在跑即 200;全 idle 也 200,见 §6)|
| 跨所在场持仓 | 2 笔:HOME-USDT、H-USDT,**真实两腿 delta 中性**,在吃资费 |
| 引擎 | 跨所执行器 + okx/gate/bybit 单所 funding 实盘;**币安遗留单所引擎已退役**(改走跨所)|

**当前没有裸腿、没有未兜住的风险。**

---

## 1. 系统架构速览

- **跨所对冲执行器**(`internal/app/cross_venue_*.go`):perp-perp,**做空高费所 + 做多低费所(最负)**,赚费率价差,delta 中性。可交易集合 `crossVenueTradeableVenues = {binance, bybit, okx, gate}`(`cross_venue_gates.go`)。
- **单所 funding 执行器**(`internal/app/venue_funding_*.go`):okx/gate 用;同所现货+永续吃正费率,负费率本所借币对冲。
- **Bybit 精简执行器**(`bybit_exec_*.go`):bybit 专用,正/负 carry。
- **币安**:遗留 `FundingRateSniper`(`funding_sniper_engine.go`)**已默认不自启**(`strategy_control.go: legacyBinanceStrategyAutoStart=false`),币安只作为跨所一条腿。恢复:把该常量改回 true。
- **扫描器**(`cross_venue_scanner.go`):只读,4 所拉费率算价差 + 借币可得性探测(只读遥测,`borrow_probes.jsonl`)。
- **统一适配器**:`internal/venue/{binance,bybit,okx,gate}/`,中性接口 `internal/venue/venue.go`。
  - bybit/binance 永续**币本位**(`ContractBasedPerp=false`);okx/gate **张本位**(下单需币↔张换算)。

---

## 2. 今天(06-13)做完的事(均已提交进 `a0f7821`,已部署)

1. **平仓死循环修复**:跨所平仓重试用轮次后缀 client ID(防 bybit `110072 duplicate`)+ 平仓后用 `FetchPositions` 真实复核已平腿。回归测试钉死(`cross_venue_executor_test.go`)。起因是昨晚旧二进制平仓死循环 15 分钟造成短时裸腿,已结束。
2. **qty 两腿不配平修复**:gate `qtyToSignedContractSize` 加 `contractStep` 对齐;`normalizeVenueQty` 修正。**实盘已确认两腿真实币数相等**。
3. **币安接入跨所**:加入可交易集合 + 适配器接线 + 退役遗留引擎 + 双向冲突守卫(`venue_funding_conflicts.go`)。
4. **运行日志健壮性**:基文件缺失/归档后自愈,不再因 `runtime_events.jsonl` 缺失而"启动隔离"禁掉整所(`runtime_log_store.go`)。
5. **healthz idle 放行**:全所"干净 idle"(从未启动、无熔断/急停/故障)判 ready,避免 `autostart=false` 手动模式下看门狗空重启(`observability_http.go: isCleanIdle`)。
6. flaky 测试修复。
7. **微额实弹自检工具** `cmd/livecheck`(okx/gate 真实开平往返,验证账户模式/权限/精度/IOC)。

---

## 3. ⚠️ 账本记账修复:已提交进 HEAD,但运行二进制还没部署它

**状态**:
- helper(`crossVenueLegExecutedCoinQty` / `reconcileCrossVenueOpenBookQty` / `crossVenueBookNotionalUSDT`)+ 调用点(`cross_venue_open.go` 第 211–220 行)**已提交进 HEAD `a0f7821`,HEAD 可编译**。
- `cross_venue_qty.go` + `cross_venue_qty_test.go` 还有**未提交的增量微调**(+23/-11),不是新功能。
- **运行二进制(09:26)早于此修复 → 线上还没生效。重新 `go build` + 重启即部署(§5)。**

**修的是什么**:`positions.json` 里 `high_leg.qty/low_leg.qty/notional_usdt` 记错(与真实持仓对不上)。**真实交易是对的,只是账本数字错**,不影响安全(平仓/风控读真实持仓)。

**根因(重要,残留隐患)**:gate/okx 的 `QueryOrder`→`mapFuturesOrder` 在 `lookupInstrument` 失败时 `mult` 默认 1,把**张数**当**币数**写进 `ExecutedQty`。Composer 在跨所层做了规避,**但适配器本身的 `mult=1` fallback 仍在**(见 `internal/venue/gate/trading.go` 和 okx 同样位置)→ 单所 okx/gate 在合约信息缓存失效时也可能误报。**建议下一轮直接修适配器层:lookup 失败应报错或重试,绝不静默 mult=1。**

**部署方法**:见 §5。建议**等当前 HOME/H 平掉、空仓时**再部署(避免在持仓上重启;此修复是记账层,不急)。历史 `positions.json` 不会自动修正,只影响新开仓。

---

## 4. 下一步 TODO(按优先级)

1. **部署账本记账修复**(§3),空仓窗口做。
2. **修适配器层 `mult=1` fallback 根因**(§3),让 gate/okx `mapFuturesOrder` 在 lookupInstrument 失败时不静默误报张数。
3. **观察四所跨所实盘**:盯第一批自然开/平,确认两腿配平、平仓干净、资金费正确入账。
4. **币安合约钱包余额**:确认币安 100U 在 **U 本位合约账户**(币安 `SupportsCapitalRebalance=false`,面板调仓不会自动划);否则币安腿会被余额闸拒。
5. **(用户侧)轮换密钥**:`secret.yaml` 早先曾被推上 GitHub;`records_backup_*.tar.gz` 也含面板控制 token 被提交进库(见 §7 安全)。建议轮换 + 清 git 历史。
6. ROADMAP 里:跨所借币负 carry 执行链(只有探测、未建);WebSocket 行情;新币上线事件流。
7. 跨所借币负 carry 若要建,**铁律**:做多腿永远选最负的所;对冲腿绝不放最负的所;净 carry =（多腿费率收益 − 对冲腿成本/借币利息 − 手续费）> 0 才开。

---

## 5. 关键操作手册(命令)

```bash
cd /root/lingxi

# —— 构建 + 测试 ——
go build ./... && go test ./... -count=1

# —— 部署(主程序入口 cmd/lingxi;systemd ExecStart=/root/lingxi/lingxi)——
go build -o lingxi.new ./cmd/lingxi
mv -f lingxi.new lingxi && restorecon -v lingxi   # SELinux 必须 restorecon → bin_t
systemctl restart lingxi && sleep 6
systemctl is-active lingxi
curl -s localhost:8080/healthz

# —— 重启后引擎是 idle(autostart=false 手动模式),用策略总控一键启动 ——
TOKEN=$(cat records/.dashboard_control_token)
curl -s -X POST -H "X-Dashboard-Token: $TOKEN" -H "Content-Type: application/json" \
  -d '{"secret_path":"secret.yaml"}' localhost:8080/api/strategy/start
# 注:binance_start 会显示 skipped(遗留引擎已退役,正常)

# —— 急停 / 解除(停所有新开仓;在场持仓继续管理;看门狗豁免不重启)——
touch records/.halt-live      # 急停
rm -f records/.halt-live      # 解除

# —— 看日志 ——
journalctl -u lingxi --since "5 minutes ago" --no-pager | grep 跨所执行

# —— 跨所账本(注意 leg.qty 当前可能不准,见 §3;以交易所真实持仓为准)——
cat records/crossvenue/positions.json
```

**查真实持仓 / 应急平仓**:写临时 `cmd/` 小工具,用 `app.LoadVenueSecretConfig` + `{bybit,gate,okx}.New(venue.Config{...})` 构造适配器调 `FetchPositions` / `PlaceOrder`(**binance 适配器构造不同:`New(*exchange.BinanceClient)`**)。应急平仓务必 **reduce-only IOC**,价格用盘口买一/卖一(okx 有价格保护带,偏移别超 ~0.5%,否则 51006/51137 被拒)。参考 `cmd/livecheck/main.go`。

---

## 6. 部署/重启关键行为(别踩)

- **autostart=false 手动模式**:重启后所有引擎 idle,必须用 `/api/strategy/start` 拉起。跨所执行器(`enabled=true`)会随进程自启并 reconcile 在场持仓;单所/bybit 需总控启动。
- **healthz 全 idle 也返回 200**(`isCleanIdle`):这是有意的,防看门狗空重启。真死进程由看门狗"心跳超时"兜底。
- **看门狗**:`/healthz != 200` 或心跳超时 → 重启;有 `.halt-live` 则豁免不重启。
- **重启会 reconcile**:跨所执行器从 `positions.json` + 交易所真实持仓对账接管,delta 中性仓重启安全。

---

## 7. 安全铁律(务必遵守)

1. **持仓账本绝不能删**:`records/crossvenue/positions.json`、`records/bybit/bybit_positions.json` 是引擎记住在场腿的唯一依据,删 = 实盘仓变裸仓。日志/观测流(`*_events.jsonl`、`scan_observations`、`monitor_records`)可清。
2. **`secret.yaml` 永不入库**(已 gitignore)。`records/` 里有面板控制 token / 审计 → 备份包别提交。`records_backup_20260612_162146.tar.gz` 已误入库且含 token,建议 `git rm --cached` + 轮换。
3. **跨所转账(提币)永不自动化**,只能人工。
4. **应急平仓只用 reduce-only**,绝不裸下市价反向单。
5. **改 config 放量前确认各所合约钱包余额**;多引擎共用同所余额,靠余额闸防超订(余额不足会拒开,不会硬开半腿)。

---

## 8. 配置要点(`config.yaml`)

- `cross_venue_executor`: `enabled=true, live_trading_enabled=true, max_concurrent=2, max_position_usdt=60`
- `venues.{okx,gate}.funding_executor`: `live_trading_enabled=true, max_position_usdt=60`
- `venues.bybit`: `autostart=false, allocated_capital_usdt=...`(bybit_executor 实盘)
- 资金已放量到 ~340U/400U(用户 2026-06-13 拍板)。

---

## 9. 重要文件索引

| 路径 | 内容 |
|---|---|
| `internal/app/cross_venue_{executor,gates,open,manage,qty,scanner,positions_store}.go` | 跨所引擎 |
| `internal/app/venue_funding_*.go` | okx/gate 单所引擎 |
| `internal/app/bybit_exec_*.go` | bybit 引擎 |
| `internal/app/funding_sniper_engine.go` | 币安遗留引擎(已退役自启)|
| `internal/app/strategy_control.go` | 策略总控 API(一键启停)|
| `internal/app/venue_funding_conflicts.go` | 单所↔跨所冲突守卫 |
| `internal/app/observability_http.go` | healthz/metrics(`isCleanIdle`)|
| `internal/app/runtime_log_store.go` | 运行日志(自愈)|
| `internal/venue/{gate,okx}/trading.go` | 币↔张换算(`mult=1` 残留隐患在此,§3)|
| `cmd/livecheck/main.go` | 微额实弹自检工具 |
| `SYSTEM_OVERVIEW.md` | 系统总览(更早,部分已被本文档更新)|

---

## 10. 给接手 agent 的一句话

系统现在四所跨所实盘正常跑、零裸腿。**最近一项收尾**是把"账本记账修复"(§3,2 个未提交文件)在空仓窗口部署掉,并顺手修适配器层 `mult=1` 根因(§3)。其余按 §4 TODO。动手前先读 §7 安全铁律,部署照 §5,别在持仓上随意重启。
