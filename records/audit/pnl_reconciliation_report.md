# Lingxi PnL 三账对齐 Reconciliation 报告

> 来源:Composer 2.5 只读审计(2026-06-18)。对应整改文档任务 8 / P0-A。

**审计对象**: `/root/lingxi`（资金费 carry 多策略：perp-perp 跨所、同所/跨所 spot-perp、同所/跨所 perp-spot）
**归因修复边界**: commit `fab6afd`（2026-06-17 14:40:22 UTC，`lingxi-V12.1`）
**只读命令**: `go run ./cmd/ledgerrecon /root/lingxi/records` + Python3 聚合

---

## 1. 执行摘要

| 问题 | 答案 |
|------|------|
| **真实赚还是亏？** | **亏**。四所合计权益从 **439.17 → 435.33 USDT**，Δ = **-3.84 USDT**（`equity_snapshots.jsonl` / `ledgerrecon`） |
| **内部账本怎么说？** | 五策略 settlements 合计 **-791.98 USDT**（几乎全来自 crossvenue perp-perp **-791.84**） |
| **差额在哪？** | 主因 **HEI-USDT 单笔 -801.56 USDT 归因幻觉**（gate 张/币平仓 qty 未归一化）；剔除后内部 **+9.57 USDT** vs 权益 **-3.84 USDT**，仍有 **~13.25 USDT 未解释差** |
| **能否 go/no-go？** | **否**。现有 settlements **100% 标为 `untrusted_legacy_sample`**（全部 close_time 早于 `fab6afd`，且 173/181 缺 `funding_truth_status`） |

**一句话**: 真实现金小幅亏损（~4U），内部 perp-perp 账本因 HEI 幻觉显示巨亏（~792U），**不得以 settlements Σnet_pnl 驱动风控/分配**；以 `equity_snapshots` 聚合层为准。

---

## 2. 真相锚(账户级权益)

`records/crossvenue/equity_snapshots.jsonl`(9576 行,2406 时间戳,每 3min 采样):
- 首/末:2026-06-13T16:37:43Z 439.1719 → 2026-06-18T15:58:58Z 435.3327，**Δ = -3.8392 USDT**

分所 Δ:binance -34.06 / bybit +41.31 / gate +8.74 / okx -19.66(跨所划转致分所不归零,只在四所合计层对平)。

---

## 3. 内部账本(策略 settlements)

| 文件 | 记录数 | Σ net_pnl | realized_funding | trade_pnl | total_fees |
|------|--------|-----------|------------------|-----------|------------|
| crossvenue (perp-perp) | 181 | **-791.84** | +1.6267 | -792.41 | 1.059 |
| cross_venue_spot_perp | 126 | -0.15 | 0 | (无字段) | ~0 |
| single_venue_spot_perp | 6 | 0 | 0 | 0 | 0 |
| single_venue_perp_spot | 文件不存在 | — | — | — | — |
| bybit/settlements.json (legacy) | 1 | -0.187 | — | — | — |

PnL 公式(`cross_venue_settlement.go`): `net_pnl = realized_funding + trade_pnl − total_fees − rollback_cost − rebalance_cost`。

辅助:`crossvenue/earnings.jsonl` 48 条 Σ=+3.0641（口径≠settlements realized_funding +1.63）；`live_daily_risk_state.json` 停在 2026-06-13、realized_pnl=0（未与 settlements 同步）。

---

## 4. 日级对账(UTC,ledgerrecon,容差 ±100bps/日)

| 日期 | equity_Δ | settlements Σ | excl_abnormal | diff | 判定 |
|------|----------|---------------|---------------|------|------|
| 06-13 | +1.30 | 0 | 0 | +1.30 | OK |
| 06-14 | +0.63 | +3.35 | +3.35 | -2.72 | OK |
| 06-15 | -0.74 | -5.11 | -5.11 | +4.37 | OK |
| **06-16** | **-2.71** | **-790.08** | **+11.48** | **-14.19** | **GAP** |
| 06-17 | -0.20 | 0 | 0 | -0.20 | OK |
| 06-18 | -2.37 | 0 | 0 | -2.37 | OK |

---

## 5. Trusted / Incomplete / Unreconciled 拆分

**Go/No-Go 层(字段完整性 + fab6afd 边界)**:
- `untrusted_legacy_sample`: **181/181 = 100%**(close < 修复 commit 或缺 funding_truth_status)→ 全部退出 go/no-go。
- post-fix 完整样本: **0 条**。

**legacy 判据明细**: close_time 全部 < 2026-06-17 14:40(181/181)；funding_truth_status 缺 173/181；close_reason 缺 137/181；settlement_id 缺 173/181；无 leg entry/exit/closed_qty 快照(181/181，不可逐仓考古)；attribution_incomplete 有值 0/181。

**技术对账层(ledgerrecon)**: trusted 21%(38) / unreconciled 78.5%(142) / abnormal 0.6%(1)。注:技术层 trusted 仍全属 legacy,不可作业务可信。

**Explained/Unexplained**: 真实权益 -3.84；HEI phantom -801.56(应隔离)；剔 HEI 后残差 ~-13.25(划转 / 6/17–18 无 settlement 的权益变动 / spot-perp / earnings 口径差 / 资金费时差)。

---

## 6. 个案

- **HEI-USDT**(confirmed 记账幻觉):06-16 09:20,gate/binance,notional 46.4,trade_pnl=net_pnl=-801.56,fees=0,close_slippage=+2.06。事件窗口 08:33–09:21 真实现金 Δ≈-0.29U。全库唯一 |trade_pnl|>50。根因:平仓侧 `accumulateCrossVenueLegClose` 未做张→币归一化,gate 1700 张当币数。
- **NBIS-USDT**(疑似,未确认):06-16 16:15,bybit/binance,notional 16,trade_pnl=+10.83,fees=0,fts 缺。|trade_pnl|/notional=67.7%;窗口现金 Δ=-0.26U 方向不一致 → 高度疑似幻觉(非 gate 腿,不太像 HEI 同根因)。
- **RIVN-USDT**(疑似,未确认):06-15 11:43,**gate**/binance,notional 44.3,trade_pnl=-6.94,fts 缺。含 gate 腿与 HEI 场景一致;窗口现金 Δ=+0.10U 方向不一致 → 疑似同类。金额远小,不做完整还原。

---

## 7. Forward Instrumentation(逐仓对账所需字段)

| 优先级 | 字段/机制 | 目的 |
|--------|-----------|------|
| P0 | 平仓 leg entry_price/exit_price/closed_qty(币本位)快照 | 复算 trade_pnl、检测 HEI 类 bug |
| P0 | funding_truth_status 强制写入(verified/partial/missing/unknown,禁省略) | 资金费 truth 闸 |
| P0 | attribution_incomplete: bool + reason | 标记 opening 快 finalize / 零成交关闭 |
| P0 | 平仓路径统一 crossVenueLegExecutedCoinQty | gate/okx 张→币 |
| P1 | entry/current_snapshot_id、decision_cycle_id 全量 | 关联 scan/opportunity |
| P1 | close_reason 必填 | 区分正常平仓 vs orphan finalize |
| P1 | 逐笔 execution journal(order_id, executed_qty_raw/coin, mult) | 订单级考古 |
| P1 | equity_snapshots 标注 transfer_delta / 外部划转事件流 | 解释聚合层 diff |
| P2 | spot-perp/perp-spot settlements 增加 trade_pnl 分解 | 五策略同口径 |

**结论**: 在 P0 字段落盘并有 post-fab6afd 样本跑通 ledgerrecon 日级 OK 之前,系统只能可信回答聚合层「四所权益净变动约 -3.84 USDT」,不能逐仓回答盈亏。
