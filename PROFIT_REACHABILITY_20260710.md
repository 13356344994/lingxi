# 运行可达性与盈利取舍（2026-07-10）

## 现役真钱

1. `startLiveStack` 启动四所方向引擎壳；慢线开仓调用点已被删除，慢线只保留旧仓管理能力。
2. OKX fast 引擎是当前唯一自动真钱入口。
3. fast 中只有 `fast_probe_source`/持证名单允许的来源能过 `fastCertGate`；当前名单为
   `key_sqz_fade,key_casc_fade`，其中 sqz 已被治理器停机，实际只剩 casc。
4. `cmd/capcarry` 是事件出现后人工批准、命令触发的独立真钱工具，不是后台自动策略。

## 现役但只记录

- 全市场 mid、OKX/Bybit 强平流、热币 books/trades、全市场 tape、跨所 mid；
- scorer/fade/liq_follow/xgap/firework/tape 等影子候选；
- 粒子账本、阅卷人、auto_miner、天气格。

这些模块当前不直接亏钱；保留标准是“是否给真钱策略提供不可替代信息”。后续按磁盘、CPU 和实际提名率逐项淘汰。

## 已短路或不可达

- `cross_venue_scanner`、`CrossVenueExecutor`、`UnifiedRotationRunner`、旧 FundingRateSniper：
  当前 [`internal/app/run.go`](/root/lingxi/internal/app/run.go) 没有构造或启动调用链。
- `config.yaml` 中 `cross_venue_executor`、`unified_rotation` 和大部分 carry 经济参数是兼容遗留，
  不是当前生产行为。
- 慢线 `openFromShortlist` 函数仍在源码中，但调用点为零，并有 `slow_open_removed_test.go` 防复活。

## 删除与保留裁决

### 立即保留

- 交易所适配器、订单确认、持仓恢复、账户风控、结算与资金流；
- **方向量化主链**：天气识别（司天监）→ 状态路由（分拣器）→ 对应方向打法 → 仓位/退出 → 真钱反馈；
- fast 当前探针入口及方向分拣所需的最小采集流；
- #3a 正钉状态机、下架/限价带只作低相关辅助，不占方向主线预算。

### 保持关闭

- sqz、xgap、firework、scorer、fade、liq_follow、tape 实弹；
- 负钉 cash-and-carry（借还币能力未实现）；
- carry/rotation 后台链。

### 暂不物理删除

carry/rotation 配置结构和慢线管理代码暂留一轮发布：它们不在运行路径，但一次性大删对赚钱没有直接增益，
却可能误伤面板、旧账本解析或在场仓恢复。先通过当前安全版本部署与实盘核对，再单独做纯删除提交。

## 策略层重写触发器

出现任一条件即停止在现框架继续调参：

1. casc 到 n=150 后等权 bps PF <1.2 或净中位不为正；
2. #3a 首次完整双腿实验扣全部成本后为负，且亏损来自规则经济性而非可修执行错误；
3. 天气格在有效标签样本中仍不能让 train/holdout 同号；
4. 新候选依赖价格自身历史、in-sample 切点或未成交假设才能为正。

触发后保留数据/执行/风控与司天监框架，重写具体方向打法和分拣映射；方向量化仍是主业务。
研究预算优先给能提升“天气→打法→真钱”闭环的非价格流、私有测量和执行结构；规则洞只并行低成本运行。
