# Backtest Directory

这个目录用于放置回测相关的样例记录、后续回测器实现和策略验证输出。

当前已经包含：

- `sample_backtest_records.csv`
  - 作为 Web 面板的回测样例数据源
  - 程序首次启动时，如果 `records/backtest_records.csv` 还没有任何记录，会自动把这个样例文件的内容复制过去
- `sample_backtest_trades.csv`
  - 作为最小回测器的交易样本输入
  - 可通过 `go run ./cmd/lingxi backtest` 聚合生成新的 `records/backtest_records.csv`
- `sample_backtest_replay.csv`
  - 作为历史回放式最小回测器的时点样本输入
  - 可通过 `go run ./cmd/lingxi backtest replay` 或 `go run ./cmd/lingxi backtest-replay` 回放生成新的 `records/backtest_records.csv`
- `sample_backtest_timeline.csv`
  - 作为按模块时序推进的事件流样本输入
  - 可通过 `go run ./cmd/lingxi backtest timeline` 或 `go run ./cmd/lingxi backtest-timeline` 生成新的 `records/backtest_records.csv`
  - 运行后若出现残腿或双腿数量不一致，还会同步生成 `records/residual_risk_records.csv`

当前回测 CSV 字段说明：

- `recorded_at`: 回测记录写入时间
- `scenario`: 回测场景名称
- `symbol`: 交易标的
- `period`: 回测区间
- `trades`: 交易次数
- `win_rate`: 胜率
- `funding_income`: 资金费收入
- `trading_pnl`: 交易盈亏
- `total_fees`: 总手续费
- `net_pnl`: 净利润
- `max_drawdown`: 最大回撤
- `notes`: 备注

后续如果你要继续推进，可以在这个目录里新增：

- 更完整的历史回放器
- K 线 / 盘口样本
- 参数扫描结果
- 多版本策略对照结果

最小回测器当前用法：

```bash
go run ./cmd/lingxi backtest
```

如果要指定自定义输入文件：

```bash
go run ./cmd/lingxi backtest backtest/your_trades.csv
```

历史回放式最小回测器当前用法：

```bash
go run ./cmd/lingxi backtest replay
```

或：

```bash
go run ./cmd/lingxi backtest-replay
```

如果要指定自定义回放样本：

```bash
go run ./cmd/lingxi backtest replay backtest/your_replay.csv
```

按模块时序推进的回放器当前用法：

```bash
go run ./cmd/lingxi backtest timeline
```

或：

```bash
go run ./cmd/lingxi backtest-timeline
```

如果要指定自定义时序样本：

```bash
go run ./cmd/lingxi backtest timeline backtest/your_timeline.csv
```

`timeline` 样本字段说明：

- `scenario`: 回测场景名
- `symbol`: 交易标的
- `period`: 回放区间
- `phase`: 阶段，只支持 `scan`、`monitor`、`open`、`close`
- `timestamp`: 当前阶段事件时间
- `funding_rate`: 当前时点使用的资金费率
- `basis_rate`: 当前基差
- `spread_rate`: 监控阶段使用的综合摩擦率
- `fee_rate`: 单腿手续费率
- `notional_usdt`: 本次名义资金
- `drawdown_rate`: 当前阶段回撤率样本
- `liquidity_ok`: 当前阶段整体流动性是否允许继续
- `scan_score_rate`: 扫描阶段排序分数，`Top N` 竞争优先使用这个值
- `futures_leg_spread_rate`: 开平仓时合约腿摩擦率
- `spot_leg_spread_rate`: 开平仓时现货腿摩擦率
- `futures_leg_ok`: 合约腿是否成交成功
- `spot_leg_ok`: 现货腿是否成交成功
- `futures_fill_ratio`: 合约腿成交比例，`0~1`
- `spot_fill_ratio`: 现货腿成交比例，`0~1`
- `notes`: 备注

`timeline` 模式当前规则：

- 扫描阶段会按 `scenario + period + timestamp` 分组做 `Top N` 竞争，只保留前 `MaxTargets`
- `monitor` 阶段会复用当前引擎的观察阈值和立即作废阈值
- 只有在开仓前恢复到 `healthy` 的目标，才允许进入 `open`
- `open` 阶段按“先合约后现货”模拟，任一腿失败都会终止该目标
- 若双腿都成功但 `fill_ratio` 不一致，会单独写入 `residual_risk_records.csv`
- `residual_risk_records.csv` 会额外给出应急对冲成本、应急手续费和估算应急损益
- `backtest_records.csv` 只保留真正完成开仓和平仓的目标；被 `Top N` 挤掉、观察未恢复、或双腿失败的目标只会体现在样本与日志逻辑里，不会出现在最终汇总中

残腿风险应急损益估算口径：

- `emergency_hedge_cost_usdt = residual_usdt * (max(futures_leg_spread_rate, spot_leg_spread_rate) + spread_rate)`
- `emergency_hedge_fee_usdt = residual_usdt * fee_rate`
- `emergency_hedge_pnl_estimate = -(emergency_hedge_cost_usdt + emergency_hedge_fee_usdt)`

残腿风险等级口径：

- `high`: `residual_usdt >= 200` 或 `emergency_hedge_pnl_estimate <= -0.20`
- `medium`: `residual_usdt >= 50` 或 `emergency_hedge_pnl_estimate <= -0.05`
- `low`: 其他情况

## 月度主回测残腿 / 部分成交 haircut（H2）

月度真实行情回测（`go run ./cmd/lingxi backtest-monthly` / `RunMonthlyMarketSimulation`）此前假设
「完美对冲、100% 成交、零残腿」，会系统性高估收益。现在主路径会用一套保守、确定性的
期望成本模型对每笔候选计提 haircut，和 `timeline` 回放里的 `fill_ratio` / `leg_reject`
残腿逻辑同源：

1. 部分成交残腿：每条腿期望只成交 `expected_leg_fill_ratio`，未匹配名义需在更差价格
   紧急补腿，成本 = 残腿名义 ×（单边摩擦率 × `emergency_hedge_spread_multiplier` + |基差|）。
2. 单腿拒单：以 `leg_reject_probability` 的概率某条腿完全未成交，已成交腿被迫紧急平掉，
   开仓、平仓两侧各计提一次整条腿名义的期望成本。

haircut 会同时作用于：

- `EstimatedNetPnL`（参与安全边际门禁与候选择优，让回测主动拒掉「只有在完美对冲假设下
  才赚钱」的边缘候选）；
- `RealizedNetPnL` 与 `backtest_records.csv` 的 `trading_pnl` / `net_pnl`（让汇总净收益诚实）。

相关配置项（`config.yaml` 的 `historical.*`，含默认值）：

- `residual_fill_haircut_enabled`（默认 `true`）：是否启用 haircut；设为 `false` 可用于
  对照「修正前 vs 修正后」净收益差异。
- `expected_leg_fill_ratio`（默认 `0.99`，取值 `(0,1]`）：单腿期望成交比例。
- `leg_reject_probability`（默认 `0.01`，取值 `[0,1]`）：单腿拒单概率。
- `emergency_hedge_spread_multiplier`（默认 `4.0`，`>=1`）：紧急补腿/平腿相对正常摩擦的恶化倍数。

每笔候选的 `notes` 会输出 `residualFillHaircut` / `residualPartialFillCost` /
`residualLegRejectCost` / `expectedLegFillRatio` / `legRejectProbability`，便于事后审计。
