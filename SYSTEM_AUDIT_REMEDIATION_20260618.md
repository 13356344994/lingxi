# Lingxi 系统审判与整改思路（2026-06-18）

> **文档结构**：§1–§8 = 2026-06-18 审计时点结论（保留主体，部分运行态描述已被 §9–§10 整改超越）；§9–§10 = 2026-06-19 整改落地记录（以代码为准）。

本文只记录架构判断、关键问题和整改方向，供后续实现者（例如 Composer 2.5）拆任务执行。
结论以当前代码实现为准，文档只作为旁证。

## 1. 总判

Lingxi 不是玩具项目。它已经具备真实扫描、执行、持仓、恢复、面板、影子/实盘切换等个人量化项目所需的核心工程件。

但它目前也不是一个真正闭环的“五策略统一交易操作系统”。更准确的定位是：

- `perp-perp` 跨所资金费策略已经进入小额 live 阶段，并走独立 `CrossVenueExecutor`。
- 四类 spot/perp carry 策略也已经被 `rotation_control.json active=true` 推入 live 路径；`config.yaml` 中 `unified_rotation.live_trading_enabled=false` 已被持久化控制状态覆盖。
- 五类机会共享了部分 scanner、candidate、dashboard 和轮动结构，但没有共享一个完整的实盘交易大脑。
- 资金路由、风控控制面、恢复/flatten 语义仍然分裂。
- 研究证据不足以证明多策略统一后带来稳定正增益。

最核心问题不是“有没有做很多模块”，而是：系统复杂度已经上来了，但统一性、证伪能力和资本效率还没有同步跟上。

## 1.1 当前真钱敞口处置（最高优先级）

这不是中期整改问题，而是“现在这一刻真钱怎么办”的问题。

已核实事实：

- `records/rotation_control.json` 当前为 `active: true`，更新时间 `2026-06-18T10:05:32Z`。
- `UnifiedRotationRunner.InitState` 会用持久化 `active` 覆盖 config 默认值。
- `UnifiedRotationRunner.isLive()` 直接返回 `active`。
- `records/single_venue_spot_perp/settlements.jsonl` 已有 2026-06-18 的 SVSP settlement。
- SVSP `!live` 影子路径在 `executeOpen` 中提前 return，不落真实台账；因此 settlement 是 live 路径证据。

直接结论：

- 四策略 rotation 不是“shadow 待证明”，而是已经在用真钱 live。
- 当前是 `perp-perp live + rotation live` 两条循环同时存在，double-sizing / 控制面分裂是当前风险，不是未来风险。
- `config.yaml` 看起来是 shadow，但运行时持久化状态实际是 live；这本身就是控制面分裂的已发生事故。

当前建议：

1. 在 churn bug 修复、PnL reconciliation 通过、live path 风控覆盖确认前，先把 `rotation_control.active` 关回 `false`。
2. `perp-perp` 可以暂时保留小额 live，但必须确认 `max_position_usdt=60`、`max_concurrent=8`、openings control、halt 都真实生效。
3. 写下当前最大可容忍亏损（以账户 ~435U 为锚的**建议初值，需你确认/校准**；这些线必须是具体数字，否则 halt 纪律不成立）：
   - rotation 单日最大可容忍亏损：建议 ≈ 账户 1%/日 ≈ 4–5U
   - perp-perp 单日最大可容忍亏损：建议 ≈ 账户 1%/日 ≈ 4–5U
   - 全账户单日合计最大可容忍亏损：建议 ≈ 账户 2%/日 ≈ 8–10U（与代码里休眠的 `MaxDailyLossUSDT` 默认 25U 对齐或更紧）
   - 全账户累计回撤 halt 线：建议 ≈ 账户 5–6% ≈ 22–26U（对齐 `MaxAccountDrawdownUSDT` 默认 60U 或更紧）
4. 到线后执行 halt，而不是继续“采数据”。

执行顺序：

1. 先决定并执行 rotation 是否关回 false。
2. 再修账本可信度。
3. 再修 CVSP churn bug。
4. 再确认账户级风控覆盖当前 live path。
5. 再恢复或扩大任何四策略 rotation live。

## 1.2 策略意图 vs 代码现状（核心，勿偏离）

策略意图很明确：perp-perp、同所 spot-perp、跨所 spot-perp、同所 perp-spot、跨所 perp-spot 这五类**同步扫描**进同一候选池，**按预期收益挑最高的开单**；跨结算周期时用「续持预期收益 vs 换新预期收益」对比，决定 HOLD / SWITCH / CLOSE。这套设计是对的、内在自洽的；**本文档的终点是让代码真正实现它（下文路线 B），而不是把它退化成四策略。**

当前代码与该意图的差距，一句话：**决策层已经基本实现了五策略择优轮动，但执行层对 perp-perp 断了线。**

- 决策层（已实现）：五类（含 perp-perp）进同一候选池（`unified_rotation_runner.go:681-689`）；`DecideRotation` 用 `rotationComparableWindowScore` 统一排名、择优、跨周期 HOLD/SWITCH/CLOSE（`unified_rotation.go:232-271, 420-464`），**不按策略类型过滤**。
- 执行层（未实现 perp-perp）：`NewUnifiedRotationRunner` 只绑定 4 个执行器，无 perp-perp binding（`unified_rotation_runner.go:138-150`）。于是 perp-perp 能参与比分、却不能被统一大脑开/换/平。

由此产生三个直接违背策略意图的行为（代码细节见 2.3），必须优先堵住：

1. **幽灵换仓**：决策选中 perp-perp 做 SWITCH 时先平掉旧仓（`unified_rotation_runner.go:751-753`），再开 perp-perp 时因无 binding 被跳过（`:782-788`）→ 平了正在工作的仓、新仓没开。
2. **挤占名额**：perp-perp 在计划里占 `MaxConcurrent` 名额并扣资金（`unified_rotation.go:258-268`），dispatch 时又被跳过 → 把真能开的四类候选挤下去。
3. **perp-perp 持仓不进轮动**：`collectHeldPositions` 只遍历 4 个 binding（`unified_rotation_runner.go:709-732`），perp-perp 真仓不在 `held[]`，无法被统一大脑跨周期换/平，只能由独立 `CrossVenueExecutor` 自管。

另一个对该策略致命但易被忽略的点：**轮动择优的质量 = 喂进去的「预期收益」的质量。** 当前 EV 闸把真实手续费按 `1e-5` 计（`config.yaml:24`），且账本 realized 与真实 equity 差约 13U，因此「挑收益最高」常挑出**真实成本下其实为负**的候选，「跨周期对比」用的 realized 也不准。修准 EV 输入与账本，是让这套轮动选对仓的前提（见 P0-A / P1-A）。

## 2. 代码证据摘要

### 2.1 当前生产形态是 perp-perp live，统一轮动也 live

`config.yaml` 中：

- `cross_venue_executor.enabled: true`
- `cross_venue_executor.live_trading_enabled: true`
- `unified_rotation.enabled: true`
- `unified_rotation.live_trading_enabled: false`

但 config 不是最终事实。`records/rotation_control.json` 当前 `active: true`，且 `InitState` 会用持久化 active 覆盖 config 默认值；`isLive()` 直接返回 active。

因此当前真实形态是：

- `perp-perp` 跨所执行器 live。
- 四策略 `UnifiedRotationRunner` 也 live。
- 两条 live 循环同时存在，并共享同一组 venue/wallet 资源。

SVSP settlement 已落盘进一步证明四策略不是 shadow。影子路径不会落真实 settlement。

### 2.2 `run.go` 明确拆成两个交易大脑

`internal/app/run.go` 中装配逻辑：

- `CrossVenueScanner` 常驻。
- `CrossVenueExecutor` 独立启动。
- `UnifiedRotationRunner` 另行启动。
- 注释明确写明：`perp-perp` 仍走上方独立执行器，不受统一轮动开关影响。

这意味着五策略不是一个统一 executor，而是“一个独立 perp-perp live executor + 一个四策略 live rotation runner”。

### 2.3 `UnifiedRotationRunner` 没有 `perp_perp` 执行绑定

`internal/app/unified_rotation_runner.go` 的 `openOpportunity` 会按 `strategyType` 查 binding。
如果没有 binding，就记录 “OPEN 跳过(无执行器绑定)” 并返回 nil。

当前 binding 覆盖：

- `single_venue_spot_perp`
- `cross_venue_spot_perp`
- `single_venue_perp_spot`
- `cross_venue_perp_spot`

未覆盖：

- `perp_perp`

因此 `perp_perp` 即使进入统一候选集合，也不能通过统一 runner 执行开仓。

这会产生三个违背「五策略择优轮动」意图的具体后果：

1. **幽灵换仓（会真亏钱）**：`dispatchRotationPlan` 处理 SWITCH 时先 `closeHeldVerified` 平旧仓（`unified_rotation_runner.go:751-753`），再 `openOpportunity` 开 perp-perp，因无 binding 命中「OPEN 跳过(无执行器绑定)」返回 nil（`:782-788`）→ 旧仓平了、新仓没开。
2. **挤占开仓名额/资金**：`planUnifiedRotationOpensFrom` 对每个选中候选（含 perp-perp）`openCount++` 并扣资金（`unified_rotation.go:258-268`），perp-perp 占掉 `MaxConcurrent` 槽后又在 dispatch 被跳过 → 真能开的四类候选被挤下。
3. **perp-perp 持仓游离在轮动之外**：`collectHeldPositions` 仅遍历 4 个 binding（`unified_rotation_runner.go:709-732`），perp-perp 真仓不进 `held[]`，无法参与统一的续持/换仓/平仓决策，只由独立 `CrossVenueExecutor` 用其自有价差止损逻辑管理。

### 2.4 轮动决策没有充分消费 scanner 语义

`internal/app/unified_rotation.go` 的 `rotationCanOpenOpportunity` 主要检查：

- `ExpectedNetEVUSDT > 0`
- `MaxConcurrent`
- `MaxPositionUSDT`
- venue 可用余额

但没有系统性检查：

- `RejectReasons`
- `RequiresRebalance`
- 深度/盘口质量是否达到 executor 级别要求
- scanner 对机会质量的更细约束

这会造成 scanner 和 decision layer 的语义不完全一致。

### 2.5 跨所资金路由只记录建议，不自动执行

`internal/app/capital_router_exec.go` 明确写明：

- 所内划转可以执行。
- 跨所建议只记录日志。
- 不执行跨所自动调拨。

这对小资金多所系统影响很大：机会发现增加了，但钱可能不在正确 venue / wallet，导致机会不可执行或需要人工介入。

### 2.6 研究证据不足

`tools/go_no_go/RESULT.md` 对 Binance 单所 spot-perp positive carry 的 90 天结果是 `NO-GO`：

- occupied capital 加权净年化：`-58.23%`
- 正 edge 符号：`0`

这不能否定所有策略，但足以说明：carry 不是天然 alpha，必须用严格成本、basis、滑点和资金占用重新证明。

当前没有充分证据证明：

- 五策略组合级别长期正期望。
- scanner EV 能稳定映射到实际成交后的 realized PnL。
- 统一轮动相对单独运行策略确实提高资本效率。

## 3. 关键问题

### P0-1：五策略统一叙事不成立

问题：

当前系统不是一个统一五策略 executor，而是两个开仓系统并存：

- `CrossVenueExecutor` 管 `perp-perp` live。
- `UnifiedRotationRunner` 管四类 carry。

影响：

- 资本竞争没有统一仲裁。
- 风控、暂停、恢复、flatten 语义不一致。
- dashboard 上容易让操作者误以为“统一轮动管理全部策略”。
- 后续功能越多，事故定位越难。

整改方向：

- **最终目标是路线 B**（真正的五策略同步择优轮动，与策略意图一致），不是把系统退化成四策略。两条路线是「先止血、后接线」，不是二选一替代：
  - 路线 A（临时止血，非终点）：在 perp-perp 还没接进统一 runner 之前，先消除「幽灵换仓 / 挤占名额 / 误导展示」——把 perp-perp 在 unified candidates 中标记 `candidate_only` / `CanExecute=false`，让决策层在生成 plan 前就知道它不可由 rotation 执行（不再用「无执行器绑定」当正常控制流）。这一步只为堵住 1.2 列出的三个 bug，不改变五策略统一的目标。
  - 路线 B（终点，实现策略意图）：把 perp-perp 真正接入统一 runner——补 binding（open/close/manage/reconcile）、把 perp-perp 真仓喂进 `collectHeldPositions`、与四策略共用同一份 capital budget 和 risk gate。完成后五类才真正同步、同台择优、跨周期统一换/平（落地见 Composer 任务 17）。
- 由于四策略 rotation 已经 live，**当下第一步不是架构选择，而是先决定是否把 `rotation_control.active` 关回 false**，直到账本、churn、风控覆盖修好；止血(路线 A)与接线(路线 B)都在地基修好后推进。

### P0-2：控制面分裂

问题：

当前至少存在多套 live/open/active 语义：

- `cross_venue_executor.live_trading_enabled`
- `cross_venue` runtime openings control
- `rotation_control.json active`
- legacy/shadow trading 语义
- dashboard flatten-all 与 emergency CLI flatten 覆盖范围不完全一致

影响：

- 操作者可能以为暂停了“系统”，实际只暂停了一部分。
- 某些 manage/close 路径在 paused/shadow/live 下语义不一致。
- 事故中最怕的不是没有按钮，而是按钮含义不唯一。
- 当前已经出现实证：`config.yaml` 显示 `unified_rotation.live_trading_enabled=false`，但持久化 `rotation_control.json active=true` 使 rotation 实际 live。这个状态足以误导人，包括文档作者。

整改方向：

- 定义一个统一控制模型：
  - `global_halt`
  - `allow_new_opens`
  - `allow_manage_orders`
  - `allow_verified_closes`
  - `strategy_live_enabled`
- 每个 executor 必须显式实现这些语义。
- dashboard 和 CLI 只调用同一套控制 API。
- 文档中明确 emergency flatten 与 dashboard flatten-all 的覆盖范围；若 CLI 已过时，要么删掉，要么重写到当前 position stores。

### P0-3：系统级资本分配没有闭环

问题：

统一轮动看似有 capital routing，但跨所调拨只输出建议，不自动执行。
同时 `perp-perp` live executor 和 rotation runner 没有一个全局 capital allocator 统一仲裁。

影响：

- 小资金被分散到多个 venue，实际可用资金低于账面资金。
- 系统可能发现机会但不可执行。
- 多策略并存时，先启动/先抢到余额的策略可能占用资本，而不是全局最优策略。

整改方向：

- 短期：做只读 capital dashboard，显示各 venue/wallet 的可用资金、预留资金、策略占用、unfundable 原因。
- 中期：做全局 capital budget，不一定自动跨所转账，但必须统一限制每个策略可占用资金。
- 长期：如果确实需要自动跨所调拨，再单独做“人工确认 + 白名单 + 金额上限”的半自动流程。

### P0-4：研究闭环不足，无法证明统一带来增益

问题：

现在系统有 EV 模型，但缺少组合级证伪框架。
已有 go/no-go 结果反而显示单所 spot-perp 主流币在保守成本下不可行。

影响：

- 工程复杂度可能在放大一个未经证明的收益来源。
- 统一轮动可能只是增加扫描面，而不是提高净收益。
- 真实收益可能被 basis drift、手续费、滑点、借币、换仓吞掉。

整改方向：

- 建立统一的 opportunity -> decision -> execution -> realized PnL 归因表。
- 每笔机会记录：
  - scanner predicted EV
  - decision score
  - open cost
  - close cost
  - realized funding
  - realized basis PnL
  - borrow cost
  - slippage
  - final net PnL
- 先证明单策略，再证明组合轮动。
- 没有 attribution 前，不要扩大 live capital。

### P1-1：轮动决策太粗

问题：

`rotationCanOpenOpportunity` 没有充分继承 scanner reject semantics。

影响：

- scanner 认为有质量问题的机会，可能只要 `ExpectedNetEVUSDT > 0` 且余额够就进入 open。
- shadow 到 live 的一致性不足。

整改方向：

- 在 `UnifiedOpportunity` 中明确标准字段：
  - `RejectReasons`
  - `Warnings`
  - `RequiresRebalance`
  - `ExecutionScore`
  - `LiquidityScore`
  - `MinDepthUSDT`
- `DecideRotation` 必须对这些字段有显式规则。
- 不允许“scanner 标记不可执行但 decision 当作可开”的隐性冲突。

### P1-2：轮动 scan 缺少深度信息

问题：

统一 shadow market fetch 主要取 `FetchBookTicker` 中间价，没有完整订单簿深度。

影响：

- EV、slippage、tradable notional 可能过于乐观。
- 小币 carry 机会尤其容易被盘口摩擦吞掉。

整改方向：

- 对进入 top candidates 的机会二次拉深度。
- scanner 粗筛可用 book ticker，决策前必须做 depth verification。
- depth 不足时降低 notional 或直接 reject。

### P1-3：影子/实盘一致性需要专门验收

问题：

不同 subsystem 的 shadow/live gate 不一致。
某些 shadow close 逻辑会直接修改 ledger，而不是只记录意图。

影响：

- 如果真实仓位和 ledger 混在一起，shadow 行为可能污染状态。
- shadow 结果可能不能代表 live 行为。

整改方向：

- 明确 shadow 模式禁止修改真实 position store，或把 shadow ledger 单独隔离。
- 所有 executor 输出统一的 dry-run event，而不是复用 live ledger close 流程。
- 增加 shadow/live parity test。

### P1-4：恢复和 flatten 覆盖范围需要重新审计

问题：

legacy emergency flatten 与当前多策略 position stores 的关系不够清晰。

影响：

- 事故中调用错误工具，可能只处理旧策略仓位，漏掉当前 live 仓位。

整改方向：

- 列出所有 position store。
- 为每类 position store 明确：
  - startup reconcile
  - manual close
  - verified close
  - flatten-all
  - emergency CLI coverage
- 若某路径不覆盖，UI/CLI 必须直接提示。

### P1-5：perp-perp 查单未知与回滚语义需要对齐

问题：

四个 carry executor 有“查单未知禁止自动回滚”的防御思路，但 `perp-perp` 当前是主力 live 路径，必须复核 `confirmCrossVenueFill` / 第二腿失败 / 回滚第一腿的完整语义。

风险：

- 如果查单失败没有进入明确 unknown 状态，而第二腿失败后直接回滚第一腿，可能在交易所状态不明时制造误回滚或裸腿扩大。
- 这不是 shadow 问题，是当前 live path 的执行安全问题。

整改方向：

- 为 `perp-perp` 引入与 carry executor 一致的 unknown-order / unknown-fill 状态。
- unknown 时禁止自动做会扩大风险的动作，只允许进入人工/verified reconcile 流程。
- 增加第二腿失败、第一腿查单 unknown、回滚查单 unknown 的测试矩阵。

### P1-6：SVSP 腿序与薄盘裸多窗口需要复核

问题：

SVSP 先买现货再空永续。在薄盘小币上，这会产生裸多方向性窗口。

风险：

- 高费率小币往往也是盘口更薄、冲击更高的标的。
- 一笔 IOC + fill confirm 的窗口足以让 dust、滑点、rollback cost 吃掉微薄 carry。

整改方向：

- 复核 SVSP 腿序是否应该按 venue / depth / spread 动态选择。
- 对低深度小币，要求更严格 depth gate 或直接禁用 SVSP live。
- 对每笔 SVSP open_retry 记录真实 rollback / opportunity cost，用于校准成本模型。

## 4. 整改优先级

### 第零阶段：先处理当前真钱敞口

目标：在地基修好前，不让 rotation 继续用真钱产生不可解释亏损。

任务：

1. 决定是否立即将 `rotation_control.active` 关回 `false`。
2. 确认 `perp-perp` 当前硬 cap 生效：
   - `max_position_usdt`
   - `max_concurrent`
   - openings control
   - halt
3. 写下当前最大可容忍亏损，到线 halt。
4. 暂停任何扩大 live 资金、扩大并发、增加策略的动作。

### 第一阶段：把系统说清楚，不继续误判 live 状态

目标：消除虚假统一和操作歧义。

任务：

1. 将 dashboard / 文档 / 状态命名改成真实形态：
   - `perp-perp live executor`
   - `four-strategy carry rotation`
2. 在 UI/status 中明确显示：
   - `perp-perp live/shadow`
   - `rotation live/shadow`
   - `openings enabled`
   - `global halt`
   - `manage orders enabled`
3. 对 `perp_perp` 在 unified candidates 中的角色做选择：
   - 不执行就不要展示为可轮动策略；
   - 或标记为 `candidate_only`。

### 第二阶段：统一控制面和事故语义

目标：任何暂停、急停、flatten、reconcile 的含义唯一。

任务：

1. 抽象统一 control state。
2. 所有 executor 接入相同控制语义。
3. dashboard 和 CLI 走同一套 flatten/reconcile service。
4. 补齐 paused/live/shadow/manage/verified-close 的测试矩阵。

### 第三阶段：补资本层

目标：小资金不会因为多所分散而虚假提高机会数。

任务：

1. 建立 capital snapshot。
2. 建立 strategy capital budget。
3. 建立 unfundable reason dashboard。
4. 将 `perp-perp` 和 rotation 的 capital usage 放进同一张账。

### 第四阶段：补研究归因

目标：证明系统赚钱来自哪里，而不是只证明系统能运行。

任务：

1. 每笔机会落一条 prediction record。
2. 每笔完成交易落一条 realized PnL attribution。
3. 建立 prediction vs realized 对账报表。
4. 预先登记 go/no-go 判据。
5. 每周输出策略级和组合级 go/no-go。

预注册判据示例：

- 只统计真实成本闸打开后的样本。
- relaxed gate / collect-data 模式样本只能用于成本模型校准，不能用于证明 net edge。
- 每个策略至少满足：
  - 样本数不少于 `N` 笔；
  - 覆盖不少于 `M` 周；
  - 占用资金加权净年化大于预设阈值 `X%`；
  - 最大回撤小于预设阈值 `Y%`；
  - unexplained PnL 低于预设容忍度；
  - attribution complete ratio 高于预设阈值。
- 数字必须在重新采样前写死，不能事后按结果调门槛。

### 第五阶段：再考虑真正五策略统一

目标：在证据足够后，再决定是否把 `perp-perp` 接入统一 runner。

前置条件：

- 四策略 rotation 必须先从 live 退回可控状态，或证明当前 live 已被账本、风控和成本闸覆盖。
- 真实或纸面 attribution 能说明统一轮动优于单策略独立运行。
- control plane 和 capital budget 已统一。

## 5. 给 Composer 2.5 的执行拆分建议

### Composer 任务 1：状态命名与 dashboard 显示纠偏

目标：

- 不改交易行为。
- 只把系统真实状态暴露清楚。

具体做：

- 找 dashboard status API 和前端展示。
- 分别展示：
  - `cross_venue_executor.live_trading_enabled`
  - `cross_venue openings enabled`
  - `unified_rotation.active`
  - `unified_rotation.live_trading_enabled`
  - `perp_perp_candidate_only_in_rotation`
- 如果 `perp_perp` 没有 rotation binding，在 UI 中明确显示为“独立执行，不由统一轮动开仓”。

验收：

- 页面不能再暗示五策略都由 unified rotation 实盘执行。
- 不改变任何下单路径。

### Composer 任务 2：统一候选中的 `perp_perp` 语义修正

目标：

- 避免 `perp_perp` 进入统一 rotation 后被误认为可执行。

可选方案：

- 方案 A：从 unified rotation candidates 中移除 `perp_perp`。
- 方案 B：保留展示，但加 `ExecutionMode: candidate_only` / `CanExecute: false`。

推荐：

- 先做方案 B，保留对比视野，但禁止误执行和误展示。

验收：

- `openOpportunity` 不再用“无执行器绑定”作为正常控制流。
- 决策层在生成 plan 前就知道该机会不可由 rotation 执行。

### Composer 任务 3：rotation decision 消费 reject semantics

目标：

- scanner 认为不可执行的机会，decision 不能当成普通可开机会。

具体做：

- 梳理 `UnifiedOpportunity` 已有字段。
- 在 `rotationCanOpenOpportunity` 中加入规则：
  - hard reject reasons 阻断 open。
  - `RequiresRebalance` 且 capital router 无法解决时阻断 open。
  - execution/liquidity score 低于阈值时阻断 open。
- 给每个阻断原因写测试。

验收：

- 有 reject reason 的候选不会仅因正 EV 被打开。
- 日志能说明阻断原因。

### Composer 任务 4：capital snapshot 与 unfundable dashboard

目标：

- 先不自动跨所转账，但把资金碎片化暴露出来。

具体做：

- 汇总每个 venue / wallet：
  - free USDT
  - reserved USDT
  - occupied by strategy
  - required by pending opportunities
- 输出 unfundable opportunities：
  - opportunity id
  - strategy
  - missing venue/wallet
  - missing amount
  - whether cross-venue transfer would solve it

验收：

- 操作者一眼能看出机会是没 edge，还是钱不在位置。

### Composer 任务 5：shadow/live 状态隔离审计与修正

目标：

- shadow 行为不能污染真实 position store。

具体做：

- 搜索所有 `if !live` 分支。
- 检查是否写真实 ledger / position store。
- 将 shadow-only close/open 记录迁移到 shadow event 或 shadow ledger。
- 对真实 ledger 的写入必须要求 live 或 verified exchange evidence。

验收：

- shadow 模式不会把真实 position 标为 closed/opened。
- 增加测试覆盖 paused rotation + real position + manage 的行为。

### Composer 任务 6：flatten/reconcile 覆盖矩阵

目标：

- 事故工具覆盖范围明确，不允许误用。

具体做：

- 列出所有 position store。
- 列出所有 flatten/reconcile entrypoint。
- 为每类策略补 coverage test 或显式 unsupported warning。
- 如果 emergency CLI 不覆盖当前多策略仓位，命令执行前必须打印强警告并拒绝“看似全局”的命名。

验收：

- 任一策略 live 后，都能回答：如何启动恢复、如何 verified close、如何 flatten。

### Composer 任务 7：prediction vs realized PnL attribution

目标：

- 建立研究闭环。

具体做：

- 在 scanner/decision 时记录 prediction：
  - expected funding
  - expected basis PnL
  - expected fees
  - expected slippage
  - expected borrow
  - expected net EV
- 在 close 后记录 realized：
  - realized funding
  - realized basis PnL
  - realized fees
  - realized slippage
  - realized borrow
  - final net PnL
- 建立按 strategy/symbol/venue 的聚合报表。

验收：

- 每笔交易能解释预测错在哪里。
- 每周能输出策略级 go/no-go。

## 6. 不建议马上做的事

### 不建议马上把五策略强行统一实盘

原因：

- 四策略 rotation 已经 live，但尚未证明有效，且已有 open_retry / rollback / dust 类真实成本暴露。
- `perp-perp` 当前独立链路更成熟。
- 强行合并会提高事故面。

### 不建议马上自动跨所转账

原因：

- 自动跨所资金流是新的高风险系统。
- 小资金阶段先做可视化和人工确认更合理。

### 不建议继续堆新策略

原因：

- 当前瓶颈不是机会种类不够，而是统一控制、资本效率和研究归因不足。

## 7. 最终判断

Lingxi 当前最值得保护的是已经跑起来的 `perp-perp` 小额实盘执行链。

注意：「最值得保护」指它是当前**最经过实战检验**的链路，**不等于它已被证明盈利**——它的 realized_funding 同样≈0，且带着 P1-5 的查单未知回滚安全缺口。因此「保留 perp-perp 小额 live」应以 P1-5 修复 + 账户级风控覆盖为条件，而不是凭「更成熟」豁免。

最需要收敛的是“五策略统一”的叙事和控制面。

最需要补强的是研究归因和资本账。

如果下一阶段继续堆策略，项目会变成复杂但不可证伪的交易工程。
如果下一阶段先收敛控制面、资本面和 attribution，它才有机会从“个人量化工程系统”进化成真正的“个人交易操作系统”。

## 8. Claude4.8 审判合并补充

本节合并另一轮审判中合理、且与本文件主判断一致的内容。

处理原则：

- 与当前代码结构判断一致的，直接采纳。
- 具体 live 状态、SVSP settlement、`rotation_control.active=true`、CVSP churn 代码路径已升级为已证实或基本证实。
- 历史个案 `NBIS` / `RIVN` 是否与 HEI 同类，仍列为待复核。
- 能转成整改任务的，直接给 Composer 2.5 拆执行。

## 8.1 执行优先级重排

原文把太多问题都标成 P0，容易让执行者平铺开工。实际不可并行、必须先做的只有三件地基级工作：

1. 账本可信：PnL reconciliation / attribution complete。
2. 状态可信：CVSP churn / 已平仓复活 bug 修复。
3. 生存可信：当前 live path 的账户级风控覆盖确认。

紧急动作在三件事之前：

- 先决定是否把 `rotation_control.active` 关回 `false`。

其余问题虽然重要，但应降为 P1/P2：

- 成本闸配置审计。
- `perp_perp` candidate-only 语义。
- 资本 snapshot。
- go/no-go 预注册。
- UI 命名纠偏。

### P0-A：先修 PnL 可信度，否则研究迭代没有地基

Claude4.8 的核心判断是：当前系统最大的问题之一不是亏了多少，而是“系统无法可信回答自己赚了还是亏了”。

这个判断合理，应提升为 P0。

原因：

- 本文件前文已经指出缺少 prediction vs realized PnL attribution。
- 如果 settlement、ledger、equity snapshot 三者不能闭环，任何 go/no-go 都会被污染。
- 即使策略只是在小额“赔钱采数据”，也必须知道每一笔钱到底赔在哪里。

已证实或已足够采纳的事实：

- 真实权益约 `-3.7U`。
- settlements 与真实权益相差约 `13U`。
- `ledgerrecon` trusted 仅约 `21%`，unreconciled 约 `78.5%`。

仍待复核的事实：

- `NBIS`、`RIVN` 是否存在类似 HEI 的归因幻觉。

整改方向：

- 建立 PnL 三账对齐：
  - exchange equity snapshot
  - internal settlements
  - strategy attribution ledger
- 每个 closed position 必须能解释：
  - funding
  - basis PnL
  - fee
  - slippage
  - borrow
  - transfer/rebalance cost
  - residual/dust
- 口径说明（避免追一个对不平的目标）：`equity_snapshots` 是**账户总额**，会被资金费流入、所间划转、其它在场仓同时搅动，**无法逐仓对到真实现金**。因此对账分两层：真相锚用「日级 / 账户级 equity delta」，内部归因用「逐仓 attribution ledger」，二者**只在聚合层要求对平**；逐仓只对内部账本自洽负责。
- 所有 attribution incomplete 的记录必须显式进入“不可信 PnL”桶，不能参与策略 go/no-go。
- 修复点之前（例如 2026-06-17 / `fab6afd` 前后需要由 Composer 核准边界）的历史 settlements 缺字段，不能假装可以完整考古复原；旧样本应整体标为“不可信历史样本”，退出 go/no-go。
- reconciliation 的重点应转为 forward instrumentation：从修复点之后，确保每笔记录都带齐 entry/exit/closed_qty/funding_truth/attribution_complete。

Composer 任务：

1. 找出现有 `ledgerrecon`、`equity_snapshots`、`settlements`、strategy position stores 的数据源。
2. 输出一份 reconciliation report：
   - total exchange equity delta
   - total internal settlement PnL
   - explained PnL
   - unexplained PnL
   - trusted ratio
3. 将修复前 settlements 整桶标为“不可信历史样本”，默认不进入 go/no-go。
4. 对 HEI 做已知异常链路说明；对 NBIS / RIVN 只做是否同类的特征复核，不要求完整考古还原。
5. 在 dashboard 或报告里明确显示 “PnL trusted / incomplete / unreconciled”。

验收：

- 任意时间点能回答：账户真实权益变化和系统账本差多少。
- 差额不能被静默吞掉。

### P1-A：真实成本闸必须恢复，否则采到的数据会系统性偏乐观

Claude4.8 指出当前 config 存在主动放松 EV/cost gate 的情况，例如：

- `fee_reserve_rate` 极低。
- `exit_spread_reserve_rate` 为零。
- `auto_derive_fee_reserve_enabled` 关闭。
- `impact_reserve_in_gate_enabled` 关闭。
- 某些执行器允许负 `min_net_profit_rate`。

这个方向合理：如果真实成本闸被关掉，系统采集到的是“能成交的数据”，不是“能赚钱的数据”。

需要复核的具体数字：

- “闸门比真实四腿成本低约 22 倍”。
- “`max_scan_basis_rate` 放宽约 67 倍”。
- “真实四腿往返成本约 `0.0024`”。

即使倍数需要复核，整改方向也成立。

整改方向：

- 将 live/canary config 明确分成两类：
  - `research_collecting_mode`
  - `profit_seeking_mode`
- 数据采集模式允许亏损，但必须在每笔记录中标记。
- 盈利判断只能基于真实成本闸打开后的样本。
- 不允许把“学费配置”的结果拿去证明 alpha。
- relaxed gate 采到的数据不是废物，但唯一合法用途是校准成本模型，例如真实滑点、两腿价差、rollback cost、open_retry 损耗；不能拿它证明 net edge。

Composer 任务：

1. 扫描 config 中所有 cost / fee / impact / reserve / min profit 相关参数。
2. 输出当前配置相对默认配置的差异表。
3. 标记哪些参数是“放松盈利闸门”。
4. 增加运行模式字段：
   - `collect_data`
   - `canary_profit`
   - `profit_seeking`
5. 不同模式下 dashboard 必须显示当前是否为“主动接受亏损采数据”。

验收：

- 系统不能在宽松采集配置下显示为“盈利策略验证中”。
- 每笔交易都能知道自己是在采数据还是在追求正 EV。

### P0-C：账户级风控可能未覆盖当前实盘路径

Claude4.8 指出一个非常重要的结构性风险：许多账户级风控可能仍挂在已退役的 `FundingRateSniperEngine` 上，而当前实盘路径主要是 `CrossVenueExecutor` 与 `UnifiedRotationRunner`。

这个判断与本文件前文“控制面分裂”一致，应提升为 P0。

需要复核的具体对象：

- `startRealtimeDrawdownMonitor`
- `MaxDailyLossUSDT`
- `MaxCycleLossUSDT`
- `MaxAccountDrawdownUSDT`
- consecutive loss fuse
- `live_daily_risk_state.json`
- single-writer lease
- watchdog / dead-man flatten coverage

整改方向：

- 不能只确认“代码里有风控”，必须确认“当前 live path 调用了风控”。
- 为每条实盘路径建立 risk coverage matrix：
  - `CrossVenueExecutor`
  - `UnifiedRotationRunner`
  - 四个 carry executor
  - legacy engine
- 每个风险机制必须标注：
  - wired
  - not wired
  - legacy only
  - dashboard only
  - CLI only

Composer 任务：

1. grep 所有账户级风险函数的 call sites。
2. 画出当前 live startup path 到 risk monitor 的调用链。
3. 如果 `CrossVenueExecutor` 没有账户级 drawdown monitor，先补只读报警，再补硬熔断。
4. 如果 single-writer lease 没覆盖当前 live path，优先接入。
5. 如果 dead-man flatten 不读 `records/crossvenue/positions.json`，必须标红并拒绝自称全局 flatten。

验收：

- 开启当前生产配置后，能证明账户级风控实际运行。
- kill/restart/dead-man 场景能覆盖 `perp-perp` 真仓。

### P0-B：position churn / 已平仓复活 bug 已基本定位，需修复并回归

Claude4.8 指出一个具体代码级疑似 bug：

- `handleOrphanLeg` 按值接收 `pos`。
- `finalizeClosedPosition` 用副本删除/关闭台账。
- `manageOpenPositions` 随后可能把原始未改写的 `open` 状态重新 persist。
- 结果是已平仓 position 被“复活”，每 60s 重复刷 `dust_orphan` settlement。

该问题已从“待复核”升级为“基本定位，待生产回归验证”：

- 与“账本不可信”问题强相关。
- 与“CVSP dust_orphan 高频出现”强相关。
- `cross_venue_spot_perp_executor.go` 中 `manageOpenPositions` 在 `managePosition` 后无条件 `persistPosition(updated)`，而 orphan dust 分支通过按值 `pos` 调 `finalizeClosedPosition`，存在已关闭状态被旧 open 副本重新写回的风险。
- 这会污染 settlements 和 attribution。

整改方向：

- 立即作为 P0 bug 修复，而不是普通清理项。
- 先修状态机，再重跑修复后的 forward reconciliation。

Composer 任务：

1. 定位所有 `handleOrphanLeg` / `finalizeClosedPosition` / `persistPosition` 调用链。
2. 检查是否存在按值修改后又写回旧对象的路径。
3. 搜索 settlement 中相同 `position_key` 是否周期性重复出现。
4. 写一个回归测试：
   - 构造 orphan leg。
   - manage 一轮后 position store 中不得重新出现 open 状态。
   - settlement 不得重复落多笔 dust close。

验收：

- 同一已关闭 position 不会被下一轮 manage 复活。
- dust_orphan 不会周期性重复刷账。

## 8.2 对六视角评分的修正

结合 Claude4.8 的补充，如果上述 PnL、风控、churn、成本闸问题复核成立，评分应下调。

修正后的保守评分：

- 工程架构：`6.5/10`
  - 纯函数决策、路由、测试仍是优点。
  - 但 `internal/app` 过重、控制面迁移半途、疑似 churn bug 拉低架构可信度。

- 量化研究：`3/10`
  - 没有已验证 alpha。
  - 如果当前配置主动放松真实成本闸，则采集样本不能证明盈利能力。

- 量化实现：`5.5/10`
  - 执行/恢复防御强。
  - 但影子不能校准盈利，fillmodel 未充分接入 live executor，账本归因不可信。

- 风险控制：`4/10`
  - 风控模块多，不等于 live path 被覆盖。
  - 若账户级风控确实仍挂在 legacy engine，则这是结构性不及格。

- 实盘可操作：`5/10`
  - 面板、halt、verified close 合格。
  - 但 dead-man 覆盖、薄盘成交、position churn、PnL 可信度会显著削弱实盘可操作性。

- 资本配置价值：`2.5/10`
  - 无验证 edge。
  - carry 收益可能被成本吞掉。
  - double-sizing 与资金碎片化风险仍在。

- 综合：`4.5/10`
  - 工程让项目有未来。
  - 研究、风控、账本可信度让项目当前不能被视为“已成立的个人量化项目”。

注意：这个评分比前文更严，是在 Claude4.8 的补充事实成立时的评分。

## 8.2.1 评分天花板与可达性（关于「想冲 9+」）

文档本身改不动项目分——评分打的是系统属性，不是整改计划。本文档只是「承诺修」，分数要动必须「修完 + 证明能赚」。按维度看可达性：

- 工程架构：拆包 + 控制面统一 + churn 修复后，**可达 8–9**。
- 量化实现：churn 修复 + 影子定价隔离/真实化 + 账本可信 + fillmodel 接入后，**可达 8**。
- 风险控制：账户级风控接到 live path + dead-man 覆盖跨所仓 + 单写租约 + 真数字止损后，**可达 8**。
- 实盘可操作：控制面唯一化 + 成交质量改善 + 账本可信后，**可达 7–8**。
- 量化研究：要到 9，**必须拿出真实成本闸下、足够样本、净年化为正且可解释的 edge**。这是目前唯一零证据、也最可能失败的维度；没有它，综合上不了 8+。
- 资本配置价值：受「个人 / 小资金 / 单一 carry 家族」结构性限制（容量小、跨所不补资、carry 薄），**即使全部修好可能也只到 5–6**，这是赛道物理上限，不是努力问题。

结论：工程 / 实现 / 风控 / 实盘四维有机会进 8–9；但**综合 9+ 的唯一钥匙是量化研究维度跑出真实 edge**——那不在文档里，在那条还没跑出来的、真实成本下为正的收益曲线里。

## 8.3 项目定位修正

如果 PnL 不可信、账户级风控未覆盖当前 live path、成本闸被主动放松这三点全部成立，则项目定位应从：

“已初步成立的个人量化项目”

下调为：

“工程完成度很高的数据采集型研究平台，具备交易操作系统骨架，但尚未成立为可信个人量化项目。”

更精确地说：

- 不是玩具项目。
- 是优秀工程作品。
- 是研究平台，但当前研究结果尚未转正。
- 有交易 OS 骨架，但统一性、风控覆盖和账本可信度不足。
- 还不是已证明可持续盈利的成熟项目。
- 是否算“已初步成立的个人量化项目”，取决于 PnL reconciliation 和 live risk coverage 的复核结果。

## 8.4 最该先做的一件事

如果只能做一件事，不是继续统一五策略，也不是继续加策略，而是先决定当前 live 敞口姿态：

在 churn bug 修复、账本可信、live path 风控覆盖确认前，建议把 `rotation_control.active` 关回 `false`。

顺序：

1. 决定并执行 rotation active 是否关回 false。
2. 修 position churn / attribution incomplete。
3. 修 PnL reconciliation，但重点是 forward instrumentation，不是考古旧账。
4. 确认账户级风控覆盖当前 live path。
5. 打开真实成本闸，区分采数据配置和盈利配置。
6. 预注册 alpha go/no-go 判据。
7. 再讨论五策略统一。

## 8.5 给 Composer 2.5 的新增任务包

### Composer 任务 8：PnL 可信度专项

目标：

- 让系统能可信回答真实权益变化、账本 PnL、settlement PnL 三者差异。
- 不要求完整考古修复缺字段历史账；历史旧样本先整体降权。

执行：

- 汇总 `records` 下所有 equity snapshot、settlement、position close、funding records。
- 做 symbol / strategy / venue / position_key 级别 reconciliation。
- 输出 unexplained delta。
- 将修复点之前的缺字段样本标为 `untrusted_legacy_sample`，退出 go/no-go；具体边界以归因修复 commit / 数据字段首次齐全时间为准。
- 对 `HEI` 做已知异常说明；对 `NBIS`、`RIVN` 只判断是否疑似同类，不做无谓完整还原。
- 给修复后的新样本补齐 forward instrumentation。

验收：

- 输出 `pnl_reconciliation_report.md` 或 dashboard 等价视图。
- trusted / incomplete / unreconciled 三类金额分开显示。
- go/no-go 默认只统计修复后 attribution complete 的样本。

### Composer 任务 9：Live path 风控覆盖矩阵

目标：

- 确认当前实盘路径到底被哪些风险机制保护。

执行：

- 从 `cmd/lingxi/main.go` 到 `app.Run` 到 `startLiveStack` 追调用链。
- 列出每个 risk function 的实际 call site。
- 标注是否覆盖：
  - `CrossVenueExecutor`
  - `UnifiedRotationRunner`
  - 四个 carry executor
  - legacy `FundingRateSniperEngine`
- 输出矩阵。

验收：

- 不能只说“有风控代码”，必须说明“当前 live 配置会不会执行它”。

### Composer 任务 10：成本闸配置审计

目标：

- 明确当前配置是不是“主动亏损采数据”。
- 明确 relaxed gate 数据只能用于成本模型，不能用于证明 alpha。

执行：

- 比较 `config.yaml` 与 defaults 中所有 fee / reserve / impact / min profit 参数。
- 计算当前 gate 与真实 taker fee / expected slippage 的差距。
- 输出每条策略当前是：
  - profitable gate
  - relaxed data collection gate
  - unsafe gate

验收：

- dashboard 或报告里显示当前模式。
- 采数据模式下的交易不得进入 alpha 证明样本。
- 采数据模式下的交易可以进入成本校准样本。

### Composer 任务 11：Position churn bug 复核与修复

目标：

- 修复已基本定位的已平仓复活 / settlement churn bug。

执行：

- 重点查 `cross_venue_spot_perp_executor.go` 的 orphan handling。
- 检查所有 executor 是否有类似按值修改后又写回旧状态的问题。
- 增加回归测试。

验收：

- 同一 `position_key` 不会在无新交易情况下重复生成 close settlement。

### Composer 任务 12：当前 rotation live 敞口处置

目标：

- 在修地基前，明确四策略 rotation 是否继续用真钱跑。

执行：

- 读取 `records/rotation_control.json`。
- 在 dashboard / CLI / 文档中明确当前 `active` 覆盖 config 默认值。
- 给出并执行决策：
  - `active=false`：暂停新开仓和真实 manage 下单，只保留 verified close / reconcile 能力；
  - 或维持 `active=true`：必须写明当前最大可容忍亏损和 halt 条件。

验收：

- 操作者不能再被 `config.yaml live_trading_enabled=false` 误导。
- 当前 rotation live/shadow 状态在 UI 和日志中一眼可见。

### Composer 任务 13：预注册 alpha go/no-go 判据

目标：

- 防止数据出来后事后解释。

执行：

- 为每个策略写死 go/no-go 门槛（以下为**建议初值，需你在采样前确认并锁死**，事后不得按结果调整）：
  - 最小样本数 `N`：建议 ≥ 200 笔 attribution-complete 平仓
  - 最短周期 `M`：建议 ≥ 4–8 周（需覆盖多个资金费方向 / 行情状态）
  - 占用资金加权净年化阈值：建议 GO 线 > +10~15%（真实成本闸打开后；对照现有 NO-GO 样本为 -58%、break-even 为 0，GO 必须留正向余量）
  - 最大回撤阈值：建议 < 该策略分配资金的 8–10%
  - attribution complete ratio：建议 > 95%
  - unexplained PnL 容忍度：建议 < 该策略毛 PnL 的 5%
- 明确 relaxed gate 数据只用于成本校准，不进 alpha 证明样本。

验收：

- 没有预注册判据，不允许宣布某策略 GO。

### Composer 任务 14：perp-perp unknown fill / rollback 安全审计

目标：

- 让当前主力 live 路径的查单未知语义不弱于 carry executors。

执行：

- 审计 `cross_venue_open.go` 中 confirm fill、第二腿失败、rollback 第一腿的状态机。
- 增加 unknown-fill 状态和测试。
- unknown 时进入人工/verified reconcile，而不是自动扩大动作。

验收：

- 查单未知时不会静默按“失败”或“成功”继续自动回滚。

### Composer 任务 15：SVSP 腿序与薄盘执行复核

目标：

- 降低薄盘小币裸多现货窗口带来的真实摩擦。

执行：

- 统计 SVSP open_retry、rollback、dust、成交耗时。
- 复核先现货后永续的腿序是否适合当前标的。
- 增加 depth / spread / notional gate。

验收：

- SVSP 不再在明显薄盘、低深度、真实成本不可覆盖的标的上反复烧手续费。

### Composer 任务 16：Dead-man / flatten 覆盖当前仓位

目标：

- 事故时所有当前 live 仓位都能被识别和收敛。

执行：

- 列出 watchdog / flatten / emergency CLI 读取的 store。
- 确认是否包含：
  - `records/crossvenue/positions.json`
  - 四类 rotation position stores
  - legacy `live_open_hedges.json`
- 不覆盖的路径必须改名、报警或拒绝执行。

验收：

- “全局 flatten” 必须真的覆盖所有当前 live 策略。

### Composer 任务 17：路线 B —— 把 perp-perp 真正接入统一 runner（实现五策略统一的终点）

目标：

- 让五策略真正同步、同台择优、跨周期统一换/平，实现策略意图（前置：地基三件 + 路线 A 止血已完成）。

执行：

- 先做只读可行性核对：`CrossVenueExecutor` 的 open/close/manage/reconcile 接口签名是否能套进 `rotationExecutorBinding`；列出不一致的适配点。
- 为 perp-perp 增加 `rotationExecutorBinding`：`openFn / closeVerifiedFn / heldFn / realHeldCountFn / reconcileFn / manageFn` 接到 `CrossVenueExecutor`，对齐现有四个 `bind*RotationExecutor`（`unified_rotation_runner.go:832-926`）。
- 把 perp-perp 真仓喂进 `collectHeldPositions`（`unified_rotation_runner.go:709-732`），使其进入统一 `held[]` 参与续持/换仓比较。
- 统一资金额度：perp-perp 与四策略共用同一份 capital budget（消除 double-sizing），并纳入同一 risk gate / 控制面。
- 接入后撤掉 `candidate_only` 标记，补「perp-perp 被 SWITCH 选中后能真正开仓」「perp-perp 持仓能被统一 CLOSE/SWITCH」的测试。

前置条件：

- 第零阶段敞口处置完成、账本可信、churn 修复、账户级风控覆盖确认、真实成本闸打开；否则会把「决策污染 + 双 sizing」放大到真钱上。

验收：

- 决策选中 perp-perp 的 OPEN/SWITCH 能真实成交，不再出现「OPEN 跳过(无执行器绑定)」。
- perp-perp 持仓出现在统一 `held[]`，可被跨周期换/平。
- 两条 live 循环合并为一份资金账与一套风控，不再 double-sizing。

## 8.6 修正后的一句狠话

你造出了一台工程上很认真的交易机器，但现在最该修的不是更多策略，而是仪表盘、账本和刹车；否则它就只是用很复杂的方式，去采集一堆你还无法证明有价值、也无法完全核算盈亏的数据。

---

## 9. 执行记录（2026-06-19 落地，大脑 Claude + Composer 2.5 执行）

本节是上述整改思路的**实际落地记录**。所有代码改动均：源码级、不影响当时正在运行的实盘二进制（需重新 build + restart 才生效）、每项带回归测试、最终 `go vet` + `go build ./...` + `go test ./internal/app/` 全绿。

### 9.0 运行时已执行的操作（影响在跑进程）

- **统一轮动 rotation 已 STOP**：经 dashboard 控制 API `POST /api/rotation/stop`（携带 `records/.dashboard_control_token`）调用 `SetActive(false)`，运行进程内存 `active=false`，`rotation_control.json` 同步为 false。仅停"开新仓"，在场 2 笔仓继续 manage，可随时 `POST /api/rotation/start` 恢复。
  - 注意：直接编辑 `rotation_control.json` 文件**无效**（进程只在 InitState 启动时读、运行期只认内存 atomic），必须走控制 API。
- **perp-perp（独立执行器）未停**：走独立 openings/live 开关，rotation STOP 不影响它；如需停需单独 openings_off / strategy stop。

### 9.1 已落实改动

**四份只读审计报告**（`records/audit/`）：`pnl_reconciliation_report.md`、`risk_coverage_matrix.md`、`cost_gate_audit.md`、`route_b_feasibility.md`。

**九项代码改动**（含回归测试）：

| # | 改动 | 性质 | 关键效果 |
|---|------|------|---------|
| 1 | churn 复活 bug（四执行器 `shouldPersist` 语义） | bug 修复 | 已平仓不再被 manage 复活、不再周期重复刷 dust 结算 |
| 2 | `UnifiedOpportunity.CandidateOnly`（perp-perp 标记） | 路线 A 止血 | 消除幽灵换仓 + 挤占名额；路线 B 接入后自动失效 |
| 3 | 决策消费 reject semantics（`rotationCanOpenOpportunity`） | 一致性 | 硬 reject / 不可备资候选不再被 OPEN/SWITCH |
| 4 | perp-perp unknown-fill 回滚安全（`cross_venue_open.go`） | 执行安全 | 查单未知禁回滚，与四 carry 对齐 |
| 5 | 账户级风险 watcher Stage A（`account_risk_watch.go`） | 风控可见 | 只读聚合 + 超阈告警，零交易副作用 |
| 6 | capital snapshot 只读端点（`/api/rotation/capital-snapshot`） | 可观测 | 区分"没 edge"vs"钱不在位置" |
| 7 | 统一控制面（`control_plane.go`，global_halt/allow_*） | 架构 | 控制语义唯一；默认零行为变化；`.halt-live`≡global_halt；STOP 设 global_halt |
| 8 | prediction-vs-realized 归因 + 预注册 go/no-go（`attribution_audit.go`） | 研究闭环 | 阈值写死为命名常量；relaxed 样本不进 alpha 判定 |
| 9 | **路线 B**：perp-perp 接入统一 runner（`cross_venue_rotation_adapter.go` 等） | 架构终点 | 五策略真正同台择优；双循环消歧、无 double-sizing；`CrossVenuePosition.OpportunityID` 持久化 |

### 9.2 未尽 TODO（非阻塞，后续推进）

> **2026-06-19 勘误（以下项已在 §10.1 第二批落地，勿再当「未做」）**：
> - ✅ **账户级风控 Stage B 硬熔断**（一级硬停开仓 + opt-in auto-flatten，`account_risk_watch.go`）
> - ✅ **CLI flatten 与 watchdog 五类 position store 覆盖**（`emergency_flatten.go` / `watchdog.sh`）
> - ✅ **成本闸 `profit_seeking_mode`** — 已写 `config.yaml` `strategy_mode: profit_seeking`（见 §10.2 勘误）

- 全量资金 allocator 合一（`RunUnifiedShadowAllocator` 接线）——当前已保证无双开/双 manage，但跨策略全局额度仲裁仍是下一步。
- dashboard 增 perp-perp 持仓面板视图（`buildRotationPositionViews`）。**2026-06-19 勘误：§10.1 已落地只读 UI。**
- 运行模式标记（collect_data / canary_profit / profit_seeking）当前为包级常量 `currentAttributionRunMode`，待接 config。
- ~~成本闸 `profit_seeking_mode` 配置切换~~ — **已改 config**（`strategy_mode: profit_seeking`），见 §10.2。
- ~~CLI flatten 与 watchdog 覆盖五类 position store~~ — **§10.1 已落地**。
- 止损与 go/no-go 的具体数字仍为建议值，需你在采样前确认锁死。

### 9.3 部署 checklist（重新 build + restart 前逐项确认）

1. **先确认敞口姿态**：rotation 已 STOP（见 9.0）；决定 perp-perp 是否也停。
2. **review 路线 B**：这是改 live 行为的最大改动（perp-perp 从独立循环→runner 驱动，`run.go` 装配变更）。重点核对 `run.go` 不再 `go exec.Run`、`SetRotationManaged(true)`、无 double open/manage。
3. **build**：`go vet ./internal/app/ && go build ./... && go test ./internal/app/`（已验证全绿）。
4. **先 OFF / CANARY**：用 **OFF 模式**（control_plane，跑决策+日志、不下真单）或四所各 ~10U **CANARY_LIVE** 跑一轮，看统一日志里 perp-perp 是否走 runner、有无双开、归因/控制面是否正确，再放量。（`lingxi shadow` 已删除。）
5. **控制面自检**：启动后核对 `/api/strategy/status` 的 `control_plane` 字段（perp_perp_mode / rotation_mode / effective_global_halt）与预期一致，消除 config vs runtime 误导。
6. **账本回归**：跑 `go run ./cmd/ledgerrecon /root/lingxi/records`；新样本应带齐 entry/exit/closed_qty/funding_truth/attribution_complete，确认 churn 不再刷重复结算。
7. **资金合一未完成的前提下**：放量前留意 perp-perp 与四 carry 对同一钱包的占用（capital snapshot 端点观察），避免 over-commit。
8. **回滚预案**：保留旧二进制（`lingxi.bak.*`）；异常时可回滚 + dashboard STOP。

---

## 10. 执行记录（2026-06-19 第二批：GPT P0/P1/P2 计划落地 + 删 shadow）

大脑 Claude + Composer 2.5 执行。**全部源码级、不影响当时运行中的二进制**；最终 `go build ./...` + `go vet ./...` + `go test ./...`（全部包）**全绿**。改动均带回归测试、串行落地（同一 `internal/app` 包不并行编辑以避免互相破坏编译）。

### 10.0 运行时已执行（影响在跑进程）

- **rotation 已 STOP**（`/api/rotation/stop`，active=false）。
- **一键 flatten 已执行**（`/api/strategy/flatten-all` confirm=FLATTEN）：收敛在场仓。
- **FLUX-USDT@binance 裸空**：原 ~$5.2 借币空现货、无永续对冲；flatten 已买回大部分，剩 ~0.10459 FLUX（≈$0.005）低于 Binance minNotional（-1013 NOTIONAL）下不出单 → 真实裸敞口可忽略；其 60s churn/critical 待部署「dust 收口」版本后停止。

### 10.1 已落实改动（本批）

**修正既往认知**：经代码确认，旧 STOP 语义是真 bug——`runManageBinding` 用 `isLive()=active` 传给 manage，暂停时 manage 走影子，可能把真仓影子 finalize 成失联裸腿。已修。

| GPT 项 | 改动 | 关键点 |
|--------|------|--------|
| P0 STOP 语义重做 | `manageLive()/closeLive()` 与 `active` 解耦，接 control_plane | 暂停=只停开仓，manage/verified-close/flatten 真钱；nil 退化安全 |
| P0 dust 收口 | 负 carry 现货空残腿 < minNotional → best-effort 还币 + finalize + dust_ledger | 停 FLUX 式无限 churn；`position_dust.go` |
| P0/P1 删生产 shadow + 三态 | `ledger_guard.go`（非 live 绝不写真账本，dry-run 进 `records/dryrun/`）+ `RunMode OFF/CANARY_LIVE/LIVE` | 删合成假成交持仓；**无 `lingxi shadow` 子命令** |
| P0 账户级硬风控 Stage B | 一级硬停开仓（默认）+ 二级 opt-in auto-flatten（仅真实权益、默认关、不自动解除） | `account_risk_watch.go`；新增 config 阈值 |
| P0 single-writer lease | 真钱装配抢 `.lingxi-live.lock`，双开 fail-fast | `live_stack_lease.go`；与 emergency_flatten 兼容 |
| P0 flatten/watchdog 五覆盖 | CLI flatten 复用五执行器 CloseAllPositions；watchdog.sh 检测五类 store | 补 dead-man 盲区 |
| P0 SWITCH 事务性 | close 失败禁 open；open 失败记 `switch_failed_open`（critical）到 domain_events | 不再静默「平旧没开新」 |
| P1 隔离 legacy 脏 PnL | `settlement_trust.go` 统一判据（fab6afd 边界 / 缺 funding_truth / attribution_incomplete / \|trade_pnl\|>50） | risk watcher / go-no-go / dashboard 三处分桶；HEI 假亏不再触发风控 |
| P1 全局 capital budget | `RotationConfig.TotalDeployableUSDT` + `MaxStrategyCapitalUsagePct`（默认 0 不生效） | 五策略（含 perp-perp held）同预算竞争；阻断 reason 明确 |
| P2 perp-perp 进 UI | `/api/rotation/positions` 含 perp-perp 条目 | 纯只读展示 |
| P2 carry first-leg unknown | 四 carry 执行器第一腿 unknown → 禁回滚/禁下二腿 + critical + 留台账 | 与 perp-perp 对齐 |
| P2 dead-man 覆盖 carry | 四 carry manage 对 perp 腿 venue arm dead-man（OKX/Gate 等原生支持） | `armDeadMansForVenueNames` 复用 |

### 10.2 未尽 TODO（本批后仍留）

> **2026-06-19 勘误**：`strategy_mode: profit_seeking` **已写入 `config.yaml`**（加载时 `ApplyStrategyModeToEconomics` 收紧成本闸）；下文「尚未改 config」已过时。

- CANARY_LIVE 与 config 字段映射（当前 OFF/LIVE 双态可用）。
- AttributionRunMode（collect_data/canary_profit/profit_seeking）与 RunMode 显式联动。
- Bybit/OKX/Gate 遗留单所执行器的 `[影子]` 日志与 mode 字符串（本批聚焦四 carry + perp-perp）。
- capital_allocator `shadow_rebalance_suggestions`（分配器观测语义，非交易态；**非已删 shadow 模式**）。
- go/no-go 与止损阈值仍为建议初值，需采样前确认锁死。
- ~~成本闸 profit_seeking_mode 切换~~ — **已改 config**（见上勘误）。

### 10.3 部署 checklist 补充（在 §9.3 基础上）

- 启动会要求单写租约：确认无第二实例持 `.lingxi-live.lock`，否则 fail-fast。
- 部署后 dust 收口生效，确认 FLUX 类 churn/critical 停止；剩余 dust 记入 `dust_ledger.json`。
- 账户级硬风控默认只「硬停开仓」；如需 auto-flatten 需显式开 `AutoFlattenOnHardDrawdownEnabled` 并设 `AutoFlattenAccountDrawdownUSDT`（高阈值、用真实权益）。
- control_plane 触发 global_halt 后**不自动解除**，需人工 START/清 halt。
- watchdog.sh 已覆盖五类 store；确认 `FLATTEN_ON_DEAD` 开关符合预期。

