# Lingxi 项目问题分析与下一步路线

日期: 2026-06-21

本文不是结果清单,而是把项目当前暴露的问题拆成:事实、证据、成因、影响、下一步、进入下一步的条件。判断默认处在"个人量化、小资金、自有资金"语境下,不套机构级大资金标准,也不因为项目复杂就抬高评价。

重要口径:

- 当前 `account_risk_watch.json` 显示真实权益从峰值约 `454.90U` 到当前约 `385.83U`,差额约 `69.07U`。
- 后续快照序列复核显示:约 `454.90U` 的峰值很可能不是有效高水位,而是 bybit 单 venue 读数毛刺抬高后的短暂假峰;真实权益基线更接近 `436U`。
- 用户补充约 `50U` 主动提现。该说法已被 `equity_snapshots` 中 6/21 约 `07:26` 的单步 `-49.5U`、不回弹台阶高度支持,但仍需要交易所提现流水做最终闭环。
- 因此本文不再把 `69U` 视为交易亏损证据。更准确的真相是:没有证据表明系统发生了实质交易亏损;大部分差额可由假峰值与提现解释。但这不是好消息到足以抬分,因为它证明了更底层的问题:权益计量层本身不可信。
- 当前最严重的问题从"项目在亏钱"修正为"项目不知道自己的权益读数什么时候可信"。这会污染 hard halt、auto-flatten、水位、页面和所有后续 PnL 归因。

---

## 1. 总判

这个项目不是玩具。它有真实的工程骨架:扫描器、统一轮动 runner、五策略机会结构、资金路由、执行器、状态台账、恢复机制、账户风控、dashboard、控制面和大量测试。

但它也还不是一个已经成立的个人量化系统。当前最核心的问题不是"代码不够多"或"策略不够复杂",而是:

1. 权益快照数据质量不可信:会被单样本毛刺设峰,也会把残缺 venue 读数当总额。
2. 五策略只是部分统一,不是完整收敛。
3. 研究假设还没有被真实成交与结算闭环证明。
4. 影子模式不能覆盖真正会亏钱的失败路径。
5. 页面和 API 展示了很多数据,但没有把用户最需要的三个问题说清楚:
   - 钱去哪了?
   - 为什么没开仓?
   - 现在下一步该做什么?

项目目前最准确定位:

> 工程作品 / 研究平台之间,带有交易操作系统雏形;尚未达到"已初步成立的个人量化项目"。

好消息是:现有证据更支持"没有实质交易亏损,大部分时间空仓",而不是"系统真实亏掉 69U"。坏消息是:系统用来判断风险和高水位的权益计量层被证明会产出垃圾输入。一个交易系统没有亏钱,但会把毛刺当峰值、把残缺读数当总额、把提现当回撤,仍然不能叫成立。

---

## 2. 当前已经确认的关键事实

### 2.1 账户权益显示的 `69U` 回撤不是实质交易亏损证据

证据:

- `records/account_risk_watch.json`
  - `real_equity_peak_usdt`: 约 `454.90`,但该峰值应视为可疑高水位。
  - `real_equity_total_usdt`: 约 `385.83`
  - `real_equity_drawdown_usdt`: 约 `69.07`
  - `cumulative_realized_pnl_usdt`: 约 `-0.23`
  - `open_position_count`: `0`
- 快照序列复核:
  - `454.90U` 峰值来自短暂读数毛刺,真实基线更接近 `436U`;
  - 6/21 约 `07:26` 出现约 `-49.5U` 单步台阶,不回弹,高度符合主动提现;
  - 扣除假峰值约 `19U` 和提现约 `49.5U` 后,`69U` 回撤几乎被解释完。

解释:

- 之前把 `454.90U` 当真实峰值引用是错误的;它更像毛刺污染后的高水位。
- 之前把 `50U` 提现仅称为"待验证假设"也偏保守;权益序列已经强烈支持它是外部资金流。但系统仍需要交易所流水做最终闭环。
- 真实交易亏损目前看接近 `0`,与 `cumulative_realized_pnl_usdt ≈ -0.23` 一致。
- 项目的硬伤不是"已经证明在亏钱",而是"权益计量层会制造假回撤,并把假回撤喂给风控"。

问题本质:

> 在做 PnL 桥接之前,必须先证明权益快照本身可信。否则桥接表只是在拆解垃圾输入。

下一步:

1. 先修 `equity_snapshots` 数据质量闸:
   - venue 数量不完整时,该 timestamp 不得进入总额和水位计算;
   - 单样本异常峰值不得刷新 peak;
   - peak 必须由连续 N 个完整样本确认;
   - 当前总额为 0 或只剩单 venue 时,必须判为 invalid sample;
   - 每个样本必须带 completeness / quality / rejected_reason。
2. 在快照可信之后,再建账户权益桥接表,逐日/逐小时解释:
   - 起始权益;
   - 充值;
   - 提现;
   - 已实现 PnL;
   - 未实现 PnL;
   - 手续费;
   - 资金费;
   - 借币费;
   - dust / 残留资产重估;
   - 结束权益;
   - 未解释差额。
3. 把 `account_risk_watch` 的 hard halt 与 auto flatten 判断改成只消费 quality-passed equity。
4. 调出交易所提现/转账流水,逐笔验证 `50U` 台阶。
5. 解释为什么 `detectAccountFundFlowStep` 没有对该台阶打印"检测到资金转入/转出"日志。
6. 对未解释差额设硬阈值:例如 `abs(unexplained_delta) > 2U` 时进入审计阻断,禁止新开仓。

进入下一步的条件:

- 最近 7 天的 equity snapshots 先通过质量闸,无残缺零读、无单样本设峰。
- 最近 7 天每一天的权益变化都能被解释到 `±1U` 或 `±0.3%` 内。
- dashboard 首页能一眼看到"提现 XU:已由交易所流水验证/未验证;交易口径实际变化 YU;未解释差额 ZU;权益快照质量 OK/FAIL"。
- 未解释差额不再触发误判式 hard halt。

### 2.2 资金流检测没识别 + 权益快照质量缺闸

证据:

- `account_risk_watch.go` 中存在 `detectAccountFundFlowStep` 调用。
- 触发时应打印: `检测到资金转入/转出 ... 已平移日基准(不计亏损)`。
- 当前可搜索记录中未找到这条运行日志命中。
- `equity_snapshots.go` 的采样逻辑在某个 venue 拉取失败时会跳过该 venue,但只要本轮还有其他 venue 记录,仍会写入本轮快照。
- `computeRealEquityWaterMark` 对同一 timestamp 的记录直接求和,并用任意单个 timestamp 的 total 刷新 peak;没有检查本轮 venue 是否完整,也没有异常峰值确认。

判断:

这不是"系统还没有资金流识别",而是两个问题叠加:

1. 资金流识别机制存在,但在约 `50U` 量级提现上没有触发。
2. 权益快照底层没有质量闸,会把毛刺峰值和残缺读数喂给高水位/回撤计算。

第二点是更底层的病根。资金流检测再好,也不应该建立在会产生假峰值和残缺零读的原始权益序列上。

下一步:

1. 给 `equity_snapshots` 加 sample quality:
   - expected venue count;
   - actual venue count;
   - missing venues;
   - per-venue stale/error reason;
   - quality status。
2. `computeRealEquityWaterMark` 只消费 quality-passed 完整样本。
3. peak 更新必须通过连续 N 个完整样本确认,不能单样本刷新。
4. 用交易所提现/转账流水确认该 `50U` 的时间、venue、wallet、资产和手续费。
5. 将提现流水与 `equity_snapshots.jsonl` 的台阶对齐。
6. 给 `detectAccountFundFlowStep` 增加审计输出:即便未判定为资金流,也要记录候选台阶、未触发原因和阈值。
7. 在风控与页面中分开显示:
   - raw equity drawdown;
   - quality-adjusted equity drawdown;
   - fund-flow-adjusted drawdown;
   - unexplained drawdown。

进入下一步的条件:

- 单样本 bybit 毛刺不能再刷新全账户 peak。
- 只剩单 venue 或总额接近 0 的残缺读不能进入风控。
- 这笔提现能被系统从 raw drawdown 中剥离,且剥离过程可审计。
- 如果不能剥离,必须把本次事件归类为 P0 权益计量/资金流检测失败。

### 2.3 结算事实源贫弱,不是简单"分散"

已观察到的事实:

- 顶层 `records/settlements.json` 为 `[]`。
- legacy `bybit/settlements_journal.jsonl` 只有极少记录,其中 HOMEUSDT 样本为负 funding / 负净 PnL。
- 部分新路径存在 `crossvenue/settlements.jsonl`、`cross_venue_spot_perp/settlements.jsonl` 等记录,但它们没有形成一个账户级、可信、完整的 PnL 事实源。

判断:

不能把问题轻描淡写成"记录分散导致 dashboard 读不全"。更准确是:

> 交易事实源本身还没有被提升为系统级账本。它既不完整,也没有和真实权益桥接,更没有承担策略证明的责任。

下一步:

1. 定义唯一账户级 settlement fact source。
2. 所有策略 settlement 必须进入同一事实源或同一聚合协议。
3. 每条 settlement 必须带 `source_strategy`、`position_key`、`cash_delta`、`funding_delta`、`fee_delta`、`borrow_delta`、`residual_delta`、`trust_status`。
4. dashboard 的收益、风控、绩效不能各读各的账。

进入下一步的条件:

- 任意时间窗口内,settlement fact source 汇总值能与账户权益桥接表对上。
- 如果 settlement 缺失或不可信,页面必须显示"收益未知/不可用",不能显示一个看似精确但误导人的 PnL。

---

## 3. 架构问题

### 3.1 统一是真存在,但只统一到组合决策层

项目实际运行链路:

1. `startLiveStack` 组装 scanner、cross-venue executor、五策略 executor、unified runner、account risk watcher、dashboard。
2. `UnifiedRotationRunner.Run` 启动后先 reconcile,再每 60s `runCycle`。
3. 每个 cycle 先 `runManageAll`,再判断 active/control plane,然后:
   - `runPerpPerpDecisionFast` 走 perp-perp 快路径;
   - carry 四策略走异步慢扫描 `runOnceSafe`。
4. carry 四策略进入 `scanRotationCandidates -> DecideRotation -> PlanCapitalRouting -> dispatchRotationPlan`。

统一成立的地方:

- `UnifiedOpportunity` 是共同机会结构。
- `DecideRotation` 对 hold / close / switch / open 进行组合决策。
- 资金路由在开仓前统一介入。
- control plane 和 rotation runner 是统一入口。
- dashboard 有统一事件、统一排行榜、统一 rotation API。

没有统一的地方:

- 扫描层仍是多套 scanner。
- 执行层仍是多套 executor。
- perp-perp 与 carry 四策略是不同节奏。
- recovery / reconcile / settlement 格式仍分散。
- legacy FundingRateSniperEngine 相关能力仍在代码中,但 live 主路径已 de-wire。

影响:

- 统一带来了真实增益:资金竞争、策略切换、控制面统一。
- 复杂度也真实上升:每个新问题都可能跨 scanner、runner、executor、settlement、dashboard 五层。
- 当实盘出问题时,用户很难判断是策略问题、数据问题、资金问题、执行问题、页面读数问题还是旧路径遗留问题。

下一步:

1. 把"统一"重新定义成三个层级:
   - L1: 统一展示;
   - L2: 统一决策;
   - L3: 统一生命周期。
2. 当前项目最多算 L2 部分成立。下一阶段目标不应是继续加策略,而是把五策略生命周期收敛:
   - 统一 settlement record;
   - 统一 position state;
   - 统一 execution incident;
   - 统一 recover action;
   - 统一 PnL attribution。
3. 明确废弃路径:
   - FundingRateSniperEngine live 能力要么重新接入,要么标注为 legacy only;
   - dashboard 不应把 legacy 的数据和 unified 的数据混在同一语义层。

进入下一步的条件:

- 任意一笔真实交易,能通过一个 `position_key` 串起:候选 -> 决策 -> 资金路由 -> 下单 -> 持仓 -> manage -> close -> settlement -> PnL -> dashboard。
- 不需要知道是哪类策略,也能用同一套审计工具解释生命周期。

### 3.2 五策略机会归属必须互斥,避免同一赚费腿自相残杀

当前事实:

- `defaultScanRotationCandidates` 中四个 carry scanner 分别独立调用:
  - `ScanSingleVenueSpotPerpOpportunities`;
  - `ScanCrossVenueSpotPerpOpportunities`;
  - `ScanSingleVenuePerpSpotOpportunities`;
  - `ScanCrossVenuePerpSpotOpportunities`。
- 它们拿的是同一份 `symbols`、`fundingByVenue`、`marketsByVenue`、`availableUSDTByVenue`,彼此看不到对方输出。
- 合并时只是顺序 `append`: single spot-perp、cross spot-perp、single perp-spot、cross perp-spot,后面再 append perp-perp candidates。

这意味着当前筛选是"独立但不互斥":每个策略都在全量机会空间里认领自己能解释的候选,然后把冲突交给 `DecideRotation` 和资金余额去事后解决。

真正已经隔开的维度:

- 正费率与负费率天然分开。
- spot-perp 家族吃正 funding;
- perp-spot 家族吃负 funding;
- 这一刀是干净的,不需要动。

真正没隔开的维度:

- 同一费率符号下的"单所 vs 跨所"没有互斥。
- 同一条正费率 perp 腿可能同时被:
  - `single_venue_spot_perp` 认领:同所买现货 + 同所空 perp;
  - `cross_venue_spot_perp` 认领:别所买现货 + 该所空 perp;
  - `perp_perp` 认领:空高费 perp + 多低费 perp。
- 负费率侧同理:同一条负费率 perp 腿可能同时被 `single_venue_perp_spot` 与 `cross_venue_perp_spot` 认领。
- `perp_perp` 更复杂:它可能有两条赚费腿。空高费 perp 收正 funding;如果低费腿为负 funding,多低费 perp 也可能收 funding。因此一条 perp-perp 候选可能同时落入两个互斥组。

影响:

- 一个正费率币可能产生"1 条单所 + N 条跨所 + 可能 1 条 perp-perp"候选。
- 这些候选本质上都在抢同一条赚费腿、同一个 venue 钱包、同一个 max concurrent 槽位。
- 进入 `DecideRotation` 后,贪心只会选一个,其他被 `venue_balance_insufficient`、`max_concurrent_reached`、资金路由失败等原因挤掉。
- 这会制造"每个策略都看起来有机会,但整体开不出来"的假繁荣和内耗。

正确目标:

> 统一系统不应让五个策略在同一条赚费腿上互相竞价,但也不能在还不知道谁能开出来之前把备选硬删。正确形态是:同一 `earning_leg_key` 形成互斥组,决策/开仓层在已知可开性后只开其中一个;首选不可 funded 时,自动回退同组次优。

必须避免的错误做法:

- 不要在 carry 四策略合并前直接硬删同 key 备选。
- 不要让"单所优先"成为无条件冠军。
- 不要只在 carry 合并点做互斥,因为 perp-perp 快路径不经过这个点。

互斥组规则:

1. 先按费率符号分流:
   - 正 funding -> spot-perp 家族;
   - 负 funding -> perp-spot 家族。
2. 去重键必须是"赚费腿",不是 symbol:
   - key = `symbol + earning_venue + funding_direction`;
   - 同一 symbol 的正 carry 和负 carry 不冲突;
   - 真正要避免的是同一条赚费腿被多个策略重复认领。
3. 一条候选可以挂多个 `earning_leg_key`:
   - 四类 carry 通常只有一个 earning leg;
   - `perp_perp` 可能有两个 earning legs;
   - 候选必须记录 `primary_earning_leg_key` 和 `occupied_earning_leg_keys`。
4. 同 key 内不要先删候选,而是全部保留为互斥组成员:
   - `single_venue_spot_perp`;
   - `cross_venue_spot_perp`;
   - `single_venue_perp_spot`;
   - `cross_venue_perp_spot`;
   - `perp_perp`。
5. `perp_perp` 的归属方式必须提前定死,不能实现时临场猜:
   - 方案 A: 用收益更大的那条腿做 `primary_earning_leg_key`,另一条腿只登记为占用,不参与排序;
   - 方案 B: 只允许 `perp_perp` 吃"纯 perp"机会,任一腿能被 spot-perp / perp-spot 更低成本捕获时让位;
   - 方案 C: 允许 `perp_perp` 同时占用两个 key,但调度器必须把它作为一个原子候选处理,不能在两个组里分别被选中或分别 fallback。
6. 互斥组内选择顺序:
   - 先过滤本轮不可 funded / 不可执行 / 不满足 min-notional / 触发 reserve 的候选;
   - 在可开候选里按扣完全部成本后的 `ExpectedNetEVUSDT` 或风险调整 EV 排序;
   - 单所优先只做 EV 几乎打平时的 tie-break,不是无条件优先;
   - 首选失败时,回退同组次优,而不是让整条赚费腿空掉。

重要边界:

互斥组是必要不充分。它能消除重复候选造成的虚假繁荣和同腿内耗,但治不好以下结构性开仓失败:

- 资金碎片化;
- 跨所不自动划转;
- reserve / min-notional 差几分钱;
- funding entry window 未到;
- 幽灵腿、快照过期、慢 venue;
- 极端币实际不可成交。

所以 3.2 不能被当成"让系统开得出来"的主解。真正决定可开性的更大杠杆仍是 6.1/Phase 3:砍所、合并资金、少策略、验证 `<1k USDT` 下是否有结构性容量。

落地位置:

- 不能只放在 `unified_rotation_runner.go` 四个 carry scanner 合并前。
- 当前 perp-perp 走 `runPerpPerpDecisionFast`,每 60s 独立构造候选、独立 `decideRotation`、独立 `dispatch`。
- carry 慢路径还会 `filterRotationCandidatesWithoutStrategy(..., UnifiedStrategyPerpPerp)` 把 perp-perp 排除掉。
- 因此互斥必须放在两条路都会遵守的共享卡点:
  - 方案 A: 把 perp-perp 候选也并入统一候选全集,统一按 `earning_leg_key` 分组决策;
  - 方案 B: 在开仓/派发前建立共享 `earning_leg_claim` 占用表,快路径和慢路径开仓前都必须声明并检查该赚费腿是否已被占用;
  - 方案 C: 先做诊断模式,在两条路径都计算 `earning_leg_key`,只记录冲突不改变行为。
- P0 应先做方案 C,避免立刻改变实盘行为;P1 再切到 A 或 B。

诊断字段:

- `earning_leg_key`;
- `primary_earning_leg_key`;
- `occupied_earning_leg_keys`;
- `mutual_exclusion_group_id`;
- `atomic_candidate_id`(用于 `perp_perp` 这类多 key 候选);
- `candidate_fundable`;
- `candidate_rank_in_group`;
- `selected_candidate_id`;
- `fallback_from_candidate_id`;
- `routed_reason`;
- `winner_strategy`;
- `losing_strategy`。

页面与诊断要求:

- 开仓漏斗必须显示:
  - 路由前 raw candidates;
  - 按赚费腿分组后的 mutual-exclusion groups;
  - 多 key 候选数量,特别是 `perp_perp`;
  - 每组可 funded 候选数;
  - 每组最终选择候选;
  - 首选不可开时是否 fallback 到同组次优;
  - 每个策略专属候选数。
- 排行榜不能只显示"谁被拒绝",还要显示"谁和谁互斥、谁当前可 funded、最终为什么选它"。

进入下一步的条件:

- 任一轮中,同一 `earning_leg_key` 最多只能实际开出一个仓,但备选候选可以存在于互斥组中。
- 多 key 候选必须原子处理:同一条 `perp_perp` 不能在 A 组被选中、又在 B 组被当作次优 fallback。
- 互斥组首选不可 funded 时,能自动评估并尝试同组次优,不因过早硬删导致整条赚费腿空掉。
- executable yield 上升:每轮不同 `earning_leg_key` 实际开出的数量增加,而不是仅仅把"互斥拒绝数"刷到 0。
- `venue_balance_insufficient` 和 `max_concurrent_reached` 不再被重复候选内耗放大,且不会因为硬删备选而牺牲可开性。
- perp-perp 快路径与 carry 慢路径都遵守同一个 `earning_leg_key` 占用或统一候选决策。
- 完成 3.2 后仍必须分别统计资金碎片化、进场窗、快照过期、幽灵腿导致的不可开;不要把这些问题误归因给互斥组。
- 策略开不出来时,能区分是:
  - 没有机会;
  - 互斥组内无可 funded 候选;
  - 被同组更高 EV 候选占用;
  - 资金不足;
  - 数据不新鲜;
  - 执行失败。

---

## 4. 研究问题

### 4.1 alpha 假设方向对,但还没有被证明

项目抓的是资金费 / carry:

- 正 funding: spot + short perp。
- 负 funding: long perp + borrow/short spot。
- 跨所 perp-perp: short high funding perp + long low funding perp。
- 单所/跨所组合。

这些方向不是胡编。代码里有:

- funding capture probability;
- fee;
- slippage;
- borrow cost;
- rollback cost;
- basis stress;
- funding entry window;
- liquidity / execution score;
- dust residual;
- EV gate。

问题不是没有 economics,而是 economics 没有被真实兑现验证。

当前缺口:

1. 没有足够真实样本证明 `ExpectedNetEVUSDT` 与最终 `net_pnl` 正相关。
2. 没有按策略、交易所、symbol liquidity bucket、funding regime 分层的胜率和偏差。
3. 没有清楚回答:
   - funding 预测兑现率是多少?
   - 成交滑点相对模型高多少?
   - 极端 APR 是 alpha 还是脏数据?
   - 小资金在多所分散后,机会可部署率是多少?
4. 部分关键参数仍像工程默认值,不是研究校准结果。

下一步:

1. 建 `prediction_vs_realization` 审计:
   - candidate EV;
   - open decision EV;
   - execution estimated cost;
   - realized funding;
   - realized fees;
   - realized slippage;
   - realized borrow;
   - final net PnL;
   - error = realized - expected。
2. 每个策略至少积累一批可用样本后再开放更高预算。
3. 把极端 APR symbol 单独分桶,不能和 BTC/ETH/SOL 这种主流流动性放在同一统计里。
4. 对 "net_ev<=0 但 APR 很高" 的候选做脏数据归因,不要让排行榜刺激人工误判。
5. 预先写下策略枪毙线,不能等样本出来后再解释。建议至少包含:
   - 100 笔后 realized EV 的保守置信下界仍 `<= 0`;
   - expected EV 与 realized PnL 相关性不显著;
   - realized slippage / rollback / dust 吞掉超过 50% 模型 EV;
   - 可执行机会占正 EV 机会比例长期过低,导致容量不可部署。

进入下一步的条件:

- 至少 100 笔真实闭环结算样本,且每笔有 expected vs realized。
- 每类策略能回答:
  - 平均 expected EV;
  - 平均 realized PnL;
  - 偏差来源 top 3;
  - 最大单笔负偏差;
  - 是否仍为正期望。
- 如果样本不足,只能称为实验,不能称为策略成立。
- 如果触发枪毙线,该策略应退役或降级为研究观察,而不是继续调参续命。

---

## 5. 执行与影子模式问题

### 5.1 dry-run 不能证明 live

当前 dry-run 最大问题:

- `!live` 分支直接返回 filled。
- 这能验证代码路径不 panic,但不能验证真实市场失败路径。

它测不到:

- IOC 不成交;
- 部分成交;
- maker chase 超时;
- 查单 unknown;
- 第一腿成功第二腿失败;
- rollback 盘口失效;
- 交易所限频;
- 保证金瞬时不足;
- 交易所 symbol 规则变化;
- 残留 dust 的真实成本。

影响:

- shadow 看起来越平滑,越可能误导实盘。
- 最危险的亏损路径恰好没有被 dry-run 训练过。

下一步:

1. 将 shadow 分成三种:
   - logic dry-run: 不触达交易所,只测代码;
   - market shadow: 真实拉盘口/深度/规则,模拟成交概率和 partial fill;
   - micro-live: 极小金额真实下单,用于验证交易所路径。
2. 给 shadow 加失败注入:
   - 第一腿成交,第二腿失败;
   - 查单 unknown;
   - depth 不足;
   - wallet transfer 失败;
   - funding snapshot stale;
   - order amend/retry 超时。
3. 页面上明确标注 shadow 可信等级,不能把 perfect-fill dry-run 当成实盘预演。

进入下一步的条件:

- 每个执行器至少有一次失败注入演练记录。
- shadow 报告能列出:模拟成交率、模拟滑点、模拟裸腿风险、模拟 rollback 成本。
- micro-live 样本证明真实下单路径和账本路径一致。

### 5.2 数据新鲜度与 REST 慢扫描会直接伤害执行

已观察到的具体病灶:

- `records/crossvenue/latest_snapshot.json` 中 OKX 一次扫描 `elapsed_ms` 约 `48526`,接近 48 秒。
- scanner 周期、快照年龄、funding entry window、执行时盘口之间存在时间错位。
- 当机会靠近结算窗口才允许开仓时,几十秒级扫描延迟会放大:
  - `funding_entry_window_not_reached`;
  - funding snapshot stale;
  - 幽灵腿/无效快照;
  - 排行榜机会与真实可执行机会不一致。

影响:

对资金费策略而言,数据慢不是普通性能问题,而是 alpha 损耗和误判来源。尤其是小资金追极端 funding 时,窗口本来就窄,数据慢会把"看上去有机会"变成"永远扣不动扳机"。

下一步:

1. 在机会链路中记录每个候选的 `data_age_ms`、venue fetch latency、depth age、funding snapshot age。
2. 页面排行榜不能只显示 APR/EV,必须显示数据年龄和 freshness verdict。
3. 对慢 venue 单独降权或临时排除,不要让它污染统一机会排名。
4. 判断是否需要 websocket / 增量缓存 / venue 并发优化,而不是继续用 30 分钟 snapshot staleness 兜底。

进入下一步的条件:

- 可执行候选的关键数据年龄稳定低于 entry window 的可接受比例,例如小于窗口长度的 10%-20%。
- 任意开仓失败都能区分是策略不满足、资金不满足,还是数据过期导致。

---

## 6. 资金与资本路由问题

### 6.1 小资金多所分散会放大摩擦

项目资金规模约数百 U,分散到 Binance / Bybit / Gate / OKX 后,每所可用资金变成几十到百 U。问题不是"钱太少不能做",而是:

- 每个策略有最小名义;
- 每所要留 reserve;
- perp margin 与 spot wallet 不同;
- 统一账户与非统一账户规则不同;
- 跨所资金不能自动搬;
- 差几分钱可能导致机会不可执行。

当前资本路由能力:

- 所内划转可以执行。
- 跨所调拨只做 suggestion,不会自动执行。
- 如果钱在错误交易所,机会会被标记 unfundable 或被阻断。

影响:

- 系统扫描到的机会不等于可部署机会。
- 小资金被拆碎后,可部署容量可能接近零。
- 页面如果只显示 APR/EV,会误导用户以为机会很多。

下一步:

1. 定义 `deployable capital`,而不是只看 total equity。
2. 每轮扫描输出:
   - raw opportunities;
   - positive EV opportunities;
   - executable opportunities;
   - blocked by capital;
   - blocked by rebalance;
   - blocked by wallet type;
   - blocked by venue transfer。
3. 资金层优先做"少所、少策略、少路径"。
   - 先让一到两类策略在两三个交易所稳定闭环;
   - 不要同时追求五策略全开。
4. 把"容量是否结构性成立"作为研究问题,不是工程问题。必须回答:
   - 在 `<1k USDT` 资金量级下,单所/双所是否有足够正 EV 且可执行的机会;
   - 正常流动币是否能覆盖成本闸;
   - 如果只有极端币能过闸,这些机会是否只是脏数据/不可成交/残留制造机;
   - 四所分散是否反而把小资金切碎到不可部署。

进入下一步的条件:

- dashboard 能展示"当前 385U 中,真实可用于新开仓的是多少,分布在哪些 venue/wallet"。
- 每个 rejected opportunity 都能给出资金层原因和修复动作:
  - 需要所内划转;
  - 需要跨所转账;
  - 需要降低 notional;
  - 需要关闭某策略占用。
- 如果 `<1k USDT` 下单/双所无法形成稳定容量,应优先砍所、砍策略或换方向,而不是继续优化跨所资金路由。

---

## 7. 风控问题

### 7.1 风控组件多,但主路径收敛能力未证明

已有能力:

- account risk watcher;
- daily loss / drawdown / strategy cumulative loss / open notional;
- global halt;
- allow_new_opens;
- allow_manage_orders;
- allow_verified_closes;
- auto flatten;
- untracked exposure action;
- orphan / dust recovery;
- single-writer lease;
- state envelope checksum。

核心问题:

1. 权益快照数据质量缺闸:毛刺能设高水位,残缺读能进入总额。
2. 最强实时熔断逻辑挂在 legacy FundingRateSniperEngine,不是 unified runner 主路径。
3. 60s watcher 对 fast crash 和单腿事故不够敏感。
4. 资金流识别机制存在,但本次 `50U` 提现没有被检测日志证实,属于"存在但失效",不是简单的"不够强"。
5. `auto_flatten_on_hard_drawdown_enabled: true` 已开启,但二级 auto-flatten 使用 `RealEquityDrawdownUSDT / RealEquityPeakUSDT`,不吃 fund-flow 调整,也不校验权益快照质量;这会把毛刺假峰、残缺零读、提现台阶都计入破坏性全平触发条件。
6. 风控触发后的页面指导不足,用户可能和系统风控"拔河":系统 halt,人工又 start。

下一步:

1. 把风险事件分为:
   - 交易亏损;
   - 外部资金流;
   - 估值噪声;
   - 权益快照毛刺;
   - venue 残缺读;
   - 交易所数据异常;
   - 真实敞口事故。
2. hard halt 与 auto-flatten 页面必须显示:
   - 触发原因;
   - 是否由提现/充值引起;
   - 是否有真实持仓;
   - 是否需要 flatten;
   - 是否只需确认资金流。
3. 在权益快照质量闸完成之前,auto-flatten 应立即从"自动执行"降级为"建议全平/需要确认",除非有真实裸腿、保证金危险或人工确认。
4. 将实时风险能力从 legacy engine 抽象成 unified risk monitor:
   - 读所有策略持仓;
   - 读实时盘口/持仓;
   - 统一判断单 symbol 异常、账户异常、裸腿异常。

进入下一步的条件:

- 单样本毛刺不能刷新 `RealEquityPeakUSDT`。
- 残缺 venue 读数不能进入 hard halt 或 auto-flatten。
- 一次提现不会被误判为交易亏损 hard halt,也不会吃掉 auto-flatten 安全垫。
- 一次真实裸腿能在 5s-10s 内被页面和告警识别。
- 每个 halt 都必须有"解除条件",而不是靠人工猜。

### 7.2 当前 auto-flatten 是 P0 风险,不是安全加分项

当前配置中 `auto_flatten_on_hard_drawdown_enabled: true`。这本来是保护账户的最后保险,但在权益快照质量不可信时,它会变成误伤健康仓位的破坏性动作。这里的风险比"提现吃掉安全垫"更近:系统已经出现过假峰值和残缺读,而 auto-flatten 正坐在这条不可信信号上。

具体风险链:

1. bybit 单样本毛刺把 `RealEquityPeakUSDT` 抬到约 `454.90U`。
2. 用户提现约 `50U`,权益序列出现干净台阶。
3. 某些采样若只拉到部分 venue,可能把总额算成接近 0 或严重偏低。
4. auto-flatten 判据使用真实现金权益回撤,不使用 settlements,不做资金流调整,也不做 sample quality gate。
5. 假峰 + 提现台阶 + 残缺零读叠加,可能在某一个 tick 把 drawdown 顶过阈值,对健康对冲仓执行破坏性全平。

判断:

> 在权益快照质量闸完成前,开启 auto-flatten 不是风险控制成熟,而是把破坏性动作接在坏传感器上。

下一步:

1. 立即将 auto-flatten 从"自动执行"降级为"强告警 + 人工确认",除非存在真实裸腿或保证金危险。
2. auto-flatten 的触发依据改为 `quality-passed + fund-flow-adjusted real equity drawdown`。
3. 如果无法可靠区分毛刺、残缺读、提现和亏损,禁止用真实现金高水位回撤直接触发破坏性操作。
4. 页面必须展示 auto-flatten 安全垫中有多少来自交易亏损、外部资金流、可疑峰值和无效样本。

进入下一步的条件:

- 外部资金流不会永久抬高 auto-flatten drawdown。
- 毛刺峰值不会成为 auto-flatten peak。
- 残缺读不会触发 auto-flatten。
- auto-flatten 的每次触发都能说明:不是提现、不是估值噪声、不是 dashboard 读数偏差,而是真实账户风险。

---

## 8. 状态、恢复、dust 与残留问题

### 8.1 状态机是强项,但 residual noise 太高

强项:

- positions store 有 schema/version/checksum。
- tmp + fsync + rename 原子写。
- opening_first_leg / opening_second_leg / closing / orphan_single_leg 状态清晰。
- reconcileOnStart 以交易所持仓为准。
- carry leg ownership 试图避免误判跨策略腿。

问题:

- `dust_ledger.json` 中记录数量很高,说明系统长期处理残留、未登记、dust。
- 很多 dust 单笔金额极小,但高频出现会造成:
  - 页面噪音;
  - 审计困难;
  - 用户误判系统事故;
  - 小额余额被持续污染;
  - PnL 与权益桥接更困难。

下一步:

1. dust 不应只是 ledger,而应有聚合视图:
   - by venue;
   - by asset;
   - by strategy;
   - by source;
   - total estimated value;
   - total realized cleanup cost。
2. 区分三类 residual:
   - harmless dust: 小到无法交易;
   - cleanup needed: 可合并处理;
   - risk residual: 有方向性敞口或可造成亏损。
3. 对重复 settlement / repeated dust close 做去重或幂等标记。

进入下一步的条件:

- dashboard 能显示 dust 总价值和风险级别,而不是只堆日志。
- 每个 residual 都有 owner strategy 和 expected action。
- residual 总价值低于账户权益的可接受阈值,例如 `0.1%`。

---

## 9. 页面与控制面问题

页面不是装饰品。对个人实盘系统,页面必须承担"减少误操作、减少误判、减少人工焦虑"的职责。

### 9.1 当前页面能力

已有后端 API:

- `/api/dashboard/summary`
- `/api/performance`
- `/api/earnings`
- `/api/settlements`
- `/api/cross-venue/funding`
- `/api/cross-venue/positions`
- `/api/rotation/status`
- `/api/rotation/capital-snapshot`
- `/api/rotation/positions`
- `/api/rotation/settlements`
- `/api/unified-events`
- `/api/unified/leaderboard`
- `/api/domain-events`
- `/api/execution-incidents`
- `/api/strategy/start`
- `/api/strategy/stop`
- `/api/strategy/flatten-all`

这说明页面已经不是空壳。它能读运行状态、资金、事件、机会、settlements、控制面。

### 9.2 当前页面的核心缺口

问题不是没有数据,而是没有形成操作决策闭环。

页面现在需要回答七个问题:

1. 我的钱现在在哪?
2. 相比昨天/高水位,变化来自交易、提现、估值还是未知?
3. 当前系统是否允许开仓?
4. 如果没开仓,最大阻塞原因是什么?
5. 如果有机会,它为什么可执行?
6. 如果有风险,我该 stop、flatten、还是只确认资金流?
7. 下一步动作是什么,做完后用哪个指标验收?

当前页面更像"数据面板",还不是"决策面板"。

### 9.3 页面应该新增的核心模块

#### A. 账户权益桥接卡片

显示:

- 起始权益;
- 当前权益;
- 外部资金流:充值/提现;
- 交易 realized PnL;
- 未实现 PnL;
- 费用/资金费/借币费;
- residual/dust 估值;
- 未解释差额。

页面判断:

- 未解释差额小:绿色。
- 未解释差额中等:黄色,禁止扩大仓位。
- 未解释差额大:红色,禁止新开仓。

#### B. 开仓漏斗

显示:

- raw candidates;
- positive EV;
- passes hard reject;
- passes capital;
- passes entry window;
- executable now;
- actually dispatched;
- opened successfully;
- failed with reason。

目的:

- 不再只看 "APR 很高"。
- 直接看到系统为什么空转。

#### C. 资金可部署地图

显示:

- by venue / wallet 的 total / available / reserved / required;
- 机会需要的钱在哪;
- 差额是多少;
- 是所内划转可解决,还是必须跨所转账。

#### D. 风控解释卡

显示:

- 当前是否 halt;
- halt 来源: global / account risk / control plane / emergency file;
- 是否真实持仓;
- 是否需要 flatten;
- 解除 halt 的条件;
- 最近一次人工 start/stop 审计。

#### E. 实盘可信度卡

显示:

- shadow/live 一致性等级;
- 最近 N 次 dry-run 是否只是 perfect-fill;
- 最近 N 次 micro-live 成功率;
- 执行失败注入覆盖率。

#### F. Dust / Residual 风险卡

显示:

- residual 总额;
- risk residual 数量;
- harmless dust 数量;
- top assets;
- cleanup action。

### 9.4 页面进入下一步的条件

页面改造不应先追求好看,应先追求决策闭环。

进入下一步的条件:

- 用户打开首页 30 秒内能判断:
  - 系统现在安全吗?
  - 钱是否解释得清?
  - 为什么没开仓?
  - 是否需要人工动作?
- 每个红色状态必须附带:
  - 原因;
  - 影响;
  - 建议动作;
  - 动作后的验证指标。

---

## 10. 运营与人工干预问题

当前系统已经有很多控制按钮:

- strategy start/stop;
- rotation start/stop;
- flatten-all;
- runtime capital;
- config preview/rollback;
- emergency flatten;
- global halt。

问题:

- 按钮多不等于操作清晰。
- 如果风控因为提现触发 halt,用户又手动 start,这不是用户错,是页面没有解释"这个 halt 是否真实风险"。
- 如果风控因为毛刺峰值或残缺零读触发 halt,页面必须明确标成"数据质量事故",不能让用户误以为策略在亏钱。
- 如果机会一直被 entry window / funding snapshot / capital block 拒绝,用户需要看到"等待即可"还是"必须调整资金/参数"。

下一步:

1. 建 runbook 状态机:
   - 正常运行;
   - 等待结算窗口;
   - 资金不足;
   - 风控暂停;
   - 需要核账;
   - 需要人工 flatten;
   - 禁止操作。
2. 每个状态只允许一组动作。
3. 每个动作后页面显示验收条件。

进入下一步的条件:

- 用户不需要看日志也能知道该做什么。
- 控制面不再允许"不知道原因地反复 start/stop"。

---

## 11. 分阶段路线图

### Phase 0: 停止扩大复杂度

目标:

- 不加新策略。
- 不扩大仓位。
- 不继续优化排行榜和参数。
- 先把权益快照质量闸、审计和解释链打通。

需要做:

1. 固定当前交易范围。
2. 给 `equity_snapshots` 加数据质量闸,拒绝毛刺设峰和残缺读入账。
3. 立即将 auto-flatten 从自动执行降级为强告警/人工确认。
4. 调出并记录所有充值/提现,验证 `50U` 台阶。
5. 增加五策略 `earning_leg_key` 互斥组诊断,先只观测、不改变实盘开仓。
6. 整理所有 settlement 来源。
7. 在 quality-passed equity 基础上计算权益桥接。
8. 给 dashboard 加"未解释差额"、"权益快照质量"和"候选路由内耗"红线。

进入 Phase 1 条件:

- 最近 7 天权益快照样本质量可控,无毛刺设峰、无残缺读入账。
- 最近 7 天权益桥接误差可控。
- 外部提现/充值不会污染交易亏损判断。
- auto-flatten 不会被外部资金流或估值噪声触发。
- 每轮能统计 raw candidates -> mutual-exclusion groups -> selected/fallback candidates,并识别同一赚费腿重复认领。

### Phase 1: PnL 可信闭环

目标:

- 每笔交易从 expected EV 到 realized PnL 可对账。

需要做:

1. 统一 settlement schema。
2. 每笔交易写 prediction snapshot。
3. close 后写 realized attribution。
4. 输出 expected vs realized 报告。

进入 Phase 2 条件:

- 至少 100 笔闭环样本。
- realized 与 expected 偏差可解释。
- 每类策略至少知道主要亏损来源。
- 对每类策略执行预先承诺的枪毙线:无 edge 就退役,不是无限调参。

### Phase 2: 执行可信闭环

目标:

- shadow 不再是 perfect-fill 幻觉。

需要做:

1. dry-run 分级。
2. 增加失败注入。
3. 做 micro-live 极小金额验证。
4. 统计真实开仓成功率、部分成交率、rollback 成功率。

进入 Phase 3 条件:

- 每个执行器的主要失败路径都被测试或演练过。
- micro-live 与账本闭环一致。

### Phase 3: 资本效率闭环

目标:

- 先判断小资金容量是否结构性成立;若成立,再让小资金真正可部署,而不是散在各所空转。

需要做:

1. 明确 deployable capital。
2. 在 `<1k USDT`、单所/双所/少策略约束下重新测可执行容量。
3. 页面展示资金阻塞。
4. 先少所少策略稳定跑。
5. 必要时减少交易所数量,而不是继续扩。
6. 如果正常流动币长期过不了成本闸、只有极端币能过闸,优先判定当前方向容量不足,而不是继续加工程复杂度。

进入 Phase 4 条件:

- 资金阻塞率显著下降。
- executable opportunities 占 positive EV opportunities 的比例稳定。
- 已回答"当前资金量级下是否有足够容量"这个战略问题;如果答案是否,进入砍策略/换方向,不进入 Phase 4。

### Phase 4: 策略扩展

目标:

- 在可信审计、可信执行、可信资金路由基础上再扩策略。

禁止:

- 在 PnL 未解释、shadow 不可信、资金不可部署的状态下继续加第六类策略。

进入条件:

- 前三阶段指标全部达标。

---

## 12. 优先级排序

### P0: 必须先做

1. 权益快照数据质量闸:完整 venue、异常值、peak 确认、无效样本拒绝。
2. 立即降级 auto-flatten,禁止坏权益信号触发破坏性全平。
3. 权益桥接与资金流归因。
4. 验证 `50U` 提现流水,并解释资金流检测为何未触发。
5. 统一 settlement / PnL 聚合。
6. dashboard 首页显示未解释差额、权益快照质量、候选路由内耗。
7. dry-run 可信度标注。
8. 开仓漏斗与阻塞原因聚合。
9. 五策略 `earning_leg_key` 互斥组诊断,避免重复候选内耗继续污染开仓漏斗。

优先级说明:

- 1-2 是止血项,因为它们关系到真钱误平风险。
- 3-6 是账户可信度项,因为它们决定系统是否知道钱去哪了。
- 7-9 是交易质量项,包括互斥组诊断;它重要,但不应排在 auto-flatten 和权益快照质量闸之前。

### P1: 紧随其后

1. expected vs realized 审计。
2. dust / residual 聚合风险视图。
3. 资金可部署地图。
4. control plane 状态机化。
5. 风控 halt 解除条件显示。
6. 数据新鲜度与慢 venue 降权/剔除。
7. 将 `earning_leg_key` 互斥组从诊断模式切到共享占用/统一候选决策模式,覆盖 carry 慢路径和 perp-perp 快路径。

### P2: 稳定后再做

1. 实时风险监控统一化。
2. 跨所资金调拨自动化或半自动化。
3. 策略参数校准。
4. 更好的 UI 视觉层。

### 暂时不要做

1. 不要再加新策略。
2. 不要为了更多开仓盲目放宽 EV gate。
3. 不要把极端 APR 当成 alpha 证据。
4. 不要继续用 perfect-fill dry-run 给自己信心。

---

## 13. 重新评分

在快照序列复核后,项目不应被判定为确认交易漏损 `69U`:假峰值和提现基本解释了大部分差额,真实交易亏损证据接近 `0`。但这不能抬高系统评分,因为它暴露了更底层的问题:权益计量层不可信,并且 auto-flatten 正接在这条不可信信号上。

评分:

- 工程架构: `6.0 / 10`
- 量化研究: `2.5 / 10`
- 量化实现: `4.5 / 10`
- 风险控制: `4.0 / 10`
- 实盘可操作: `3.2 / 10`
- 资本配置价值: `2.5 / 10`
- 页面与操作闭环: `3.8 / 10`
- 综合: `3.7 / 10`

解释:

- 工程能力明显高于普通个人项目。
- 研究证明明显不足。
- 没有证据显示发生实质交易亏损,这是好消息。
- 权益快照会毛刺设峰、残缺读入账,这是同级别坏消息。
- 资金流检测没有识别 `50U` 提现台阶,这是 P0 失败。
- auto-flatten 已开启,但其输入依赖尚不可信,风险控制不应升分。
- 页面数据多,但还没变成决策系统,也不能阻止用户和风控拔河。

---

## 14. 最直接的一句话

你现在不缺交易系统的零件,缺的是一条能证明"每一分钱从哪里来、到哪里去、为什么赚、为什么亏、下一步该做什么"的主线。

更狠一点说:你这次大概率没有亏钱,但你的仪表会把毛刺当峰值、把残缺读当总额、把提现当回撤,还把自动全平接在这个坏仪表上。在这条主线打通之前,继续堆策略、堆页面、堆参数,都是把复杂度倒进一个还没校准的仪表盘里。
