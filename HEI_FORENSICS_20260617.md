# HEI forensic 报告（2026-06-17）

## 1. 核心结论

| 选项 | 判定 | 置信度 |
|------|------|--------|
| **A. 纯记账 bug** | **采纳** | 高 |
| B. 执行脱腿 bug | 驳回 | — |

**HEI -801 USDT 是 settlements 归因幻觉。** 名义 ~46U 的 gate/binance 对冲仓不可能在真实现金上产生 801U 亏损；四所 `cash_usdt` 快照在 08:34–09:20 几乎平坦。根因是 **gate 张本位平仓成交量未与开仓侧一样归一化为币数量**，`computeCrossVenueTradePnL` 用错误的 `ClosedQty`×价差 算出 -801。

**对实盘含义：** 本次 HEI 事件本身**不能**证明存在持续裸腿；但 **settlements 不可信**，在修复归因前不应把 `net_pnl` 用于风控/分配。是否开实盘还需单独评估 60 秒 opening 抖动与 gate 归因修复是否部署。

---

## 2. HEI settlements 时间线

| # | open (UTC) | close (UTC) | 持有 | notional | trade_pnl | net_pnl | 备注 |
|---|------------|-------------|------|----------|-----------|---------|------|
| 1 | 06-15 16:28 | 06-16 00:46 | ~8.3h | 24.5 | +0.03 | +0.09 | binance/bybit，正常归因 |
| 2–6 | 06-16 08:34–08:38 | 06-16 08:35–08:39 | ~45–51s | 30–51 | **0** | **0** | gate/binance 抖动 |
| **7** | **06-16 08:39:32** | **06-16 09:20:23** | **~41min** | **46.37** | **-801.56** | **-801.56** | **异常** |

异常笔特征：`open_fees=close_fees=0`，`close_slippage=+2.06`，无 `close_reason`，无 `funding_truth_status` 字段。

---

## 3. -801 如何被算出

### 3.1 公式（代码）

```
func computeCrossVenueTradePnL(pos CrossVenuePosition) float64 {
	var total float64
	if pos.HighLeg.ClosedQty > executionQtyTolerance && pos.HighLeg.ExitPrice > 0 {
		total += (pos.HighLeg.EntryPrice - pos.HighLeg.ExitPrice) * pos.HighLeg.ClosedQty
	}
	if pos.LowLeg.ClosedQty > executionQtyTolerance && pos.LowLeg.ExitPrice > 0 {
		total += (pos.LowLeg.ExitPrice - pos.LowLeg.EntryPrice) * pos.LowLeg.ClosedQty
	}
	return total
}
```
(internal/app/cross_venue_settlement.go:99-107)

```
// net_pnl = realized_funding + trade_pnl − total_fees − rollback_cost − rebalance_cost
netPnL := money.SumUSDT(realizedFunding, tradePnL, -totalFees, -attr.RollbackCostUSDT, -attr.RebalanceCostUSDT)
```
(internal/app/cross_venue_settlement.go:110-123)

HEI 异常笔：`realized_funding=0`，`total_fees=0` → **`net_pnl = trade_pnl = -801.56`**。

### 3.2 平仓归因入口（未做张→币）

```
func accumulateCrossVenueLegClose(leg *CrossVenueLegState, executedQty, avgPrice float64) {
	// ...
	leg.ClosedQty += executedQty
	leg.ExitPrice = (leg.ExitPrice*prevQty + avgPrice*executedQty) / leg.ClosedQty
}
```
(internal/app/cross_venue_attribution.go:54-66)

```
	case cvExecCloseHigh, cvExecCloseLow, cvExecOrphanCloseHigh, cvExecOrphanCloseLow:
		// ...
		case cvExecCloseHigh, cvExecOrphanCloseHigh:
			accumulateCrossVenueLegClose(&pos.HighLeg, res.ExecutedQty, res.AvgPrice)
		case cvExecCloseLow, cvExecOrphanCloseLow:
			accumulateCrossVenueLegClose(&pos.LowLeg, res.ExecutedQty, res.AvgPrice)
```
(internal/app/cross_venue_attribution.go:89-96)

**开仓**侧有 `crossVenueLegExecutedCoinQty`（cross_venue_qty.go:67-97），**平仓没有**。

### 3.3 数量级反推

- notional ≈ 46U，HEI 价 ~0.44–0.50 → 账本 qty ≈ **100 币**。
- 若 `(entry − exit) × ClosedQty ≈ −801`：
  - 单腿、价差 ≈ 全价（~0.46）且 **ClosedQty ≈ 1700**（≈17×）→ 典型 **张数当币数 / 平仓量重复累计**；
  - 或 **ExitPrice ≈ 8.4、EntryPrice ≈ 0.46、qty ≈ 100** → 像把限价/错误字段当成交价。
- `close_slippage=+2.06` 表明至少有一腿平仓执行**优于** mid，与「真亏 801」不一致。

### 3.4 与 HANDOFF 一致

`HANDOFF.md` §3：gate/okx `ExecutedQty` 张数/币数混淆导致 **positions 账本 qty 错，但真实交易对**。HEI 是 gate(high)/binance(low) 组合，与已知风险场景一致。全库 **仅 HEI 一笔** 出现 |trade_pnl|>50，符合单次归因爆炸而非系统性真亏。

---

## 4. 60 秒抖动循环

### 4.1 现象

06-16 08:34–08:39 共 5 笔 gate/binance HEI：持有 ~45–51s，close 秒位 **:23**（与 LRC/SAMSUNG 等同日 churn 一致），`trade_pnl=net_pnl=0`，费用/滑点全 0。

### 4.2 触发路径（代码）

**路径 1 — opening continuation，交易所已空：**

```
case !highOpen && !lowOpen:
	pos.Status = CrossVenueStatusClosed
	// ...
	e.finalizeClosedCrossVenuePosition(pos)
	return pos, true
```
(internal/app/cross_venue_manage.go:179-190)

**路径 2 — 在场仓管理，两所均无持仓：**

```
if !highOpen && !lowOpen {
	pos.Status = CrossVenueStatusClosed
	e.finalizeClosedCrossVenuePosition(pos)
	return pos, true
}
```
(internal/app/cross_venue_manage.go:72-76)

两路径 **不调用** `closeCrossVenuePosition` → **不** `recordCrossVenueExecution` → `ClosedQty=0` → `trade_pnl=0`。

推断：HEI gate/binance **反复 opening / 快速清空**（第二腿失败、换仓 retry、或所内已平但台账仍在 opening），每 ~60s 管理周期 finalize 一次。**不是** recovery flatten 的 CRITICAL 路径（那会留下 rollback/slippage）。

### 4.3 transitions.jsonl

**无 HEI 记录** — 该文件仅从 **2026-06-16T18:36**（BEAT/LRC 全局平仓）起有数据，无法还原 08:34 HEI 状态机；结论来自 settlements 时间形态 + 上述代码路径。

---

## 5. 账本 vs 真实现金

### 5.1 按日对照（UTC close_time / 日末权益）

| 日期 | settlements Σ net_pnl | 权益日 Δ | 偏差 |
|------|----------------------|----------|------|
| 2026-06-14 | +3.35 | +0.99 | +2.36 |
| 2026-06-15 | -5.11 | -0.97 | -4.15 |
| **2026-06-16** | **-790.08** | **-2.63** | **-787.45** |
| 2026-06-17 | 0 | +0.04 | -0.04 |
| **合计** | **-791.84** | **-2.55**（6/13→6/17） | **≈ -789** |

- 全库 Σ net_pnl = **-791.84**
- 剔除 HEI -801.56 后 = **+9.72**
- **6/16 偏差 -787 几乎全由 HEI 一笔贡献**

### 5.2 HEI 事件窗口权益（每 3min 快照）

| 时刻 (UTC) | 四所合计 |
|------------|----------|
| 08:33:22 | ~440.25 |
| 08:39:22 | ~440.25 |
| 09:21:22 | ~439.82 |
| **Δ** | **≈ -0.43 U** |

### 5.3 6/15→6/16 分所 Δ（含多 symbol，非 HEI 单独）

| 所 | Δ (U) |
|----|-------|
| binance | -6.49 |
| bybit | +32.01 |
| okx | -19.72 |
| gate | -8.42 |
| **合计** | **-2.63** |

与 HEI -801 **无法同向解释**；更像多笔正常 churn + 资金再平衡。

---

## 6. funding_truth_status="?" 成因

| 统计 | 数量 |
|------|------|
| JSON **缺字段**（读作 `?`） | **173** |
| `partial` | 5 |
| `missing` | 3 |
| Σ realized_funding | 1.63 U |

**初判：主要是 settlements 序列化未写入该字段**，不是 173 次拉取失败。

- 写入链：`buildCrossVenueSettlement` ← `pos.FundingTruthStatus`（cross_venue_settlement.go:146）
- 赋值：`syncRealizedFunding`（cross_venue_manage.go:448-477）仅在 manage 周期 polling 时更新；大量 **快速 finalize（<1min）** 的 churn 记录从未 sync → 字段缺失
- 仅 8 条有显式 status（如 LRC `partial`、C/BR/W `missing`），说明 polling **偶发可用**，非完全未实现

---

## 7. A vs B 证据链摘要

| 维度 | A 记账 | B 脱腿 |
|------|--------|--------|
| 四所现金 | 08:34–09:20 Δ≈0.4U | 应 ≈-801U |
| 全库异常集中度 | 仅 HEI | 应分散 |
| close_slippage | +2.06（优） | 应大负 |
| 代码 | 开/平仓 qty 口径不对称 | — |
| 抖动 5 笔 | trade_pnl=0，所内已空 finalize | 应有裸腿 PnL |
| transitions | 无覆盖 | 无法证实 B |

**未100%还原** 异常笔内存 `EntryPrice/ClosedQty/ExitPrice`（positions 已删、无 HEI 订单日志），但 **现金 + 代码 + 全库唯一异常** 足以支持 A。

---

## 8. 最小修复建议（不实现）

1. **平仓归因对称化**：`recordCrossVenueExecution` 的 close/rollback/rebalance 相位对 gate/okx 调用 `crossVenueLegExecutedCoinQty`（与 cross_venue_open.go:325-328 对齐）。
2. **适配器层**：gate/okx `mapFuturesOrder` lookup 失败禁止 mult=1（HANDOFF §3、§4 TODO #2）。
3. **settlements 增强**：写入 `high/low entry/exit/closed_qty` 快照，便于审计。
4. **opening 快 finalize 打标**：`attribution_incomplete=true`，避免 0 与 -801 混在 Σ net_pnl。
5. **funding_truth_status**：finalize 前至少一次 `syncRealizedFunding`；缺失写 `"unknown"` 而非省略。
6. **实盘门禁**：修复 + 部署前，`Σ settlements.net_pnl` 不得驱动本金分配；以 `equity_snapshots` 为准。
7. **补日志**：06-16 08:30–09:30 跨所 journal（当前 `lingxi_server.log` 无 HEI）以便下次复核。

---

## 9. 数据限制

- `transitions.jsonl` 不覆盖 HEI 时段
- `positions.json.bak-20260616T183455Z` 无 HEI
- 无 HEI 订单/成交原始日志
- 权益快照 3 分钟粒度，非 tick 级

---

**一句话给决策层：HEI = A；真实现金几乎没变，-801 是 gate 平仓 qty/price 归因 bug 的幻觉；修平仓 coin 归一化并停用 settlements 驱动分配前，勿信账本 PnL。**
