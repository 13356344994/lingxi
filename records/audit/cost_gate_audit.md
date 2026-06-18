# Lingxi 成本闸配置审计

> 来源:Composer 2.5 只读审计(2026-06-18)。对应整改文档 P1-A / 任务 10。

**结论先行**: 当前 `config.yaml` 是**主动亏损采数据闸**(research_collecting_mode),不是盈利闸。全局经济闸 `BreakEven` 被压到 **0.011%**,相对真实四腿往返手续费 **0.24%**(BNB 抵扣后)约**高估 EV 22×**;相对出厂盈利配置 **0.55%** 约 **50×**。

代码锚点:BreakEven 公式 `economic_params.go:41-44`;净利估算 `engine_cost_guard.go:133-135`;配置自述 `config.yaml:14-19`。

---

## 1. 参数差异表(config.yaml vs internal/core/defaults.go)

### 1.1 全局 Strategy / Risk

| 参数 | 默认 | 当前 | 倍数 | 放松? |
|------|------|------|------|------|
| fee_reserve_rate | 0.0030 | **0.00001** | 降 300× | ✅ |
| exit_spread_reserve_rate | 0.0005 | **0.0** | 0× | ✅ |
| auto_derive_fee_reserve_enabled | true | **false** | — | ✅(关死真实派生) |
| impact_reserve_in_gate_enabled | true | **false** | — | ✅ |
| min_estimated_funding_rate | 0.0055 | **0.0001** | 降 55× | ✅ |
| max_scan_basis_rate | 0.0015 | **0.10** | 66.7× | ✅ |
| favorable_basis_max_rate | 0(关) | **0.10** | — | ✅ |
| min_net_profit_rate (risk) | 0.0020 | **0.0001** | 降 20× | ✅ |
| enable_rolling_hold | false | **true** | — | ✅延长暴露 |

当前有效 BreakEven = 0.00001 + 0 + 0.0001 = **0.011%**;出厂 = 0.0030+0.0005+0.0020 = **0.55%**。扫描海选被 `max(configFloor, BreakEven)` 兜底,有效地板 = 0.011%。

### 1.2 真实四腿往返成本

config BNB 抵扣口径:`2×(0.00075+0.00045) = 0.0024 (0.24%)`;实拉账户口径约 0.30%。`auto_derive=false` → 实拉费率不写回进闸的 fee_reserve(economic_params.go:79-80)。

### 1.3 Cross-Venue Executor(perp-perp 主实盘)

| 参数 | 默认 | 当前 | 放松? |
|------|------|------|------|
| live_trading_enabled | false | **true** | 已实盘 |
| open_spread_apr | 1.50 | **0.35** | ✅降4.3× |
| close_spread_apr | 0.30 | 0.20 | 略 |
| max_concurrent | 2 | **8** | 放量 |
| max_position_usdt | 50 | 60 | — |

### 1.4 Bybit Executor(四腿 carry)

| 参数 | 默认 | 当前 | 放松? |
|------|------|------|------|
| live_trading_enabled | false | **true** | 已实盘 |
| min_net_profit_rate | 0.0005 | **-0.005** | ✅ **负门=主动亏损** |
| roll_enabled | false | **true** | ✅ |
| max_hold_settlements | 1 | **12** | ✅ |
| ev_gate_enabled | false | **true** | ✅允许单次负EV |
| perp_leverage | 1 | **2** | 放大名义 |

### 1.5 OKX/Gate funding_executor(默认不自动启)

live_trading_enabled=true 但 singleVenueStrategyAutoStart=false(strategy_control.go:26);min_net/min_funding 未覆盖(0.05%),走独立 BybitExecConfig 路径,不受全局放松影响。

---

## 2. 各路径成本入闸情况

**币安单所引擎** `estimateReservedNetProfitRate`(engine_cost_guard.go:92-96,133-135): funding − entrySpread(实测✅) − fee_reserve(1e-5❌几乎0) − exit_spread(0) − borrow(✅) − residual(✅) − impact(❌关)。真实 taker 四腿费**未进闸**;basis **仅过门不进净利公式**(strategy_evaluator.go:103-104)。

**跨所 perp-perp** `evaluateCrossVenueOpenEV`(cross_venue_gates.go:248-305): roundTripCost=Σ4腿×(takerBps[5~5.5]+slip3bps);breakevenHours≤24 且 funding×24h≥cost×1.3。**taker+滑点进闸**(硬编码 bps),但前置 APR 闸 35% vs 默认 150% 放松。

**Bybit/OKX/Gate 四腿** `planBybitDeltaNeutralOpen`(bybit_exec_plan.go:206-247): grossFunding − (spot/perp taker×2 + 借币),netRate≥MinNetProfitRate。Bybit 门=-0.5% **主动放行亏损**;entry 两腿价差用 mid 价**未进闸**。

---

## 3. 量化高估

| 对比 | 数值 | 相对真实 0.24% |
|------|------|---------------|
| 出厂 BreakEven | 0.55% | 2.3× |
| 当前 BreakEven | **0.011%** | **0.05×(=真实费的 1/22)** |
| 当前 fee_reserve alone | 0.001% | 1/240 |
| max_scan_basis 放松 | 10% vs 0.15% | 67× |

单笔示例(单次 funding 0.04%,零 entrySpread,正 carry):当前闸 0.04%−0.001%=+0.039% 过门;真实 0.04%−0.24%=**-0.20%** 结构性亏损。符号反转。

---

## 4. 分路径定级

| 路径 | 运行态 | 定级 |
|------|--------|------|
| 跨所 perp-perp | 实盘(主) | 混合,偏 relaxed-data-collection(APR闸放松,EV闸本身较完整) |
| Bybit 四腿 carry | 实盘 | relaxed-data-collection / unsafe-for-profit(负净利门) |
| OKX/Gate 单所 | 配置实盘但不自启 | profit_seeking 默认参数(若启动) |
| 币安单所引擎 | 不自启,配置最松 | relaxed/unsafe(若手动启高危) |
| Unified rotation | shadow(注:运行时 rotation_control.active 可能覆盖,见风控报告) | — |

---

## 5. 整改建议(只描述)

引入显式运行模式 `research_collecting_mode` / `profit_seeking_mode`,写入每笔记录 metadata,dashboard 禁止 collecting 模式显示「盈利验证中」。

profit_seeking_mode 最低恢复项:auto_derive_fee_reserve_enabled=true;exit_spread_reserve_rate=0.0005;impact_reserve_in_gate_enabled=true;min_net_profit_rate≥0.0020;min_estimated_funding_rate≥BreakEven;max_scan_basis_rate=0.0015;Bybit min_net≥0.0005;cross_venue open_spread_apr 回 1.5。

collecting 模式若保留:所有样本打标 data_collection_only=true,禁入 alpha/net edge 统计,唯一用途=校准真实滑点/entrySpread/rollback/open_retry 损耗;仍保留 entrySpread 实测扣减、借币实拉、残腿 fillmodel(真实摩擦不应关)。

验收:BreakEven≥2×(spotTaker+futuresTaker) 实拉四腿费;collecting 模式 dashboard 显示红色「主动接受亏损采数据」;关闭 collecting 后 0.04% 单次 carry 不得过闸。
