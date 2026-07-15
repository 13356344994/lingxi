# 机制搜索 V2 — Opus 结构性付款者候选集

> Candidate set ID: `firstbattle.mechanism_search_v2.candidates_opus`
> Owner: `Claude Opus 4.8 Thinking High`
> Domain: 结构性付款者 / 规则 / 偿付 / 结算 / 库存 / 授权机制
> Generation: `msv2-opus-candidates-v1`
> JSON: `research/firstbattle/mechanism_search_v2/candidates_opus.json`
> JSON candidates_hash (self-excluded): `1ed962fc83026349a4f112c87c7d185ca9604566e14245395434824b898c4d32`
> Document hash: `a17cb90bc31c6189a584290362d6ef79e5f194ff92e20856da66beedfc717c97`
> Status: `OUTCOME_BLIND_CANDIDATE_PROPOSALS_ONLY_STOPPED`
> `labels_unblinded=false` · `routes_started=0`

## 0. 独立性与边界

- 本文件在**未读取**任何其他 owner（Fable/Grok）的 v2 候选、**未读取**任何 `return/pnl/fillPnl/winner/mfe/mae/考卷` 前独立生成。
- 可测性判断只用 `route_data_source_registry` / history catalog(`g10_reopen_condition_inventory`) / 官方交易所元数据；不下载大数据、不购买、不启动路线、不修改 plan/master/records/冻结工件、不提交。
- 只修改我自己的两个文件：`candidates_opus.json` 与 `candidates_opus.md`。

## 1. 最高目标

> 在不同市场状态、特殊事件和个人执行条件下，动态路由到各自经过历史证据认证的专属盈利路径；没有可靠匹配路径时必须空仓。最终以资金流调整后的真实账户净值长期增长为唯一成功标准。

小账户约束：`30U–数千U`，杠杆 `<= 5x`，认证前须绑定真实手续费/滑点/资金费/最小下单/延迟。

## 2. H0 null 与竞争假设

- **H0_NULL_STRUCTURAL**：所有候选的表面 edge 只是选择偏差、成本低估、公共因子重命名、窗口不足或事后归纳；不存在被结构性规则/结算/偿付/授权强制的付款者，也不存在小账户可事前区分并吃到的净 edge。除非冻结完整事件分母 + 匹配输家并逐层通过 `observability → identifiability → ex_ante`，否则默认 H0。
- **H1_SETTLEMENT_CLOCK_COERCION**：确定性结算时钟（资金费 8h / 借币利息按小时 / 交割日）对某一侧强制转移价值，被迫一侧在结算窗口附近产生暂时性库存错位，其分母是官方结算事件本身。
- **H2_DELIVERY_CONVERGENCE_COERCION**：定期交割合约在交割时刻被强制按指数结算，基差必须收敛；持有基差一侧被交割时钟强制平仓。
- **H3_AUTHORIZATION_MANDATED_FLOW**：授权型强制流（跟单镜像/子账户授权/强制赎回）在被授权动作触发时确定性执行，付款者是预授权的跟随方。
- **H4_REIMBURSEMENT_SOCIALIZATION**：偿付/社会化机制（ADL/保险基金穿仓分摊）在破产缺口把损失强制转嫁给盈利对手方。

本轮不读任何收益判断胜负，只判断“现有官方/本地历史能否观测并识别其完整事件分母与匹配输家”。任一候选无法冻结完整分母与匹配输家即回到 H0，分类 `DATA_BLIND/REJECTED`，不降门槛、不等未来数据。

## 3. Outcome-blind 评分规则（读取任何收益前冻结）

| 维度 | 权重 |
|---|---|
| 机制结构强度 | 30% |
| 现有历史可观测性 / identity / 完整分母 | 25% |
| 公开后机构排斥 / 容量护城河 | 20% |
| 本账户真实可执行性 | 15% |
| 最短证伪成本 | 10% |

禁用信号：`future_return / realized_pnl / winner_profit / 近期实盘 / g10_residual_magnitude`。本文件不打最终分，仅提出候选，交由独立红队按此规则排序。

## 4. 非同源基线（必须避开）

| 既有路线 | 核心 |
|---|---|
| `micro_execution_capacity_cliff` (R1) | 自有小单 fill/冲击/排队/授权摩擦容量断层 |
| `exchange_rule_discontinuity` (R2) | ctVal/lot/tick/minSz/结算规则/迁移/风控参数离散版本变化 + 容量/保证金断点（需 PIT 版本链） |
| `sparse_forced_flow` (R3) | 清算/到期/保证金/规则整改/抵押品短缺迫使一侧短窗成交 |
| `cross_venue_operational_asymmetry` (R4) | 跨场所规格/抵押品/授权/结算/最小下单差异错位 |

G1–G10 是同一“司天监 → 分拣器 → 机制 → 条件方向”研究的历代 generation，机制空间以上述四路线 + `BTC return × realized volatility × BTC funding STATE × horizon × direction` 单/双特征公共因子规则为主。本候选集避免：(a) 复用该公共因子规则空间；(b) 把 funding/vol 当公共 STATE 因子回归；(c) 与四路线同构。每条候选给出显式 `non_homology_proof`（付款者/触发时钟/事件分母/required fields 四维）。

## 5. 候选汇总

| ID | route_id | verdict | 分类 | 强制来源 | 事件分母 identity |
|---|---|---|---|---|---|
| MSV2-OPUS-01 | `funding_settlement_instant_coercion` | RETAIN_HYPOTHESIS | `DATA_PARTIAL_PIT_GAP` | 永续资金费 8h 结算 | (symbol, settlement_stamp) 官方确定性键 |
| MSV2-OPUS-02 | `dated_delivery_basis_convergence_settlement` | RETAIN_HYPOTHESIS | `DATA_BLIND` | 定期交割结算收敛 | (dated_contract_id, delivery_stamp) |
| MSV2-OPUS-03 | `margin_borrow_interest_settlement_coercion` | RETAIN_HYPOTHESIS_WEAK | `DATA_BLIND` | 借币利息按小时结算 | (currency, hourly_interest_settlement) |
| MSV2-OPUS-04 | `copy_trade_authorized_mirror_flow_coercion` | RETAIN_HYPOTHESIS | `DATA_BLIND_SOURCE_ABSENT` | 跟单授权镜像 | (lead_trader_id, action_event) |
| MSV2-OPUS-05 | `adl_insurance_fund_socialization_reimbursement` | REJECTED | `REJECTED_HOMOLOGY_AND_DATA_BLIND` | ADL/保险基金社会化 | 无 ADL 事件 identity |
| MSV2-OPUS-06 | `funding_rate_cap_clamp_persistent_imbalance` | REJECTED | `REJECTED_PIT_UNPROVEN_AND_SIBLING_HOMOLOGY` | 资金费封顶 clamp | cap_version 无 KnownFrom |

计数：`proposed=6` · `retain=3` · `retain_weak=1` · `rejected=2` · `present_complete=0` · `routes_started=0`。

---

## 6. RouteCard 详述

### MSV2-OPUS-01 · 资金费结算瞬时强制付款者（8h 结算时钟）— `RETAIN_HYPOTHESIS` / `DATA_PARTIAL_PIT_GAP`

- **机制**：永续在每个 8h 结算戳（OKX 00:00/08:00/16:00 UTC）按 `position_notional × funding_rate` 在多空间强制转移价值。付款者=结算瞬时处于付款侧的持仓者（被合约强制转出）；次级付款者=为规避付款在戳前被迫减仓/翻转的高杠杆持有者。被迫动作在戳附近制造暂时库存错位与随后回归；事件分母是官方结算事件本身。
- **urgency**：硬交易所结算时钟，转移在戳点确定发生，规避窗口分钟级。`EventTime=交易所结算戳`。
- **支持市场状态**：`|funding|` 极端 bucket（单侧拥挤、OI 严重失衡）且临近结算戳的 event-clock regime。
- **分支**：LONG=付款侧为空头（funding 深正）且戳前有被迫减仓压力时承接、持有过戳、回归窗内平；SHORT=付款侧为多头（funding 深负）的镜像；FLAT=`|funding|` 中性或 OI 不失衡；HOLD=已在收款侧且错位-回归未完成；EXIT=回归窗关闭/退出极端 bucket/戳后 taker-flow 中性化；**NO_TRADE**=缺 funding 现值/下一期、缺结算戳对齐、OI 失衡不可算。
- **机构排斥 + 公开后护城河**：机构在整本库存承担 funding，无法在戳点无自冲击翻转符号；结算是硬规则、付款义务随规模线性放大。公开后每标的每戳错位容量极小，跨标的聚合触及容量/监控/自冲击成本；“知道机制”不降低 per-notional 结算成本。
- **小账户优势**：戳点翻转/进出冲击可忽略、tiny notional funding 微不足道、可瞬时归零库存。
- **决策时刻字段**：funding_rate 现值+下一期、结算戳+time_to_settlement、PIT funding interval(KnownFrom)、OI/多空比失衡桶、戳前 taker-flow 失衡(event-time)、depth/spread/mid、symbol/session、min size/tick/lot(PIT)。
- **完整分母 + 匹配输家**：分母=in-scope 标的全部 funding 结算事件（每 8h×标的）；匹配输家=同 funding-sign×magnitude-bucket×session×OI-imbalance-bucket 中未获错位-回归定位者。fill/no-fill 属执行物理；净 edge/PnL forbidden。
- **现有历史 source / 可行性**：官方 `okx_historical_data_portal_and_cdn`（funding_rate 2022-03+ 提供戳与费率；trades_tick 2021-09+ 提供戳附近 tape）、instruments 快照（interval 仅向未来 KnownFrom）；本地 xmeta funding/OI 1,733 行（稀疏，不足每戳 OI 失衡分母）、hot/market mids（WEAK 上下文）、私有 fills(3M) 仅校验。**优势**：事件分母（结算戳）可从官方 funding 历史干净构造，`identity=(symbol, settlement_stamp)` 官方确定性键。**缺口**：interval 历史 KnownFrom 版本链缺失（PIT_UNPROVEN）；决策时刻 OI 失衡仅 1,733 稀疏快照；戳附近 taker-flow 需 tape 对齐验证。
- **PIT / identity / 三时钟**：Event=结算戳/tape；Receive=recorded_at；Process=落盘；interval/费率上限须 KnownFrom，当前仅向未来快照。
- **最强替代解释**：只是 funding carry（公共因子）/单侧拥挤动量/00:00 UTC 换日 session 效应。区分：匹配输家控制 funding 幅度/方向/session，只测结算戳错位-回归结构量，并分层排除纯 UTC-day 效应。
- **最短 outcome-blind 证伪**：仅用现有 funding 历史+公共 tape 验证 (1) 戳与 tape 事件时间可对齐；(2) 戳前可算 OI 失衡/taker-flow 决策特征（不用戳后信息）；(3) interval PIT 可证。任一不成立即停，全程不读收益。
- **预算 / stop / reopen**：`1 CPU / 8h / 1 次 / 仅冻结历史`。停止门=interval PIT 不可证 / 无戳前 OI 失衡特征 / 戳与 tape 不可对齐。重开仅限现有 funding+tape 补齐决策时刻 OI 失衡分母，不等未来。
- **非同源证明**：非 R1（不依赖自有 order lifecycle/fill）、非 R2（结算是循环转移事件非 spec 版本变化，无需 ctType 版本链）、非 R3（funding 结算不在 清算/到期/保证金/整改/抵押 集合内，付款者是 funding 付款方）、非 R4（单场所）、非 G-series（不把 funding 当公共 STATE 因子）、非 sibling（与 OPUS-03 基质不同）。
- **下游风险**：`PIT_UNPROVEN(interval) / INSUFFICIENT_SUPPORT(OI 失衡稀疏) / POSTHOC_ONLY`。

### MSV2-OPUS-02 · 定期交割结算收敛强制（交割日时钟）— `RETAIN_HYPOTHESIS` / `DATA_BLIND`

- **机制**：定期（周/季）交割合约在交割戳被强制按交割指数结算，基差 `future - index` 必须收敛到零。付款者=临近交割仍持非零基差的一侧，被强制平仓或接受现金结算。收敛是结算规则，非价格预测。
- **urgency**：固定交割戳，收敛在交割瞬间强制完成，越临近规避成本越高。
- **支持市场状态**：临近周/季交割的 T-minus calendar-clock regime。
- **分支**：LONG=贴水（future<index）收敛须向上；SHORT=升水收敛须向下；FLAT=远离交割/基差在噪声带；HOLD=已在收敛侧至临界；EXIT=收敛入阈值/进入结算冻结期；**NO_TRADE**=缺交割日历 PIT/缺定期合约 tape/基差不可算。
- **机构排斥 + 护城河**：机构做基差受定期合约薄流动性、交割机制、库存约束；每到期事件容量受限于该合约深度（远小于永续），跨到期聚合触及容量/监控成本。小账户以极小基差持有至收敛几乎无冲击。
- **小账户优势**：微小基差头寸持有至收敛无冲击。
- **决策时刻字段**：dated future price + spot/index（算基差）、交割戳+time_to_delivery(PIT 日历)、合约 identity(expiry/listTime/state) PIT、dated 合约 depth/spread、session、min size/tick/lot(PIT)。
- **完整分母 + 匹配输家**：分母=全部 (dated_contract, delivery_event) 及 T-minus 决策点；匹配输家=同 tenor×升贴水符号/幅度 bucket×session 中未定位收敛者。
- **现有历史 source / 可行性**：官方 trades_tick 2021-09+（定期 FUTURES tape 可得性未验证，registry 主面向 SWAP）、instruments 快照仅当前 live（已交割合约消失）；history catalog 7 family 无 instrument/rule/交割事件 archive（inventory §4.2）。**阻断**：已交割合约无 PIT（PIT gap）、定期合约历史 tape 未验证、无官方交割事件 archive → 观测层 `DATA_BLIND`。
- **PIT / identity / 三时钟**：identity=(dated_contract_id, delivery_stamp)；Event=交割戳/tape；日历与 spec 须 KnownFrom，已交割合约当前快照缺失 → PIT_UNPROVEN。
- **最强替代解释**：基差只是利率/持有成本 carry（公共），或临近交割移动只是流动性枯竭噪声。区分需交割日历 PIT + 定期合约 tape，当前均缺。
- **最短证伪**：盘点现有历史是否存在 (1) 已交割合约 tape；(2) 带 KnownFrom 的交割日历/spec；(3) 决策时刻可算基差的 index 序列。任一缺即停（预计立即 DATA_BLIND）。
- **预算 / stop / reopen**：`1 CPU / 6h / 1 次`。停止门=无已交割合约 tape 或无交割日历 PIT。重开仅限合法已交割合约 tape + 带来源时间的交割日历。
- **非同源证明**：非 R1/R4；非 R2（交割是合约结算事件非 spec 参数离散变化）；非 R3（R3 的“到期”是被保证金/清算强制短窗成交，此处是全体持有者在交割戳有序结算强制收敛，付款者是基差持有者）；非 G-series；与 OPUS-01 基质为定期交割收敛。
- **下游风险**：`DATA_BLIND(observability) / PIT_UNPROVEN(交割日历)`。

### MSV2-OPUS-03 · 多币种自动借币利息结算强制（按小时结算时钟）— `RETAIN_HYPOTHESIS_WEAK` / `DATA_BLIND`

- **机制**：跨/组合保证金下负币种余额触发自动借币，借币利息按小时结算并被规则强制计提；账户被迫在每个利息结算戳前后偿还或继续被计息（借贷结算/偿付规则）。付款者=持自动借币库存、在利息结算戳被强制计息者；被迫动作=戳前偿还/减仓的去杠杆流。
- **urgency**：硬按小时利息结算时钟，未偿还即确定性计息，随借币规模线性放大。
- **支持市场状态**：借币利率飙升（借贷紧张）regime 且临近小时结算。
- **分支**：LONG=被计息侧被迫抛压过度时承接；SHORT=被计息侧被迫买回平借币空头造成暂时上行时；FLAT=利率中性/无戳邻近；HOLD=错位-回归未完成；EXIT=退出利率紧张 bucket/回归完成；**NO_TRADE**=缺借币利率现值/缺结算戳/无法识别被迫去杠杆流。
- **机构排斥 + 护城河**：机构大额借币承担全额利息且难在戳点无冲击偿还；利息随规模线性放大不可规避、被迫流分散、单事件容量小。小账户借币微小、偿还无冲击。
- **决策时刻字段**：borrowing_rate 现值+下一结算戳、小时结算戳+time_to_settlement、借币库存压力代理、受影响 pair depth/spread/mid、session、min size/tick/lot(PIT)。
- **完整分母 + 匹配输家**：分母=全部 (currency, hourly_interest_settlement) 事件及决策点；匹配输家=同利率 bucket×币种流动性×session 中未定位去杠杆错位者。
- **现有历史 source / 可行性**：官方 `okx_historical_data_portal_and_cdn`（borrowing_rate 2021-12+；trades_tick 2021-09+）；**阻断**：借币利率与结算戳可观测（identity=(currency, settlement_stamp)），但“谁被强制偿还”无事件 identity、dislocation 对现货借贷市场分散不定域 → identifiability 失败 → `DATA_BLIND`（明显弱于 OPUS-01）。
- **PIT / identity / 三时钟**：identity=利率结算戳可识别、被迫动作不可识别；Event=利息戳/tape；利率/计息规则须 KnownFrom，规则变更无版本链则 PIT_UNPROVEN。
- **最强替代解释**：借贷成本 carry（公共），或利率飙升与价格波动同源共动而非被迫去杠杆流。
- **最短证伪**：盘点能否 (1) 对齐利息戳与 tape；(2) 在决策时刻构造被迫去杠杆流可识别特征而非仅利率。第二点几乎必然失败即停。
- **预算 / stop / reopen**：`1 CPU / 6h / 1 次`。停止门=无被迫去杠杆事件 identity。重开仅限出现可识别强制偿还/去杠杆流历史。
- **非同源证明**：非 R1；非 R2（按小时利息是循环结算事件）；非 R3（付款者是自动借币计息方、被利息结算时钟强制、对象是去杠杆错位）；非 R4；非 G-series；与 OPUS-01 基质为保证金借币利息。
- **下游风险**：`UNIDENTIFIABLE(被迫去杠杆流) / POSTHOC_ONLY / DATA_BLIND`。

### MSV2-OPUS-04 · 跟单授权镜像流强制（授权动作时钟）— `RETAIN_HYPOTHESIS` / `DATA_BLIND_SOURCE_ABSENT`

- **机制**：跟随者预先授权镜像带单员；带单员开/平仓时全部跟随账户被授权规则强制秒级镜像，形成与跟随 AUM 成比例的确定性聚合流（授权机制）。付款者=被授权强制追随带单员成交（更差价格）的跟随者聚合流。
- **urgency**：授权镜像在带单员动作触发后秒级确定性执行，跟随流无自主择时，urgency 来自授权契约。
- **支持市场状态**：高 AUM 带单员在低流动性标的交易的 idiosyncratic-flow regime。
- **分支**：LONG=带单员在薄标的开多触发镜像买入波、冲击过度后回归时对侧承接（fade）；SHORT=开空触发卖出波、过度下冲后回归；FLAT=无带单员动作/标的过深；HOLD=镜像波冲击-回归未完成；EXIT=回归完成/镜像流耗尽；**NO_TRADE**=缺带单员动作 identity/缺跟随 AUM 代理/深度不可算。
- **机构排斥 + 护城河**：授权镜像流是特定带单员-跟随者关系的特质流，机构难统一复制且每事件容量受限于薄标的深度；授权契约驱动的强制流持续存在、单事件小容量、跨带单员特质不可聚合成机构级容量。
- **小账户优势**：微小规模可在薄标的镜像波对侧承接而无自冲击；机构承接会二次放大冲击。
- **决策时刻字段**：带单员动作事件(open/close, symbol, side, time)、每 lead 跟随 AUM/copier 数代理(KnownFrom)、薄标的 depth/spread/mid、标的流动性桶/session、min size/tick/lot(PIT)。
- **完整分母 + 匹配输家**：分母=全部带单员动作事件（每 lead×action）及决策点；匹配输家=同 lead-AUM bucket×标的深度×side×session 中未定位镜像波错位者。
- **现有历史 source / 可行性**：OKX 跟单公共带单员/持仓接口存在但为快照/滚动，非完整历史强制流分母（registry 未登记、属 `ABSENT_EXPECTED`）；trades_tick 仅聚合 tape 无法归因授权流。**阻断**：现有 registry/catalog 均无带单员动作/镜像流历史（source ABSENT），tape 无法归因 → 观测层 `DATA_BLIND / SOURCE_ABSENT`。机制新颖但当前不可测。
- **PIT / identity / 三时钟**：identity=(lead_trader_id, action_event)→镜像波，当前无稳定历史 event ID；Event=带单员动作时间；跟随 AUM/授权关系须 KnownFrom（当前仅快照）。
- **最强替代解释**：镜像波只是一般 taker 动量/新闻驱动；带单员本身可能是聪明钱而非被动流；薄标的移动是流动性噪声。区分需带单员动作 identity + 跟随 AUM。
- **最短证伪**：盘点现有历史/接口是否提供带单员动作事件+时间+跟随 AUM 代理、可否构造完整动作分母。当前无 → 立即 `DATA_BLIND`。
- **预算 / stop / reopen**：`1 CPU / 6h / 1 次`。停止门=无带单员动作历史 identity。重开仅限出现合法带单员动作历史 + 跟随 AUM 代理（须单独审批数据存在性盘点），不等未来采集。
- **非同源证明**：非 R1（付款者是被授权第三方跟随流）；非 R2（触发是授权镜像动作）；非 R3（是跟随者预授权契约的商业强制流、付款者是自愿预授权跟随方而非被清算方，强制来源完全不同）；非 R4；非 G-series；唯一以“授权”为强制来源的候选。
- **下游风险**：`DATA_BLIND / SOURCE_ABSENT / UNIDENTIFIABLE(镜像流归因)`。

### MSV2-OPUS-05 · ADL / 保险基金穿仓社会化偿付强制 — `REJECTED` / `REJECTED_HOMOLOGY_AND_DATA_BLIND`

- **机制**：保险基金不足吸收穿仓时，交易所通过 ADL 强制盈利对手方按破产价平仓，把损失社会化转嫁给赢家一侧（偿付/社会化结算）。付款者=被 ADL 强制减仓的盈利一侧。
- **未展开 LONG/SHORT/FLAT/HOLD/EXIT**：候选在观测层即被拒，展开分支会给不可测机制虚假可执行外观；`NO_TRADE_DEFAULT`=无 ADL/保险基金事件 identity → 恒空仓。
- **现有历史 / 可行性**：无官方 ADL 事件历史 archive、保险基金余额仅当前/滚动接口；`okx_official_liquidation_history_batch=ABSENT`；本地 liquidation streams（OKX 62,728 / Bybit 85,960）是被清算侧观测、非被 ADL 赢家侧事件。观测层失败：无 ADL 事件 identity、无完整分母。
- **拒绝理由**：
  1. 观测层 `DATA_BLIND`：现有源无 ADL/保险基金事件 identity（liquidation streams 只是被清算侧）。
  2. **同源风险**：ADL 是清算级联的镜像（保险基金穿仓后社会化），触发条件/时钟/完整分母与 R3 `sparse_forced_flow` 高度重叠，会共用同一 forced-flow 分母，非同构证明失败。
  3. 护城河仅为“被动少被 ADL”，非可主动吃到的付款者错位。
- **预算 / reopen**：预算 0（当前拒绝）。重开仅限出现官方 ADL/保险基金穿仓事件历史 + 稳定 identity，且先证明与 R3 非同构。

### MSV2-OPUS-06 · 资金费率上限触顶导致持续失衡释放 — `REJECTED` / `REJECTED_PIT_UNPROVEN_AND_SIBLING_HOMOLOGY`

- **机制**：交易所对每期 funding 设上/下限；理论 funding 超上限时被 clamp，付款侧被少收/少付，单侧拥挤无法被费率充分惩罚而持续累积直至离散释放。付款者=本应支付超上限但被 clamp 的一侧。
- **未展开分支**：候选被拒（PIT + sibling 同源），避免虚假可执行外观；`NO_TRADE_DEFAULT`=无封顶规则 PIT 版本链 → 恒空仓。
- **现有历史 / 可行性**：funding 历史提供已实现 funding，不提供理论未 clamp 值与封顶阈值版本链；`okx_official_rule_instrument_version_archive=ABSENT/PIT_UNPROVEN`。`PIT_UNPROVEN`：封顶阈值属规则参数、无历史版本 archive（与 R2 同 gap），且 clamp 绑定不可在决策时刻证明。
- **拒绝理由**：
  1. `PIT_UNPROVEN`：封顶阈值无历史版本链（与 R2 同 gap）。
  2. **sibling 同源**：与 OPUS-01 共用 funding 结算事件与分母，封顶效应可能只是 OPUS-01 极端 bucket 子情形，不能同时独立于 OPUS-01 与 R2。
  3. clamp 绑定不可在决策时刻证明。
- **裁决**：作为 OPUS-01 的极端-bucket 条件注记保留，不单列为独立路线。预算 0。

---

## 7. Owner Decision Journal

1. **问题重述**：作为 Opus 机制 owner，在不读收益、不改冻结工件、不提交前提下，独立提出 ≥5 个与 G1–G10 及四路线非同源的结构性付款者机制候选，仅用 registry/catalog/官方元数据判断现有历史可测性。
2. **为何选这些**：聚焦“确定性时钟上的强制价值转移”（结算/交割/利息）与“授权强制流”，因为其事件分母可由官方确定性事件（结算戳/交割戳/授权动作）干净定义，恰补四路线普遍缺失的稳定 event identity，且天然带 per-notional 硬成本护城河。
3. **竞争假设**：H0_NULL_STRUCTURAL / H1 结算时钟 / H2 交割收敛 / H3 授权强制流 / H4 偿付社会化。
4. **最可能被忽略的反例**：把 funding/borrow 结算错位误当结构强制流，实为公共 carry 因子或 UTC-session 效应的重命名（正是 G2/G5 已用过的 funding-state 公共因子）；以及把稀疏结算/交割窗口不足误当机制边界。已在每条 `strongest_alternative_explanation` 与 falsification 显式对冲。
5. **方法与弃选**：对每条做 homology_test（付款者/时钟/分母/required fields 四维）与可测性盘点。拒 OPUS-05（与 R3 同源 + ADL identity 缺失）、拒 OPUS-06（与 OPUS-01 sibling 同源 + 封顶 PIT_UNPROVEN 同 R2 gap）；OPUS-02/03/04 保留假设但诚实标 DATA_BLIND；OPUS-01 标 DATA_PARTIAL。
6. **最强候选**：MSV2-OPUS-01 —— 唯一其完整事件分母（结算戳）可从现有官方 funding 历史干净构造，`identity=(symbol, settlement_stamp)` 官方确定性键；主要缺口是决策时刻 OI 失衡分母稀疏与 funding interval PIT。
7. **已花预算**：仅读取 registry / contracts / g10 portfolio / reopen inventory / master plan 机制段与 `datatruth.HashJSON` 源码；无下载、无购买、无收益读取、无实验。
8. **停止决策**：写完两个候选文件即停，等待独立红队排序与用户批准；不自动预注册/获取数据/启动路线。
9. **reviewer 分离**：本 owner 不自批可测性 PASS 或路线重开；须由独立 Sonnet 红队检查同构/泄漏/事后选择/机构可复制性/完整分母。

## 8. Safety

```text
global_halt:      true
system_status:    NO_GO
exam_status:      EXAM_UNAVAILABLE
exam_actions:     0
trade_allowed:    false
labels_unblinded: false
routes_started:   0
no_purchase:      true
no_bulk_download: true
no_commit:        true
not_modified:     records/ | G1-G10 冻结协议/hash/generation/产物 | FIRST_BATTLE_MASTER_PLAN.md | route_data_contracts.json|.md | route_data_source_registry.json|.md | g10_* | 考卷 | 真钱开关/控制面 | plan.md
self_files_only:  research/firstbattle/mechanism_search_v2/candidates_opus.json | .md
```

## 9. Hash 复算

- **JSON**：将 `hashes.candidates_hash` 置为 `null` 后用仓库 `datatruth.HashJSON`（`json.Marshal → unmarshal → normalizeJSON → json.Marshal → SHA-256 小写 hex`）。当前值 `1ed962fc83026349a4f112c87c7d185ca9604566e14245395434824b898c4d32`。
- **Markdown**：将唯一一行 `> Document hash: \`<value>\`` 规范化为 `> Document hash: \`SELF_EXCLUDED\``，保持其余 UTF-8 LF 字节不变，再做 SHA-256。
- **JSON↔MD 一致性**：`candidate_id / route_id / verdict / current_classification / summary_counts / hypotheses / safety / hashes.candidates_hash` 必须一致，不匹配 fail closed。
