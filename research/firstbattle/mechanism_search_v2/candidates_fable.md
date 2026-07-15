# LINGXI 机制搜索 V2 — Fable 候选（倒序事件归纳 / 行为流程摩擦 / 非标准小容量）

> Artifact: `firstbattle.mechanism-search-v2.candidates-fable`
> Owner: `claude-fable-5-thinking-high`
> Generation: `mechanism-search-v2-fable-v1`
> Status: `OUTCOME_BLIND_CANDIDATES_ONLY`
> JSON: `research/firstbattle/mechanism_search_v2/candidates_fable.json`
> JSON hash (self-excluded): `a244b53ccb39e9c28d01f4287a8bf83cd66e01d4d9cd7431c83b60c1c1f97cea`
> Document hash: `879d93ebc43cdd7cb886e3725ecffb7becf33e879449542874a0fa2befe703a9`
> labels_unblinded: `false`
> routes_started: `0`
> trade_allowed: `false`
> Created: `2026-07-14T03:40:00Z`

## 0. 边界

- 只写本 owner 两个文件；不提交；不购买；不 bulk 下载；不读收益/PnL/winner/future 字段/考卷。
- 不修改 G1–G10、records、冻结合同、registry、Grok 候选。
- 本 owner 专长是倒序事件归纳，但 `labels_unblinded=false`：**全部倒序赢家归纳推迟到计划 §5 Phase B**（事件分母与 protocol hash 冻结之后）。本文件只做 Phase A 之前的机制假设与 identity 设计。
- 出发点是「哪些强制/流程事件会产生付款者」，不是价格因子。

## 1. H0 与竞争框架（先于候选冻结）

| ID | 陈述 |
|---|---|
| **H0** | 一切表观流程 edge 都是选择偏差、不完整分母、成本低估或早已公开定价的知识；全部候选默认输给 H0。 |
| F1_mandatory_clock | 外生确定性时钟（标的开收盘、上市、转换截止、结算表）迫使可识别主体在窗口内行动，时机与价格无关。 |
| F2_unhedgeable_window | 天然对冲工具暂时不存在（标的休市、无借币、TGE 前）时，风险持有者必须为退出或持有付费。 |
| F3_clamp_queue | 交易所风控钳制（funding 上限等）压低杠杆价格使强制流排队，钳制解除时集中释放。 |
| F4_statutory_distribution | 代币分发事件（上市、空投、解锁）制造合同性卖方/对冲方，时机由合约与日历决定。 |

**全局最强反例**：机构可批量聚合小容量事件；日历/流程事件全部公开，时钟越确定越易被提前定价；「新颖/没人写过」不是护城河。

## 2. 存在性盘点（feature-side only，未读任何 outcome 字段）

| 扫描 | 事实 |
|---|---|
| okx_funding 401 symbols 全量 | 147,497 settlements；formula/method 转变 0；realized≠quoted 0；间隔迁移 106；\|rate\|≥0.5% 587；相同极端印刷 run≥3 = 14 段 / 4 symbols |
| instruments snapshot | groupId=7 股票类 perp **125**（121 有本地 1h+funding tape）；OFF pre-market 合约 **0**（幸存者删除）；tape 窗口内新上市 **118**（43 个独立上市日）；maxPlatOILmt 非空 207 |
| okx_1h 股票 perp | AAPL/SPY 128 天、TSLA 135 天、GME 63 天；周末 bar 天数 36/36/38/18 → perp 在标的休市期持续交易 |
| bybit vs okx funding 时钟 | Bybit 模态 4h；OKX 8h=206 / 4h=195；双所 base 交集 336 |
| okx_5m | 股票类覆盖 52；窗口内新上市覆盖 36 |

## 3. 候选一览（6 条，outcome-blind 排序）

| # | ID | 机制 | 分类 | 加权分 |
|---|---|---|---|---|
| 1 | FAB-MSV2-01 | 股票 perp 标的休市/开盘时钟强制收敛 | TESTABLE（待 index 方法核验） | 2.275 |
| 2 | FAB-MSV2-02 | 上市周法定卖方/对冲悬顶衰减 | TESTABLE（识别弱点如实登记） | 1.95 |
| 3 | FAB-MSV2-03 | pre-market 期货 TGE 转换截止 | **DATA_BLIND** | 1.75 |
| 4 | FAB-MSV2-04 | funding 钳制钉死饱和/释放 | INSUFFICIENT_SUPPORT（当前 tape） | 1.725 |
| 5 | FAB-MSV2-06 | 跨所结算时钟错相 | TESTABLE 但同构风险最高 | 1.475 |
| 6 | FAB-MSV2-05 | vesting cliff 法定供给注入 | DATA_BLIND（需版本化日历） | 1.4 |

### FAB-MSV2-01 — equity_perp_underlying_session_gap_forced_repricing

- **机制**：OKX 121 个代币化股票/ETF 永续 7×24 交易，标的每天只交易约 6.5h。标的休市中 perp 无活参照、做市商无对冲（F2）；标的重开盘时刻（F1，确定性日历，PIT 最强），持有隔夜错方向杠杆仓位者被 mark 收敛/追保/薄簿退出付款。
- **市场状态**：UNDERLYING_OPEN / CLOSED_OVERNIGHT / CLOSED_LONG（周末假日）；私营公司 perp（OPENAI 等无标的时段）从分母排除单独登记。
- **动作分支**：休市中段 = OOD → **NO_TRADE**（默认空仓是本候选一半价值）；重开边界按可观测隔夜 perp 移动 vs 参照 gap 符号分支；basis 在成本带内 → flat；fade/follow 不预设赢家。
- **payer**：错方向隔夜杠杆持仓者 + 必须重新居中的做市商。
- **机构排斥**：受监管机构合规上不碰离岸代币化股票；想要同样敞口的机构直接用真股票/CME；加密机构同样困在不可对冲窗口且受杠杆/OI 上限。护城河=监管边界+单事件硬容量。
- **个人容量**：股票 perp 簿薄 + OI 上限，几十~几千 U 恰好可执行。
- **完整分母**：121 symbols × 全部日历边界，确定性生成零挑选；现有 okx_1h/okx_funding 全覆盖，okx_5m 覆盖 52。**注意：同日全 symbol 共享边界 → 有效独立样本≈交易日数(~90)，非事件数。**
- **identity/PIT/时钟**：`instId+calendar_id+boundary_type+boundary_utc_ms`；边界提前数年可知；features 只用边界前已收盘 bar；无真 receive 时钟 → 分钟级以下不主张。
- **matched loser**：同边界日同 |basis| 桶反符号事件 + basis≈0 对照；触发集含全部边界（禁止只取大 gap）。
- **最强反例**：若 OKX 休市期冻结 mark 并钳制 funding，收敛在做市商的一个 tick 内完成，个人 taker 吃不到；且隔夜 gap 是文献最厚的老异象，可规模化部分早被 CME 定价。
- **最短证伪**：① 零获取：核验 perp 价格在休市窗口是否真实移动；② 零获取：边界 bar 特征 vs 非边界对照可否区分；③ 一次有界核验（另批）：OKX 股票 perp index/mark 方法学页。
- **预算/停止门**：1 CPU / 6h / 1 attempt / 仅冻结 tape；mark 冻结证实 → UNEXECUTABLE 停；特征不可区分 → UNIDENTIFIABLE 停。
- **非同构**：G1–G10 无标的交易时段状态机/对冲工具可用性概念；事件由外部股票日历定义，与价格极值（G6）、funding 水平（G1/G2）、交易所自身规则参数（G10-2）、强平流（G10-3）、venue 维护窗口（Grok-07）全部正交。

### FAB-MSV2-02 — listing_week_statutory_seller_decay

- **机制**：新永续上市制造法定卖方/对冲方（F4）：空投接收者/积分农民须在固定窗口变现或对冲（时机由领取日历决定），做市商按协议被迫报价。付款者经上市周 funding 印刷与吸收结构观测。
- **状态/分支**：symbol 年龄机 LISTING_DAY / DISCOVERY_WEEK / MATURE(禁区)；仅前两状态内按首 2–6 个 funding 印刷分支；正常带内 → flat；H_hedge_overhang 与 H_fomo_decay 对称，不预设赢家。
- **payer**：对冲悬顶方或追高晚到多头，Phase B 对称裁决。
- **机构排斥**：新代币无借币、合规审批慢于上市周、簿深不容机构规模；做市机构在场但站在付款/报价侧。
- **完整分母**：tape 窗口内全部 118 个新上市（identity=tape 首 settlement，snapshot listTime 仅交叉验证）；43 独立上市日 / 4 个月；**单一上市政策 regime**。
- **identity/PIT**：`instId+first_settlement_ms` tape 内生；listTime 是不可回改出生时间戳。
- **matched loser**：同上市日跨 symbol 队列 + 同 funding 桶不同年龄 + 同 symbol MATURE 对照；触发集含全部上市（含无信号者）。
- **最强反例**：上市泵/砸是最公开的民间传说；上市周微结构可被洗盘/操纵伪造；118 事件单 regime 统计力弱；无法直接观测对冲者身份（funding 只是间接观测器）。
- **最短证伪**：零获取——上市周 funding 分布 vs MATURE 对照不可区分 → 关闭；identity 矛盾 >2 例 → 关闭。
- **预算**：1 CPU / 4h / 1 attempt / 仅冻结 tape。
- **非同构**：G1/G2 把 funding 当全市场水平因子（±0.001 已 CLOSED），此处触发集由上市日历定义、funding 仅作观测器；非 Grok-01（结算日历税）/Grok-03（退市=死亡 vs 此处=出生+分发悬顶）。

### FAB-MSV2-03 — pre_market_futures_tge_conversion_deadline

- **机制**：OKX pre-market 期货（-OFF 后缀）在 TGE/转换截止全员强制转换或结算（F1+F2 极端形态）：产品生命=倒计时，截止前无资格/不愿承接者被迫平仓。
- **payer**：截止前仍持仓且无法/不愿转换者。
- **诚实现状**：**DATA_BLIND**。本地 7 个 tape family 均无 OFF 合约；当前 snapshot OFF=0（转换后从宇宙消失=教科书级幸存者删除）；唯一存在证据是 P0 Data Fitness 的 instId drift（`DATA-USDT-SWAP-OFF20260702-2` 在私人 orders 中留痕）。连全体事件名单都无法枚举 → 不造可测性。
- **最短证伪**：一次有界存在性核验（另批）：官方 pre-market 生命周期档案（名单+条款+价格史）；ABSENT → 维持 DATA_BLIND 停。
- **最强反例**：pre-market 本质是彩票市场，TGE 重定价风险淹没流程收敛 edge。
- **非同构**：产品生命周期时钟在 G1–G10 与 Grok 候选中均不存在。

### FAB-MSV2-04 — funding_clamp_pin_saturation_release

- **机制**（F3）：funding 被上限钳制连续钉死（run≥3 个完全相同极端印刷，tape 自我暴露，**不依赖 cap 参数历史档案** → 绕开 G10-2 的 PIT_UNPROVEN 阻断）时，杠杆价格被行政压低、拥挤仓位排队积压；pin 解除=释放事件。
- **payer**：钳制期被补贴而滞留的拥挤侧，解除后须以价格或全额 funding 出清。
- **诚实现状**：当前 tape 仅 14 段 / 4 symbols → **INSUFFICIENT_SUPPORT**，支持门自动失败。唯一合法扩展=官方免费 CDN funding 历史（2022-03+，registry 已登记）的一次有界获取，需用户另批；不等待未来数据。
- **最短证伪**：零获取——核验 identical run 是否采集/API 语义假象；假象 → 立即关闭。
- **机构排斥（最弱，如实降权）**：carry 桌完全有能力；仅小市值单事件容量排斥。
- **非同构 + 杀开关**：G1=单期水平阈值；本候选=run-length 饱和状态机+release identity。**预注册杀开关：Phase B 区分特征 collapse 为单期 funding 符号/水平 → 自动判 ISOMORPH_G1 关闭。**

### FAB-MSV2-05 — vesting_cliff_statutory_supply_unwind

- **机制**（F4+F1）：vesting cliff 按提前数月公开的日历注入合同性供给；标准行为=cliff 前永续对冲锁值（付 funding 维持拥挤空头）、解锁后解除。
- **诚实现状**：**DATA_BLIND**——本地无任何解锁日历族；且存在**反向 PIT 问题**：事件时钟公开先知，但「当时已知的日历版本」无法证明，用今日日历回填历史=事后污染。无版本化 as-of 日历源 → 永久关闭。
- **最强反例**：解锁效应是 2024–2026 发表最多的加密事件研究，日线粒度可检测部分早被定价。
- **非同构**：G1–G10 无代币归属日历概念；与 FAB-02 的区分=中年合同性供给注入 vs 出生分发，时钟/payer/数据源全不同。

### FAB-MSV2-06 — cross_venue_settlement_clock_phase_offset

- **机制**（F1）：OKX 8h/4h 混合且会迁移（tape 内 106 次），Bybit 全线 4h：跨所 carry 仓位在「只有一边结算」的 offset 时刻被迫单腿付款/再平衡。
- **完整分母**：336 双所 base × 全部 offset 结算时刻，双 tape 内生确定性生成。
- **诚实自 flag**：**本 owner 同构风险最高的候选**——离 G1 只有一次特征 collapse 的距离；机构排斥最弱（跨所 carry 是机构主场）。**预注册杀开关：区分特征 collapse 为单所 funding 符号/水平或纯结算时刻效应 → 自动判 ISOMORPH_G1 关闭。**
- **最短证伪**：零获取——offset 时刻 vs matched-clock 对照在 1h bar 粒度不可区分 → UNIDENTIFIABLE 关闭；106 次迁移逐一对照采集连续性。

## 4. 弃选记录（防止换皮复活）

结算间隔加密税（=Grok-01 同构）；退市/迁移（Grok-03 已覆盖）；OI-cap 饱和 reduce-only（无 OI 历史族，完全盲）；USDC 脱锚（支持度必然不足）；Rubik 多空比极值（纯公共因子，硬拒绝命中）；上市后杠杆档解锁（与 G10-2 同等 PIT 盲）。

## 5. 最残酷自评（新奇 ≠ edge）

1. **新颖性偏好本身是风险**：六条里没有一条的新颖性构成护城河；FAB-01（隔夜 gap）与 FAB-02（上市效应）恰好落在公开文献最厚的两个异象区，先验被定价概率高，H0 仍是最大概率赢家。
2. **单一 regime**：全部可测候选依赖 3–4.5 个月单 regime tape；任何通过都只是单 regime 通过。
3. **日内聚类**：FAB-01/06 有效独立样本=交易日数（~90/~65），比事件数小两个数量级；用事件数吹支持度=自欺。
4. **间接观测 ≠ 直接证据**：FAB-01 未验证休市期 mark/index 规则（若冻结则可捕获性归零）；FAB-02 的 funding 只是 forced hedger 的间接观测器。
5. **同构压力**：FAB-06 与 FAB-04 已挂预注册杀开关，红队应默认怀疑。
6. **无真 receive 时钟**：分钟级以下主张全部禁止，已写入各候选。
7. **结论**：0 条候选获得启动资格；最好的两条也只配进入红队去重与 outcome-blind 评分池。本 owner 不自批 shortlist。

## 6. Decision Journal

1. 问题重述 → 2. 先冻结 H0+四框架与全局反例 → 3. 每候选一次 feature-side 存在性盘点（5 次扫描，零 outcome 字段，见 JSON `existence_scans_performed`）→ 4. 关键发现（121 股票 perp / 118 新上市 / OFF 幸存者删除 / 14 pin run / 106 迁移+336 双所）→ 5. 弃选 6 条并留档 → 6. 残酷自评 → 7. 预算已花：只读扫描 <10 CPU-min + 本二文件；无下载/购买/收益读取/考卷接触/提交 → 8. 提交即停，等待 Sonnet 红队与主代理裁决。

## 7. 安全开关

```text
global_halt=true / system_status=NO_GO / exam_status=EXAM_UNAVAILABLE
exam_actions=0 / trade_allowed=false / labels_unblinded=false
routes_started=0 / no_purchase=true / no_bulk_download=true / no_commit=true
```

## 8. Hash 复算

- **JSON**：读取 `candidates_fable.json`，将 `hashes.candidates_hash` 置 `null`，用仓库 `datatruth.HashJSON`（对键递归排序 + encoding/json 确定性编码 + SHA-256 小写 hex）。
- **Markdown**：将唯一一行 `> Document hash: \`<value>\`` 规范化为 `> Document hash: \`SELF_EXCLUDED\``，保持其余 UTF-8 LF 字节不变，再做 SHA-256。
