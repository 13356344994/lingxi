# lingxi 多交易所(Multi-Venue)重构落地手册

> 目标读者:实现者 / 接手 Agent。本文件自包含,照此即可把当前「币安单所单体」重构为「Binance / OKX / Gate / Bybit 四所并行、各自独立做同一套现货+合约资金费 carry」的可扩展系统,并完成面板多所化与绩效指标。
>
> 口径以代码现实为准。文中所有 `// 提议代码` 均为设计草图(新代码),非现有代码原文。

> **修订记录(v2,据第一轮 review 核实代码后更新)**:
> 1. **配置 YAML 现状更正**:`config.yaml` 已是真 YAML(`config_yaml.go` 用 `yaml.v3`,`BindConfig` 按 cfg tag 自动绑定),P3 改为"扩展 schema"而非"迁移 YAML 库"(§1 / §8 / §10 / P3)。
> 2. **控制面多所化补强**:新增 Orchestrator + per-venue `RuntimeController`/`ReportStore`、venue-scoped 控制 API、双级 emergency stop、启动隔离(§7)。
> 3. **经济参数派生口径修正**:`FeeReserveRate` 仅覆盖往返 taker 手续费;借币储备已是独立运行时项,不折叠;P2 加"币安等价性单测"(§6 / P2)。
> 4. **`allocated_capital_usdt` 与真实下单规模关系**说明(§9.1)。
> 5. **进程级耦合**章节(§13.1)+ metrics/logs/reconcile/shadow 的 venue 化(§12.1)。
>
> **修订记录(v3,据第二轮 review 核实代码后更新)**:
> 6. **每所独立调仓(最大遗漏)**:新增 §7.8(API/UI/运行约束/与 allocated_capital 关系)+ §3.1 钱包与 `CapitalManagement` 调仓抽象 + `Capabilities.SupportsCapitalRebalance/CapitalWallets`;现 `runtime_capital_adjustment.go` 已核实为整文件币安耦合。
> 7. **控制 API 清单补全**(§7.3):capital / clear-* / config preview·rollback / 下载 / 审计日志 全部 venue 化。
> 8. **绩效 Benchmark 数据源锁定**为 Binance spot BTCUSDT 日 K(§9.1);`SettlementCSVRecord` 无 venue → API 响应层注入(§9.2)。
> 9. **`BindConfig` venues 实现路径明确选 path B(struct unmarshal)**(§8)。
> 10. **残留"全局绑单所"分类**(§13.2):ramp/gear 随 per-engine cfg 自动隔离(免改);真正要动的是全局单文件路径 + 币安写死符号表。
> 11. **P2/P3 解耦**:逐币时钟在 P2 末单引擎下即验收,不依赖 P3(P2 验收⑤)。
>
> **修订记录(v4,据第三轮 review 核实代码后更新,目标:冻结)**:
> 12. **调度退役范围写实(P1 优先级)**:核实 `EnableRollingHold` 分叉 + `NextPortfolioWake` 仍以 `NextWake` 兜底(默认生产走全局 `:57`);P2 给出目标调度模型并**显式标注为受控行为变更** + 对照测试(P2 框)。
> 13. **Bybit 负 carry 去矛盾**:首版只做正 carry(`SupportsNegativeCarry=false`),与 P4 的 50/50 一致(§14)。
> 14. **`Venue` 接口 embed `CapitalManagement`** + `Transfer` 类型 + §10 模板加 `capital.go`(§3/§3.1/§10)。
> 15. **P3↔P4 调仓边界**:P3 只路由(币安整文件复用),P4 才抽象下沉(§7.8/P3)。
> 16. **`symbolRiskProfile` 中性化并入 P2**(改 base asset,与符号中性化同 PR)(§5.2/P2/§13.2)。
> 17. **`MaxEstimatedRate` 派生选方案 B**(全局粗筛 + 逐币 cap 精筛)(§6.1)。
> 18. **secret/config schema 补全**:`proxy_url`/`passphrase`/`shadow_trading`/`active_profile`/`portfolio` per-venue(§8);`WinRate` 锁日胜率(§9.4);Orchestrator 启动语义、单端口单令牌、runtime 状态 per-Tab(§7.9/§9.6);余量钱包与 `/2` 换算(§3.1);`CapitalManagement` conformance(§12)。

---

## 第 0 章 · 背景与原则

### 0.1 为什么做
- 当前只用币安一个账户,机会池太小、测试样本少。
- 扩展到 4 家 → 机会池 ×4(每家有自己的币种、费率、结算时刻)。
- README 已把「单交易所/单账户」列为系统性单点 **R36**。

### 0.2 方向(已确认)
- **方案 A**:每家交易所**各自独立**做同一套 delta 中性资金费 carry(正向=现货多+合约空;负向=借币空现货+合约多)。**腿不跨所**,对冲在同一账户内原子完成。
- **非目标**:跨所套利(一腿在 A 所、一腿在 B 所)不在本次范围——那会丢掉同账户原子对冲的安全保证。

### 0.3 工程原则(必须贯穿)
1. **先抽象、不改行为**:用绞杀者(strangler-fig)式重构,全程保持币安实盘可运行。
2. **开放-封闭**:新增一家 = 写一个适配器 + 注册一行,**永不回头重构已稳定运行的所**。
3. **隔离**:每所独立账户/资金/熔断/dead-man/单写租约/记录目录;一所故障只读+告警,不连累其它所。
4. **能力协商**:不强求各所对齐;某所做不了的能力(如借币做空)用 capability flag 跳过,不强行抹平。
5. **经济参数实拉优先**:费率、费率上下限、借币利率、杠杆档位、精度等**能从交易所拉就拉**,拉不到才用每所配置兜底;**消灭"全局共享的币安常量"**。

---

## 第 1 章 · 现状耦合诊断(grounded)

| 层 | 现状 | 重构动作 |
|---|---|---|
| 纯逻辑边界包 `money`/`risk`/`fillmodel`/`strategy`/`lifecycle`/`marketdata` | 已 venue-neutral ✅ | **原样保留**,最大资产 |
| `internal/exchange` | 币安专用 client(签名/限频/缓存) | 降级为 `venue/binance` 适配器内核 |
| `internal/app` 引擎 | 持有具体 `*BinanceClient`,**币安端点/签名/错误码硬编码遍布 16+ 文件** | 改持有 `Venue` 接口,端点知识下沉适配器 |
| `internal/app` 编排 `run.go` | 单引擎、单面板、单配置 | 升级为多引擎编排器 |
| `internal/core` 配置 | ✅ **已是真 YAML**:`config_yaml.go` 用 `gopkg.in/yaml.v3` 解析嵌套,`config_loader.go`+`core.BindConfig` 按 cfg tag 自动绑定(M-S1.3,顺带已解决 R24 人工白名单);单所 flat secret | **扩展 schema**:加 `venues:` 多层级 + 每所独立 secret 结构 + per-venue 字段(非"迁移 YAML 库") |

### 1.1 关键耦合证据(实现时按此清查)
- 引擎直接持有具体类型:`engine_types.go` 的 `client *BinanceClient`(非接口)。
- 端点字符串拼接散落引擎层,例:
  - `execution_protection.go:441/445`:`SpotBaseURL()+"/api/v3/order"`、`FuturesBaseURL()+"/fapi/v1/order"`
  - `engine_settlement.go`:`/fapi/v1/income`、`/fapi/v1/leverage`、`/fapi/v1/marginType`、`/sapi/v1/margin/borrow-repay`、`/sapi/v1/asset/transfer`、`/fapi/v3/account` 等十余处
  - `engine_open_exec.go:286/294/371/375`:`premiumIndex`/`depth`
  - `user_data_stream.go`:`listenKey`(币安专属)
  - `dead_mans_switch.go:120`:`countdownCancelAll`(币安专属 Kill-Switch)
- 签名:`binance_gateway.go` 的 `X-MBX-APIKEY` 头 + `&signature=` + `exchange.Sign`(HMAC-SHA256)。
- 错误码硬编码:`exchange.go` 的 `ClassifyException`(`-1015/403/418/429`)。
- **经济参数现状(重要)**:
  - 手续费 ✅ **运行时实拉**:`funding_sniper_engine.go:237-259` 的 `ResolveAccountTradingCostProfile` 覆盖 `HistoricalConfig.SpotTakerFeeRate/FuturesTakerFeeRate`。
  - ⚠️ `FeeReserveRate`(进场闸 `coherentMinFundingRate` 的输入)**手工写死**,你的 `config.yaml` 注释明说是「三档 BNB 抵扣后」=**币安专属现实**。
  - ⚠️ `MaxEstimatedFundingRate=0.03` / `MaxFundingRate=0.03`(`defaults.go`)是**写死的币安假设**。
  - ✅ 借币利率/可借量/杠杆档位/精度均已实拉(`next-hourly-interest-rate`/`MaxBorrowable`/`leverageBracket`/`exchangeInfo`)。
  - ⚠️ 风险分层 `symbolRiskProfile`(`engine_risk_util.go`)按币安符号写死。
- **无包级可变全局状态**(已核查):`Run → loadEngineConfig → startLiveStack` 当前单引擎,但一个进程内并存多引擎技术可行。

---

## 第 2 章 · 目标架构总览

```
cmd/lingxi/main.go
   └─ internal/app/orchestrator.go        ← 新:多引擎编排器(N 个 Venue → N 个 Engine)
        ├─ Engine(binance)  ── Venue ──→ internal/venue/binance/
        ├─ Engine(okx)      ── Venue ──→ internal/venue/okx/
        ├─ Engine(gate)     ── Venue ──→ internal/venue/gate/
        └─ Engine(bybit)    ── Venue ──→ internal/venue/bybit/
   每个 Engine 复用同一套 venue-neutral 纯逻辑 + 同一套策略/风控算法

internal/venue/                            ← 新:venue 抽象 + 领域中性类型
   ├─ venue.go        (Venue 接口族 + Capabilities + Registry)
   ├─ types.go        (Instrument/FundingSnapshot/OrderRequest/Position/Balance/VenueError…)
   └─ <vendor>/       (各家适配器:中性请求 ↔ 该所 REST/WS 翻译)
```

---

## 第 3 章 · 核心抽象:`Venue` 接口族

放在新包 `internal/venue`。**按能力域拆小接口**,适配器组合实现,引擎按需依赖最小子集(避免巨型接口)。

```go
// 提议代码 internal/venue/venue.go
package venue

type Venue interface {
	Name() string                 // "binance"/"okx"/"gate"/"bybit"
	Capabilities() Capabilities
	SymbolMapper                  // 规范符号 ↔ 该所符号
	MarketData                    // 公共行情(无需签名)
	AccountData                   // 账户/持仓/余额(签名只读)
	Trading                       // 下单/撤单/查单(签名写)
	Streaming                     // WS:行情流 + 用户数据流
	Safety                        // 错误分类 / 限频预算 / Kill-Switch
	CapitalManagement             // 钱包快照 + 调仓(见 §3.1);不支持→ErrUnsupported + SupportsCapitalRebalance=false
}

type SymbolMapper interface {
	ToVenue(canonical Symbol, m Market) (string, error)  // BTC-USDT → BTCUSDT / BTC-USDT-SWAP / BTC_USDT
	ToCanonical(raw string, m Market) (Symbol, error)
}

type MarketData interface {
	FetchInstruments(ctx context.Context) ([]Instrument, error)        // 现货+合约元数据
	FetchFundingSnapshot(ctx context.Context) ([]FundingSnapshot, error) // 等价 premiumIndex+fundingInfo
	FetchBookTicker(ctx context.Context, m Market, s Symbol) (BookTicker, error)
	FetchDepth(ctx context.Context, m Market, s Symbol, limit int) (Depth, error)
	FetchOpenInterest(ctx context.Context, s Symbol) (float64, error)
	FetchRecentQuoteVolume(ctx context.Context, m Market, s Symbol, window time.Duration) (float64, error)
	ServerTime(ctx context.Context) (time.Time, error)
}

type AccountData interface {
	FetchBalances(ctx context.Context) (Balances, error)
	FetchPositions(ctx context.Context) ([]Position, error)
	FetchTradingCostProfile(ctx context.Context, syms []Symbol) (CostProfile, error)
	MaxBorrowable(ctx context.Context, asset string) (float64, error)   // 不支持→ErrUnsupported
	FetchLeverageBrackets(ctx context.Context, s Symbol) ([]LeverageBracket, error)
	VerifyAPIKeyPosture(ctx context.Context) (KeyPosture, error)         // 提现权限/IP 白名单自检
}

type Trading interface {
	PlaceOrder(ctx context.Context, req OrderRequest) (OrderResult, error)
	PlaceBatch(ctx context.Context, reqs []OrderRequest) ([]OrderResult, error)
	CancelOrder(ctx context.Context, m Market, s Symbol, clientOrderID string) error
	QueryOrder(ctx context.Context, m Market, s Symbol, clientOrderID string) (OrderResult, error)
	SetLeverage(ctx context.Context, s Symbol, lev int) error
	SetMarginMode(ctx context.Context, s Symbol, mode MarginMode) error
	BorrowRepay(ctx context.Context, asset string, amt float64, dir BorrowDir) error // 不支持→ErrUnsupported
	TransferWallet(ctx context.Context, from, to Wallet, asset string, amt float64) error
	FetchRealizedFunding(ctx context.Context, s Symbol, since time.Time) ([]FundingIncome, error)
	FetchUserTrades(ctx context.Context, m Market, s Symbol, since time.Time) ([]Trade, error)
}

type Streaming interface {
	StreamBookTicker(ctx context.Context, syms []Symbol) (<-chan BookTicker, error)
	StreamDepth(ctx context.Context, syms []Symbol) (<-chan Depth, error)
	StreamUserData(ctx context.Context) (<-chan UserEvent, error) // 封装 listenKey 续期等所内细节
}

type Safety interface {
	Classify(err error) (category, severity string)  // 取代 ClassifyException 的币安码硬编码
	ReserveOrderBudget(units int) bool                // 取代 used-weight 预算
	ReleaseOrderBudget(units int)
	ArmDeadMansSwitch(ctx context.Context, countdown time.Duration) error // 不支持→本地兜底
}

// Capabilities 是「加所不重构」的关键:引擎按此裁剪行为,而非写死币安假设。
type Capabilities struct {
	SupportsNegativeCarry   bool   // 能否借币做空现货(决定该所是否做负 carry)
	FundingIntervalsHours   []int  // 支持的资金费周期 {1,4,8}
	HasNativeDeadMan        bool   // 有无交易所侧 Kill-Switch
	HasFeeDiscountToken     bool   // 类 BNB 抵扣
	MaxBatchOrders          int
	ContractBasedPerp       bool   // 永续是否按「张」而非「币数量」下单(OKX/Bybit 常见)
	SupportsCapitalRebalance bool  // 是否支持面板「自动调仓」(钱包间划转);false→面板只读快照,人工调
	CapitalWallets          []Wallet // 该所参与调仓的钱包集合(币安四钱包 / Bybit 统一账户两格 …)
}
```

### 3.1 钱包与调仓抽象(`CapitalManagement`,补 review)
现有 `runtime_capital_adjustment.go` 是**整文件币安耦合**(四钱包 `spot/um_futures/cross_margin/funding`、`MAIN_UMFUTURE` 等币安专有划转类型、25/25/50 写死)。多所必须把"钱包模型 + 调仓计划 + 执行"抽象进 venue 层,否则 P4 接第二所会卡在这里。

```go
// 提议代码 internal/venue/capital.go
type Wallet string // 该所自定义:binance: "spot"/"um_futures"/"cross_margin"/"funding";bybit: "unified"/"funding"

type WalletSnapshot struct {
	Wallet   Wallet
	Label    string  // UI 展示名(本地化)
	FreeUSDT float64
}
type CapitalAllocationPlan struct {
	TotalTradingUSDT float64
	Targets          map[Wallet]float64 // 适配器按能力生成(见分配规则)
	RemainderWallet  Wallet             // 超出交易目标的余量打入此钱包(见余量规则)
}
type Transfer struct {
	From, To   Wallet
	AssetUSDT  float64
	VenueRef   string // 该所划转单号/类型(币安 MAIN_UMFUTURE 等),仅记录用
}
// CapitalManagement 单列子接口;不支持的所返回 ErrUnsupported,引擎/面板据 Capabilities 裁剪。
type CapitalManagement interface {
	CaptureMovableSnapshot(ctx context.Context) (snaps []WalletSnapshot, totalUSDT float64, err error)
	PlanAllocation(totalUSDT float64, caps Capabilities) CapitalAllocationPlan
	ExecuteRebalance(ctx context.Context, plan CapitalAllocationPlan) ([]Transfer, error)
}
```

**分配规则(写死进文档,适配器实现)**:
- **只做所内钱包间划转,绝不跨所**(跨所是非目标)。
- `SupportsNegativeCarry=true`(币安式):`spot 25% + cross_margin 25% + um_futures 50%`(借币做空需 margin 子账户);**余量(总可动 − 交易目标)打入 `funding` 钱包**(现有币安行为)。
- `SupportsNegativeCarry=false`:`spot 50% + perp 50%`,**不需要 margin 子账户**(比例不再是 25/25/50)。**余量规则**:有独立 funding 账户的所打 funding;无 funding 账户(如 Bybit 统一账户)则**留在 spot/统一账户余额**,并要求 `target ≤ 可交易总额`(超出直接拒绝,不静默截断)。
- **单腿名义换算**:现有 `applyTradingCapitalTargetToConfig` 是 `perLeg = target / 2`(现货+合约两腿对半)→ 写入 `PerSymbolQuoteUSDT`,`MaxCycleOpenNotionalUSDT = target`。此 `/2` 在 25/25/50 与 50/50 下均成立(两腿对冲);但若某所合约按张/统一账户保证金占用不同,适配器 `PlanAllocation` 需复核 `/2` 是否仍等价,**不得照搬币安公式**。
- 先只支持 `live_funding_asset=USDT` 的所;USDC 等口径后续扩展。
- `SupportsCapitalRebalance=false`(如 Gate 钱包结构复杂可先关):面板**只显示快照、人工调**,不强行抹平。

> 现有 `RuntimeCapitalAdjustmentResult.{Before,After}` 的"四格币安快照"将泛化为 `[]WalletSnapshot`(币安四格 / Bybit 两格),`Transfers` 加 `venue` 字段;比例与划转类型下沉到 `venue/binance/capital.go`。

---

## 第 4 章 · 领域中性类型(canonical types)

引擎只认中性类型,适配器负责双向翻译。重点类型:

```go
// 提议代码 internal/venue/types.go
type Market uint8 // Spot / Perp
type Symbol string // 规范化:base-quote 大写,如 "BTC-USDT"

type Instrument struct {
	Symbol         Symbol
	Market         Market
	BaseAsset      string
	QuoteAsset     string
	TickSize       float64
	StepSize       float64
	MinNotional    float64
	PricePrecision int
	QtyPrecision   int
	ContractSize   float64 // 永续合约面值(ContractBasedPerp 时 >0,否则 1)
	QtyUnit        string  // "coin" | "contract"
}

type FundingSnapshot struct {
	Symbol               Symbol
	MarkPrice            float64
	IndexPrice           float64
	EstimatedSettlePrice float64
	LastFundingRate      float64
	NextFundingTime      time.Time
	IntervalHours        int     // 1 / 4 / 8 …逐币
	RateCap              float64 // 该所该币资金费上限(撞限豁免用)
	RateFloor            float64 // 下限
}

type CostProfile struct {
	SpotTaker, SpotMaker     float64
	FuturesTaker, FuturesMaker float64
	DiscountApplied          bool   // 是否已计入平台币抵扣
}

type OrderRequest struct {
	Market        Market
	Symbol        Symbol
	Side          Side       // Buy/Sell
	Type          OrderType  // IOC/Limit/Market/PostOnly
	Qty           float64    // 统一用「币数量」;适配器内部换算成该所张数
	Price         float64
	ClientOrderID string
	MarginEffect  MarginEffect // None/Borrow/Repay(负 carry)
	ReduceOnly    bool
}

type VenueError struct {
	Category  string // request_timeout/platform_reject/order_rate_limit/api_key_error/request_error
	Severity  string // minor/major/critical
	RawCode   string
	Retryable bool
}
```

**符号映射示例**:`BTC-USDT` → 币安/Bybit `BTCUSDT`、OKX 永续 `BTC-USDT-SWAP`、Gate `BTC_USDT`。映射逻辑在各适配器 `mapping.go`。

---

## 第 5 章 · 各所差异参数:三层模型 + 完整清单

### 5.1 三层参数模型(禁止出现第四类「全局隐含币安常量」)
1. **实拉权威值(per-venue, runtime)** —— 真相来源,优先级最高。
2. **每所配置覆盖** —— 拉不到/无该接口/需人工标定时兜底;**带来源标注,日志打印「生效值 vs 默认值」**。
3. **能力开关** —— 二元支持与否,引擎按此裁剪。

### 5.2 逐参数清单

| 类别 | 差异点 | 归类 | 落点 |
|---|---|---|---|
| 手续费 | maker/taker、现货/合约、VIP 阶梯、平台币抵扣、返佣 | 实拉 + 抵扣=能力 | `AccountData.FetchTradingCostProfile → CostProfile` |
| 资金费上下限 | cap/floor 各所不同 | 实拉 | `FundingSnapshot.RateCap/RateFloor` |
| 资金费周期/结算时刻 | 1h/4h/8h 混合、逐币不同、结算时刻各异 | 实拉 | `FundingSnapshot.IntervalHours/NextFundingTime`;调度走逐币时钟 |
| 负 carry 借币 | 利率/可借量/机制(全仓杠杆 vs 统一账户)、是否支持 | 实拉 + 能力 | `MaxBorrowable`+`BorrowRepay`+`Capabilities.SupportsNegativeCarry` |
| **合约计量单位** | 币安按币数量;OKX/Bybit 多按「张」+ 合约面值 | 实拉 + 适配器换算 | `Instrument.ContractSize/QtyUnit`;`PlaceOrder` 内换算 |
| 符号/精度/最小名义 | 命名、tick/step/minNotional/precision | 实拉 | `SymbolMapper`+`Instrument` |
| 订单类型/批量上限 | IOC/postOnly/reduceOnly 语义、batch 上限 | 能力 | `Capabilities.MaxBatchOrders` + 支持单型集 |
| 限频/权重 | used-weight(币安)vs req/s vs token 桶 | 适配器内置 | `Safety.Reserve/ReleaseOrderBudget` |
| 错误码 | `-1015/403/418/429`(币安)各所不同 | 适配器 | `Safety.Classify` |
| 鉴权/WS/Kill-Switch | 签名方式、listenKey 等价物、countdownCancelAll | 适配器 + 能力 | `signing.go`/`ws.go`/`Safety.ArmDeadMansSwitch` |
| 平台币抵扣 | BNB/OKB/无 | 能力 | `Capabilities.HasFeeDiscountToken`(费率派生时计入) |
| 计价稳定币 | USDT/USDC/FDUSD 各所不全有 | 每所配置 | `venues.<v>.preferred_spot_quote_assets` |
| 持仓模式 | 单向/双向 | 适配器 | `SetMarginMode`/初始化处理 |
| 风险分层 | 当前按币安符号写死 | 中性化/每所配置 | 接收中性输入(流动性/波动)计算,或每所配置分层表 |

### 5.3 两个反面教材(实现时必须消灭)
- `FeeReserveRate=0.0024`(币安 BNB 抵扣后)→ 改为按实拉费率派生(见第 6 章)。
- `MaxEstimatedFundingRate=0.03` → 改为按实拉 `RateCap` 派生。

**纪律**:任何 PR 里「数字常量参与经济判断」都要自问:**这个数字是不是只对币安成立?** 是 → 挪进每所 `EconomicParams`。

---

## 第 6 章 · 经济闸「每所实例化」

现在 `coherentMinFundingRate(config)`、净利门、撞限豁免都读全局 `cfg`。多所后每个 `Engine` 持有**该所自己的 `EconomicParams`**(实拉 + 该所配置派生)。

> **先对齐现有口径(grounded,实现前必读)**:
> - `coherentMinFundingRate`(`strategy_evaluator.go:48-51`)= `FeeReserveRate + ExitSpreadReserveRate + RiskConfig.MinNetProfitRate`(注意 `MinNetProfitRate` 在 **RiskConfig**)。
> - 真正的净利闸 `estimateReservedNetProfitRate`(`engine_cost_guard.go:129`)= `费率 − 入场两腿价差 − roundTripFeeRate − ExitSpreadReserveRate − **borrowReserveRate**`。
> - **借币储备 `borrowReserveRate` 已是独立运行时项**(`liveBorrowReserveRate`,按资产小时利率×周期实算),**不在 `FeeReserveRate` 里**——多所后它仍走"每所实拉借币利率"路径,`FeeReserveRate` 不要去折叠借币。

```go
// 提议代码
type EconomicParams struct {
	SpotTaker, FuturesTaker float64            // 实拉(FetchTradingCostProfile,该所抵扣已计入)
	FeeReserveRate          float64            // 派生:仅覆盖"往返 taker 手续费"= 2*SpotTaker + 2*FuturesTaker
	                                            //   (开+平 × 现货+合约 四腿;含平台币抵扣,因抵扣已在实拉费率里)
	ExitSpreadReserveRate   float64            // 该所配置
	MinNetProfitRate        float64            // 该所配置(对应 RiskConfig.MinNetProfitRate)
	MaxEstimatedRate        float64            // 由实拉 RateCap 派生(取代写死 0.03)
	FundingCapBySymbol      map[Symbol]CapFloor // 实拉,撞限豁免用
	// 注:borrowReserveRate 不在此结构——它每币每刻实时算(liveBorrowReserveRate),按所实拉小时利率即可。
}
func (p EconomicParams) BreakEven() float64 {
	return p.FeeReserveRate + p.ExitSpreadReserveRate + p.MinNetProfitRate
}
```

- 策略/风控算法**保持 venue-neutral**(只吃数值),但喂进去的数值来自「当前所」的 `EconomicParams`。
- 同一套 `EvaluateScanCandidate`/撞限豁免,在 4 个所跑出 4 套正确闸值,**零分叉**。
- 你刚加的撞限豁免天然受益:cap/floor 来自每所真实 `FundingSnapshot.RateCap/RateFloor`。
- ⚠️ **等价性验收(P2 必须)**:用实拉费率派生 `FeeReserveRate` 替换手填常量后,币安的 `coherentMinFundingRate` 与改造前差值必须 `< ε`(加单测锚定,见 P2)。否则进场闸数值漂移、行为不等价。平台币抵扣口径以 `CostProfile.DiscountApplied` 为准,避免重复或漏算抵扣。

### 6.1 `MaxEstimatedRate` 派生口径(明确,避免实现者猜)
scan 阶段现在用**全局** `MaxEstimatedFundingRate`(过滤异常高费率),而交易所 cap 是**逐币**的。两种实现路:
- 方案 A:`MaxEstimatedRate = max(所有扫描候选的 RateCap)` 作全局上界。
- **方案 B(选这个)**:`EconomicParams.MaxEstimatedRate` 保留**该所全局默认**(由该所典型 cap 派生或配置兜底)做"异常高费率"粗过滤;**逐币精确过滤交给 `FundingSnapshot.RateCap`**——撞限豁免逻辑已逐币读 cap/floor,精度由它保证。

> 选 B 的理由:不破坏现有"全局上界粗筛 + 逐币撞限豁免精筛"的两层结构,改动面最小;`MaxEstimatedRate` 从写死 `0.03` 改为"该所默认 cap 派生值"即可,逐币正确性已由 `FundingSnapshot.RateCap` 兜住。

---

## 第 7 章 · 多引擎编排、控制面与隔离

> ⚠️ **本章是文档最大的实现缺口(review 指出),其工作量不亚于写一个适配器。** 当前控制面是严格单例:一个 `RuntimeController` → 一个 live engine goroutine;一个 `DashboardServer` 绑这一个 controller + 一个 `ReportStore`;启停/调资本 API 全是全局单实例。多所必须把这一整套"控制面"也 venue 化。

### 7.1 编排器与控制面拓扑
```go
// 提议代码 internal/app/orchestrator.go
type Orchestrator struct {
	venues map[string]*venueRuntime // key=venue name
}
type venueRuntime struct {
	venue      venue.Venue
	engine     *FundingRateSniperEngine
	controller *RuntimeController   // 每所一个(非全局单例)
	store      *ReportStore         // 每所一个,根目录 records/<venue>/
	cancel     context.CancelFunc   // 单独停该所用
}
```
**拓扑决策**:采用 **1 个 Orchestrator + N 个 venue-scoped Controller/Store**,而不是让单个 Controller 内部 venue-aware(后者会把全局锁/状态又揉成上帝对象)。Dashboard 升级为持有 `Orchestrator`(而非单个 controller),按 `?venue=` 路由到对应 `venueRuntime`。

### 7.2 每所隔离清单(逐项落实,否则"互不影响"打折)
- `records/<venue>/`:**独立** ReportStore 根目录、单写租约 `.lingxi-live.lock`、风控状态(连亏/回撤/日级预算 `live_daily_risk_state.json`)、结算/监控/事件记录。**必须拆 `engine.reportStore = c.store` 的共用**。
- 每所**独立** `RuntimeController` + 独立熔断 + 独立 dead-man + 独立 `EconomicParams`。
- 一所 panic/fatal **只停该所引擎**:`Orchestrator` 为每所起独立 goroutine 跑 `Start(venueCtx)`,**每个 goroutine 自带 recover**,绝不让一所未捕获 panic 掀翻整个进程。

### 7.3 venue 维度的控制 API(面板"停 OKX、币安继续")
所有控制/读 API 加 `venue` 参数,路由到对应 `venueRuntime`;另保留 `venue=all` 做聚合。**完整清单(逐个核对现有面板,全部要 venue 化)**:

| 现有 API | 多所化要求 |
|---|---|
| `POST /api/runtime/{stop,start}` | 加 `?venue=`;缺省/`all` 写操作**显式拒绝**,全局走 `?venue=all&confirm=1` |
| `GET/POST /api/runtime/config` | per-venue cfg(写入该所 Controller) |
| `POST /api/runtime/config/{preview,rollback}` | per-venue 历史栈 |
| **`POST /api/runtime/capital`** | **加 `?venue=`(见 §7.8,最大遗漏)** |
| `POST /api/runtime/clear-{logs,data,cache,all}` | `?venue=` + 全局 `?venue=all&confirm=1`;`clear-all` 毁灭性,强制 confirm |
| 下载链接(settlements/monitor/…) | 走 `records/<venue>/` 分路径 |
| 审计日志 `dashboard_control_audit.jsonl` | **首选单文件 + 每条加 `venue` 字段**(「全部」Tab 查聚合更方便);分目录为备选 |

### 7.4 双级紧急停机(global + per-venue)
现在 `EmergencyStopFile` 默认 `records/.halt-live`,一所触发会阻断**整个进程**启动(`runtime_startup_checks.go`)。多所改为两级:
- **全局 halt**:`records/.halt-live` → 一键停全部(运维总闸)。
- **单所 halt**:`records/<venue>/.halt-live` → 只停该所。
- 启动检查与运行期守卫都要**先看全局、再看单所**;单所 halt 绝不阻塞其它所。

### 7.5 启动隔离语义(review 补充)
- 某所 **API key 无效 / 连接失败 / 冷启动检查不过** → 该所标记 `disabled` + 告警,**其它所照常 Start**;Orchestrator 不因一所失败而整体退出。
- 启动日志逐所打印「该所生效配置 vs 默认值」(配合 `BindConfig` 已有的来源标注),防全局默认写错四家一起错。

### 7.6 进程模型
起步用**单进程多引擎**(部署简单,已确认无包级全局状态);若极在意"一所挂绝不影响其它所",**最终形态是每所一进程**(同一二进制 `lingxi --venue=okx`),单进程四引擎只是起步妥协。见第 13 章进程级耦合。

### 7.7 引擎改造
`engine_types.go` 的 `client *BinanceClient` → `venue venue.Venue`;删除引擎内所有币安 URL 拼接,改调 `e.venue.XXX(...)`;`binance_gateway.go` 的影子拦截/used-weight/熔断计数保留为引擎侧通用中间件,但「怎么签、打哪个 URL」交给适配器。影子拦截上移到 `Trading.PlaceOrder` 层(更干净,天然覆盖所有所,且支持**一所 shadow、一所 live 的混合运维**)。

### 7.8 每所仓位调整(调仓)— review 标记的最大遗漏 ⭐
v2 完全没写调仓,而现有 `runtime_capital_adjustment.go` 是整文件币安耦合 + 全局单实例。这是与 §3.1 钱包抽象、P4 各所账户结构强绑的**核心控制面能力**,必须在文档里规划,否则 P4 接第二所会在调仓卡死。

**维度对照(现状 → 目标)**:

| 维度 | 现状 | 目标 |
|---|---|---|
| API | `POST /api/runtime/capital`(无 venue) | `POST /api/runtime/capital?venue=binance` |
| 请求体 | `{secret_path, mode, target_trading_usdt}` | `{target_trading_usdt}`;secret 由 Orchestrator 按 venue 自动绑,不再手填 `secret_path` |
| 快照 | `RuntimeCapitalSnapshot` 四格币安钱包 | `[]WalletSnapshot`(币安四格 / Bybit 两格,见 §3.1) |
| 分配比例 | 25/25/50 写死 | 按 `Capabilities.SupportsNegativeCarry` 切换(true→25/25/50;false→50/50) |
| 配置联动 | 改全局 `PerSymbolQuoteUSDT`+`MaxCycleOpenNotionalUSDT` | 写入**该所** Controller 的 cfg |
| 运行约束 | 全局 `c.status.Running` 即拒绝 | **仅该 venue running 时拒绝**,其它所可同时调 |
| UI | 控制台顶部单输入框 | 切到哪个 venue Tab,就调哪个所 |
| localStorage | 单 key `lingxi-trading-capital-usdt` | per-venue:`lingxi-trading-capital-usdt:binance` 等 |

**UI 行为(写死产品形态)**:
- 顶部 Tab `[全部][币安][OKX][Gate][Bybit]`。
- 在「币安」Tab:目标交易仓位输入框 + 四格钱包快照 + 「应用并调仓」→ `POST /api/runtime/capital?venue=binance`,**只动币安**。
- 在「全部」Tab:各所仓位摘要表(目标/实际/差额),**不提供「一键调全部」**(太易误操作);要调则逐行确认。

**API 行为**:`POST /api/runtime/capital?venue=binance`,Body `{ "target_trading_usdt": 100 }`;响应仍是 `RuntimeCapitalAdjustmentResult`,但 `Before/After` 是该所钱包结构、`Config` 返回该所 `RuntimeRiskConfig`、`Transfers` 带 `venue` 字段。

**与 `allocated_capital_usdt` 的关系(承接 §9.1)**:

| 字段 | 用途 | 调仓是否自动 sync |
|---|---|---|
| `target_trading_usdt`(面板调仓) | 真实下单规模 → 改 `PerSymbolQuoteUSDT` | 是 |
| `allocated_capital_usdt`(配置) | 绩效 Sharpe/回撤分母 | **否**(可选未来增强:调仓成功后 UI 提示「是否同步 allocated_capital」) |

### 7.9 Orchestrator 启动语义 + 面板「启动任务」改造(定稿,避免 P3 UI rework)
- **进程启动**:Orchestrator 自动为所有 `enabled: true` 的所建 `venueRuntime`,但**默认 idle、不自动下单**;由面板各 Tab **手动 Start**(与现有"先看再启"习惯一致,且配合启动隔离 §7.5——某所失败只置 disabled)。是否"自动 Start"可由 `venues.<v>.autostart` 开关控制,默认 false。
- **mode(实盘 / 影子)= per-venue 配置项**,走 `venues.<v>.execution.shadow_trading`,**不再是面板上手填的全局 mode-select**。
- **现有「启动任务」按钮(mode-select + secret-path)的去向**:secret 由 Orchestrator 按 venue 自动绑(§7.8 去掉手填 `secret_path`),mode 来自该所配置;按钮退化为各 Tab 内的 **Start/Stop**(只控该所)。「全部」Tab 不提供"一键启动全部"写操作(同 §7.3 误操作防护)。
- **历史模拟 / backtest / `reconcile`**:同样 per-venue(`reconcile --venue=`、回测按所),不混所。

---

## 第 8 章 · 配置与密钥模型

> ✅ **现状更正(review)**:`config.yaml` **已是真 YAML**——`config_yaml.go` 用 `gopkg.in/yaml.v3` 解析嵌套,`config_loader.go`+`core.BindConfig` 按 cfg tag 自动绑定(M-S1.3,已消除 R24 手工白名单)。所以**不是"迁移 YAML 库"**,而是 **扩展 schema**:① 增加 `venues:` 多层级并让 `BindConfig` 支持 per-venue 覆盖;② `secret.yaml` 从单所 flat 结构改为每所一节。`engine_config.go` 里残留的旧 `SplitN(line,":",2)` 扁平兜底可在确认无引用后清理。
>
> **实现路径(P3 明确选 B,避免实现者猜)**:当前 `flattenYAMLConfig` 把 YAML 拍平成 `section.leaf` 扁平键。多所有两条路——
> - A:继续 flatten,键名形如 `venues.binance.strategy.per_symbol_quote_usdt`;
> - **B(推荐)**:`venues` 单独 `unmarshal` 成 `map[string]VenueConfig`,再与全局 default merge。
>
> 选 **B**:结构清晰,`allocated_capital_usdt` 等 per-venue 字段更自然,per-venue 覆盖/默认合并逻辑集中可测。全局非 venue 段仍走现有 flatten+BindConfig。

```yaml
# config.yaml —— 全局默认 + 每所覆盖(per-venue 可覆盖 strategy/risk/execution/portfolio/active_profile)
venues:
  binance:
    enabled: true
    allocated_capital_usdt: 500        # 绩效指标分母(见第 9 章)
    preferred_spot_quote_assets: "FDUSD,USDC,USDT"
    active_profile: "live-small"       # per-venue 生效档(可不同所不同档)
    strategy: { min_estimated_funding_rate: 0.0034 }
    risk:     { min_net_profit_rate: 0.0005 }
    execution: { shadow_trading: false }            # 该所 live
    portfolio: { enable_rolling_hold: true }        # per-venue 滚动持有开关
  okx:
    enabled: true
    allocated_capital_usdt: 500
    strategy: { min_estimated_funding_rate: 0.0040 }   # 各所边际不同
    execution: { shadow_trading: true }             # 「一所 shadow、一所 live」核心运维场景
  gate:  { enabled: false }
  bybit: { enabled: true, allocated_capital_usdt: 300 }
```

```yaml
# secret.yaml —— 每所独立密钥(字段各所不同:OKX 多 passphrase;proxy_url 可选 per-venue)
binance: { api: ..., secret: ..., proxy_url: "" }              # proxy_url 对应 SecretConfig.ProxyURL(exchange/http.go),可选
okx:     { api: ..., secret: ..., passphrase: ..., proxy_url: "" }
bybit:   { api: ..., secret: ..., proxy_url: "" }
gate:    { api: ..., secret: ..., proxy_url: "" }
```
> `proxy_url` 呼应 §13.1「极端情况 per-venue 代理」——每所可独立走不同出口,某所 IP 被 ban 不影响他所。

---

## 第 9 章 · 面板多所化 + 绩效指标(口径已锁定)

### 9.1 已锁定口径
- **收益率分母 = 每所分配本金** `venues.<v>.allocated_capital_usdt`。
- **Alpha/Beta 基准 = BTC 现货日收益**;**数据源固定走 Binance spot `BTCUSDT` 日 K**(公共、流动性最好、与被测 venue 无关)。`performance.go` 注明:Benchmark **不是**被测 venue 自身行情(避免"OKX 所算 Alpha 却用 Bybit K 线"的混乱);Binance 不可用时,该次 Alpha/Beta 标 `Insufficient`(不静默换源)。**即使 `binance.enabled=false`,也允许只读拉取该公共 K 线**(公共行情无需交易 key),保证未开币安所时组合 Alpha/Beta 仍可算。
- 无风险利率默认 **0**;年化按 **365**(crypto 7×24);重采样**按 UTC 日**。

> ⚠️ **`allocated_capital_usdt` 与真实下单规模的关系(review 必读)**:它**只做绩效分母**,不限制下单——实际下单规模由 `PerSymbolQuoteUSDT`(`engine_open_exec.go`)决定。两者**口径必须自己对齐**:
> - 配 500 但账户实放 2000 → 收益率被**高估**;配 500 但实放 200 → 被**低估**。
> - 约定:`allocated_capital_usdt = 你主观认定投入该所策略的资金,应与实际 deploy 规模一致`;UI 上标注该字段含义。
> - 可选增强:未来加「自动 sync = 账户可用 USDT 快照」,避免人工漂移。

### 9.2 数据源
绩效序列源头 = `records/<venue>/settlements.json` 的 `SettlementCSVRecord`(`report_records.go:127`),字段 `NetPnL`、`FundingSettledAt`、`FundingIncomePending`、`RecordedAt`、`CycleID`。

> ⚠️ **`SettlementCSVRecord` 当前无 `venue` 字段**(已核实)。`records/<venue>/` 分目录后,读盘能按目录区分;但 `GET /api/dashboard/summary?venue=all` 聚合、组合堆叠图、审计、reconcile 输出都需要逐条带 venue 标签。**做法:不改历史落盘格式,在 API 响应层按来源目录注入 `venue` 字段**(向后兼容)。

### 9.3 数据管道
1. **取已结算**:只保留 `FundingIncomePending=false`(最终 NetPnL,资金费已到账)。
2. **按日重采样**:按 `FundingSettledAt` 的 UTC 日聚合 `NetPnL` 之和 = `pnl_t`;**首末活动日之间空白日补 0**。
3. **日收益率**:`r_t = pnl_t / AllocatedCapital_venue`;`equity_t = AllocatedCapital + Σ_{i≤t} pnl_i`。
4. **组合(全部)**:`pnl_t^port = Σ_venue pnl_t`,分母 `Σ AllocatedCapital`,在组合曲线上独立算(不平均各所比率)。

### 9.4 指标公式(放 `internal/metrics/performance.go`,纯函数可单测)
记 `r=[r_t]`,`N=len(r)`,`rf`=无风险日利率(默认 0)。
```
mean     = Σr_t / N
std      = sqrt( Σ(r_t-mean)² / (N-1) )
downside = sqrt( Σ min(r_t-rf,0)² / N )
AnnualizedReturn = ( Π(1+r_t) )^(365/N) − 1
MaxDrawdown(USDT)= max_t( peak_t − equity_t )      # peak_t=前缀最大权益
MaxDrawdownRate  = max_t( (peak_t − equity_t)/peak_t )
Sharpe  = (mean − rf) / std      × √365
Sortino = (mean − rf) / downside × √365
Calmar  = AnnualizedReturn / |MaxDrawdownRate|
# 基准对齐同一批 UTC 日的 BTC 日收益 b_t:
Beta  = Cov(r,b) / Var(b)
Alpha = (1 + (mean(r) − Beta·mean(b)))^365 − 1
# 胜率(锁定口径,避免 UI/单测各算各的):
WinRate = count(r_t > 0) / N           # 日胜率:按 UTC 日,与其它日度指标同一序列
```
> **`WinRate` 口径锁定**:用**日胜率**`count(r_t>0)/N`(与 Sharpe/Sortino 等共用同一日度收益序列,口径统一)。**不**用"按 `SettlementCSVRecord` 条数的周期胜率"——后者与日度指标分母不一致,易混淆。

```go
// 提议代码
type PerformanceMetrics struct {
	StartEquity, EndEquity float64
	TotalReturn, AnnualizedReturn float64
	MaxDrawdown, MaxDrawdownRate   float64
	Sharpe, Sortino, Calmar        float64
	Alpha, Beta                    float64
	WinRate                        float64
	Samples                        int
	Insufficient                   bool
	RiskFree, AnnualFactor         float64
	BenchmarkSymbol                string
}
```

### 9.5 正确性铁律(必须实现)
- **只用资金费已到账的最终 NetPnL**(`FundingIncomePending=false`),否则序列抖动、双计。
- `N<2` → 比率无定义,返回 0 + `Insufficient=true`;`std==0`/`downside==0` → UI 显示 `—`。
- 样本少(<30 日)→ 年化比率打「参考值,样本不足」;**UI 默认突出 MaxDD / 胜率 / Sortino**(carry 收益非正态,Sharpe 易被小波动夸大,小样本误导性强)。
- **Beta≈0 是 delta 中性的预期**;实测 Beta 显著≠0 → 高亮为「对冲漏了/有方向暴露」风险信号。
- **各所结算时刻不同的日对齐**:币安 8h、OKX 1h 等结算周期不一致,`FundingSettledAt` 时区/时刻各异。统一**按 UTC 日聚合**为默认口径,但需在 UI 注明"按 UTC 日切分,跨所同日对齐",避免误读各所"当地结算日"。

### 9.6 API 与 UI
- `GET /api/dashboard/summary?venue={binance|...|all}`:单所运营 / 总览。
- `GET /api/performance?venue={...|all}&since=&until=` → `PerformanceMetrics` + 权益曲线点 + 水下回撤点。
- UI:顶部 venue Tab(`全部 | 币安 | OKX | Gate | Bybit`,每所状态灯);单所页=运营细节+绩效卡片;`全部`页=各所状态表+组合权益曲线+水下回撤+各所净利贡献堆叠图。前端沿用现有 `webpanel/index.html` 风格,轻量 canvas/SVG 画图,无新依赖。
- **runtime 状态区(Run ID / Cycle ID / 当前阶段)也随 Tab 切 per-venue**——每所有自己的 Run/Cycle/阶段。
- **单端口、单令牌**:`NetworkConfig.DashboardAddr` 仍是**一个端口**服务所有 venue Tab(无需每所一端口);控制令牌 `.dashboard_control_token` 放 `records/` 根**全局一份**(面板是统一入口,令牌鉴权与具体所无关)。venue 隔离体现在 API 的 `?venue=` 路由,而非端口/令牌。

---

## 第 10 章 · 现有文件去向映射

| 现有 | 去向 |
|---|---|
| `internal/exchange/*`(client/http/transport/symbols/cache) | → `internal/venue/binance/`(适配器内核) |
| `exchange.go` 的 `Sign` | → `venue/binance/signing.go` |
| `exchange.go` 的 `ClassifyException` | → 各所 `Safety.Classify` |
| `binance_gateway.go` | 拆:通用中间件(影子/used-weight/熔断计数)留引擎;签名/URL → 适配器 |
| `engine_settlement.go`/`engine_open_exec.go`/`engine_fees_market.go` 等端点拼接 | → 对应 `Trading`/`AccountData`/`MarketData` 适配器方法 |
| `user_data_stream.go`(listenKey) | → `venue/binance/ws.go` |
| `dead_mans_switch.go`(countdownCancelAll) | → `venue/binance` 的 `Safety.ArmDeadMansSwitch` |
| `runtime_capital_adjustment.go`(四钱包/25-25-50/MAIN_UMFUTURE) | P3 整文件搬进 per-venue Controller 调用;P4 拆进 `venue/binance/capital.go` 实现 `CapitalManagement` |
| `engine_types.go` 的 `client *BinanceClient` | → `venue venue.Venue` |
| `run.go` 单引擎装配 | → `orchestrator.go` 多引擎 |
| `internal/core` 配置 | **扩展** schema(已是 yaml.v3):加 `venues:` 多层级 + per-venue 覆盖 |
| `internal/metrics` | 新增 `performance.go`(绩效指标) |

每个 `internal/venue/<vendor>/` 内部结构(可抄模板):
```
adapter.go       实现 venue.Venue(组合下列文件)
signing.go       该所签名
rest.go          REST 端点 + 请求(币安由 internal/exchange 迁入)
ws.go            WS 流 + 心跳 + listenKey 等价物
mapping.go       Symbol/Instrument/Order/Error 翻译
capabilities.go  该所能力声明
capital.go       实现 CapitalManagement:钱包快照 + PlanAllocation + ExecuteRebalance(币安由 runtime_capital_adjustment.go 迁入)
```

---

## 第 11 章 · 分阶段落地路线(strangler-fig,每阶段币安不停)

> 每阶段结束都要 `go build ./... && make vet && make test && make race` 全绿,且**币安行为零变化**才进下一阶段。前 3 阶段是「纯重构不加所」,却是「以后加所不再重构」的全部价值所在。

### P0 · 抽接口、零行为变更 ⭐(保护已运行的币安)
- 新建 `internal/venue` 包:定义 `Venue` 接口族 + 中性类型。
- 写 `venue/binance` 适配器,**内部包住现有 `*BinanceClient`**,逐方法委托。
- 引擎改为依赖 `venue.Venue` 接口,但跑的还是币安、行为逐字节一致。
- **验收**:实盘/影子/对账与改造前输出一致;全套测试 + race 绿。

### P1 · 下沉端点知识
- 把 `internal/app` 里所有币安 URL/签名拼接搬进 binance 适配器;引擎不再拼 URL。
- 影子拦截上移到 `Trading.PlaceOrder`。
- **验收**:引擎层无任何 `/fapi`/`/api/v3`/`/sapi` 字面量(可用 grep 钉死);行为不变。

### P2 · 归一化领域模型 + 经济参数实例化
- 引擎全面改说中性 `Symbol`/`FundingSnapshot`/`OrderRequest`;符号映射进适配器。
- 落地 `EconomicParams`:`FeeReserveRate`/`MaxEstimatedRate` 改为实拉派生(借币储备保持独立运行时项,不折叠);撞限豁免 cap/floor 走 `FundingSnapshot`。`MaxEstimatedRate` 派生口径见 §6.1。
- **`symbolRiskProfile` 中性化(与符号中性化同 PR)**:现 `majorRiskTierSymbols`(`engine_types.go:49`)按币安原始符号 `"BTCUSDT"` 写死,`GetSymbolRiskProfile(item.Symbol)` 在 P2 改 canonical `BTC-USDT` 后会失效。**分层键改为 base asset(`BTC`/`ETH`)或 canonical `Symbol`**;验收并入 P2(不留到 P3)。
- **调度退役全局 `WakeMinute`(范围比想象大,见下框)**——这是多所能跑对的前置,四所结算周期不同(OKX 1h / 币安 8h),全局 `WakeMinute=57` 在多所下本就不成立。

> ⚠️ **调度现状(已核实,比"一个常量"复杂)**:`funding_sniper_engine.go:145-151` 按 `PortfolioConfig.EnableRollingHold` 分叉——
> - `true` → `NextPortfolioWake`(逐币 funding 时钟);但它**仍用 `NextWake`(全局 :57)做新开兜底**(`scheduler.go:95`)。
> - `false`(**当前生产默认**,`defaults.go`)→ `NextWake`,即全局单点 `:57` 新开唤醒。
>
> **P2 目标调度模型(写死,替代 `WakeMinute`)**:
> 1. **新开扫描**:按「全市场 `min(NextFundingTime) − ScanWindow`」唤醒,**不再绑 `:57`**。
> 2. **续持复核**:沿用现有 `RevalidateAt`(:55 窗口),保留。
> 3. **无持仓时**:退化为「下一次最近结算 − ScanWindow」,而非固定分钟。
>
> ⚠️ **这是有意的行为变更,不是"零变化"**——会与"币安行为零变化"的总原则冲突。P2 必须**显式标注此项为受控行为变更**,并加**改造前后对照测试**:① 机会捕获率(同一历史窗口下命中候选数);② 唤醒时刻分布。两者在可接受阈值内才算通过。
- **验收**:① 经济闸数值由「当前所」参数驱动;② 币安 `coherentMinFundingRate` 与改造前 **diff < ε**(派生公式加**单测锚定**,对照 `strategy_evaluator.go` 旧值);③ 调度不再引用全局 `WakeMinute`(grep 钉死);④ 平台币抵扣以 `CostProfile.DiscountApplied` 计,不重复扣;⑤ **逐币时钟在 P2 末的单引擎(仍单所)下即完成切换并验收**——不依赖 P3 多引擎,降低 P2/P3 耦合风险。

### P3 · 配置/编排/控制面多所化(仍只有币安适配器)
- **扩展 YAML schema**(非迁移库):加 `venues:` 多层级 + 每所 secret 节(见第 8 章)。
- `Orchestrator` 多引擎 + 每所隔离:**独立 `RuntimeController` + 独立 `ReportStore`(`records/<venue>/`,拆共用 store)+ 单写租约 + 风控状态**(第 7 章)。
- **控制面 venue 化**:`/api/runtime/{stop,start,config}?venue=` + `venue=all`;**双级 halt**(全局 `.halt-live` + `records/<venue>/.halt-live`);**启动隔离**(一所 key/连接失败 → 该所 disabled + 告警,其它所照常 Start)。
- 面板加 venue 维度 + 绩效指标(第 9 章);先只挂币安一所验证。
- **调仓 venue 化(P3 只做路由,不抽象)**:`POST /api/runtime/capital?venue=` + 面板 Tab 内独立调仓(localStorage per-venue);运行约束改为**仅该 venue running 时拒绝**(见 §7.8)。**P3 阶段币安仍直接复用现有 `runtime_capital_adjustment.go`**——整文件搬到 per-venue Controller 调用即可,**不在 P3 拆抽象**。`CapitalManagement` 接口的抽象与币安逻辑下沉到 `venue/binance/capital.go` 留到 **P1 末期 / P4**(接 Bybit 时必须有),这样 P3 不会卡在抽象上。
- **`SettlementCSVRecord` venue 注入**(API 响应层)+ `summary?venue=all` 聚合标签(见 §9.2)。
- **YAML schema 走 path B**(`venues` struct unmarshal,见 §8)。
- **验收**:① 币安跑在新多引擎路径上,shadow+reconcile 与旧路径平价;② 面板单所+总览正常;③ 模拟「停 OKX 占位、币安继续」与「单所 halt 不影响他所」;④ 故意配错一所 key,验证其它所仍 Start;⑤ 币安调仓经 `?venue=binance` 走通,localStorage 按所隔离。

### P4 · 接第二家(建议 Bybit)
- 实现 `venue/bybit` → 过适配器一致性测试套件(第 12 章)→ shadow 跑若干周期 → 极小额实盘 → reconcile。
- **调仓抽象落地**:`CapitalManagement`(§3.1)定型;币安现有逻辑迁入 `venue/binance/capital.go`;实现 Bybit 统一账户的 `PlanAllocation`(spot/perp 分配),过 conformance 的 wallet-plan 用例。`SupportsNegativeCarry=false` 的所用 50/50 分配。
- **验收**:Bybit 一致性测试全过(含 wallet plan);小额实盘 reconcile HEALTHY;币安不受影响;Bybit 调仓在面板 `?venue=bybit` 走通。

### P5 · 铺 OKX、Gate
- 重复 P4 模板。注意 OKX 签名(passphrase+base64)、`-SWAP` 符号、合约张数;Gate 细节坑多放最后。

---

## 第 12 章 · 测试策略

- **适配器一致性套件(conformance suite)**:一套 venue-neutral 表驱动测试,**任何新适配器都必须过**(对录制的/mock server 回放):符号映射往返、下单幂等、错误分类、`FundingSnapshot` 字段完整、合约张数换算、能力声明与实际一致。「加一家 = 写适配器 + 过同一套测试」。
- **`CapitalManagement` conformance**:`PlanAllocation` 在 `SupportsNegativeCarry` 两种取值下产出正确比例(25/25/50 vs 50/50)与余量归属;`ExecuteRebalance` **幂等**(重复调用不重复划转)+ **部分失败回滚/可重入**(中途某腿失败,状态可恢复、不留半成品)。
- **绩效指标单测**:构造已知答案的权益序列验证每个比率;含 Beta≈0 的中性序列用例、空白补零、`Insufficient` 边界。
- **经济等价性单测(P2 锚)**:固定一组币安实拉费率,断言派生 `FeeReserveRate`/`coherentMinFundingRate` 与改造前手填值 diff < ε。
- 纯逻辑包测试不变(已脱网)。
- 每所上线流程:`shadow`(咽喉点拦截下单)→ 极小额实盘 → `reconcile` 验证按模型赚钱 → 再放量。

### 12.1 多所运维可观测性(review 补充)
- **Prometheus `/metrics` 加 `venue` label**(现为全局);四家并行时按所拆指标。
- **日志/trace 加 `venue` 维度**(排障四所并行时必需:每条结构化日志带 `venue=` 字段)。
- **`reconcile` 命令 per-venue**(现按单一 `RecordDir`):`reconcile --venue=okx` 只对账该所。
- **`shadow` 模式 per-venue**:支持「一所 shadow、一所 live」混合运维(影子拦截已上移到 `Trading.PlaceOrder`,天然支持)。
- **启动语义**:一所 API key 无效/连接失败 → 该所 `disabled` + 告警,**不阻塞其它所**;启动日志逐所打印生效配置。

---

## 第 13 章 · 风险与非目标

- **最大难点:负 carry 借币机制**。币安全仓杠杆 `borrow-repay` 的等价物在 OKX(统一账户)/Bybit/Gate 各异,某些所可能做不了 → 用 `Capabilities.SupportsNegativeCarry` 兜底,做不了就只做正 carry,**不要强行抹平**。
- **合约张数**:OKX/Bybit 永续按张 + 合约面值,`normalizeQuantity` 当前假设币数量 → 必须在适配器换算,否则名义对不上、对冲腿裸露。
- **引擎是上帝对象(R10/R21)**:P0 务必只换 IO 边界、不动状态机,警惕隐藏字段耦合。
- **运维面 ×4**:4 套密钥/风控/告警/独立宕机 → 隔离设计先行。
- **非目标**:跨所对冲/套利不在范围;多所=并行多个独立单所引擎,腿不跨所。

### 13.1 交易层 vs 进程层耦合(review 关键结论)
**交易/仓位/资金层面:设计上四家互不影响 ✅** —— 各所独立 API 账户、独立钱包、腿不跨所;按本文档实现后,一家连亏熔断 / 一家限频 / 一家 dead-man 触发,都**不停其它家**。这是本方案最强的隔离保证。

**进程/运维层面:单进程多引擎仍有间接耦合 ⚠️**(非"A 所仓位影响 B 所",而是同进程共享资源/操作面):

| 耦合点 | 影响 | 缓解 |
|---|---|---|
| 进程级崩溃(OOM / 未 recover 的 panic) | 四所全停 | 每 engine goroutine recover;内存/连接池上限;最终拆进程 |
| 结算时刻叠加 | 四所都在整点附近扫币/下单,CPU/带宽/代理打满 | 独立 HTTP client;代理带宽预留;限并发 |
| 共享出口 IP | 极端情况某 IP 被一家 ban | 监控各所 `Safety.Classify`;必要时 per-venue 代理 |
| 全局 emergency stop | 一所运维误触全停 | **双级 halt**(§7.4) |
| 面板/Controller 单例 | 启停/调参误操作影响全局 | **Orchestrator + venue-scoped API**(§7.3) |
| 配置错误 | 全局默认写错四家一起错 | 每所 strategy/risk 覆盖 + 启动日志打印生效值(§7.5) |

> **结论**:若极在意"一所挂绝不影响其它所",最终形态应是**每所一进程**(`lingxi --venue=okx`);单进程四引擎是部署起步妥协。资金不会自动跨所搬运(非目标),每所放多少 USDT、是否与 `allocated_capital_usdt` 一致由你自管(只影响指标,不限制下单)。

### 13.2 残留「全局绑单所」清单及其分类(review 第 7 点核实后)
review 列了若干"仍绑单所"的模块。核实代码后**分两类处理,避免高估工作量**:

**(甲)随 per-engine cfg 自动 per-venue —— 无需单独改造**(每个 engine 已持有自己的 `cfg`):
- `capital_ramp.go` 灰度爬坡:读 `e.cfg.PortfolioConfig`,引擎实例化后天然每所一份。
- `EngineGear` / `ActiveProfile`:若为引擎字段,同理随实例隔离。
- → 只需在**展示/控制层**让面板能选所查看其 ramp/gear 状态。

**(乙)真正需要显式改的残留(全局文件/单所符号表)**:
- 审计日志 `dashboard_control_audit.jsonl`:**首选保持单文件 + 每条加 `venue` 字段**(聚合查询方便);`records/<venue>/` 分目录为备选。
- `symbolRiskProfile`(`engine_risk_util.go`)币安符号写死 → 中性化或每所配置分层表(§5.2,P2 补验收项)。
- 「启动任务」单 `secret-path`+`mode` → 由 Orchestrator 按 venue 自动绑 secret,不再手填路径。
- 「一键删除所有记录」`clear-all` 全局毁灭性 → per-venue + 全局两级,全局强制 confirm(§7.3)。

> 一句话:ramp/gear 这类"读 cfg 的逻辑"是 per-engine 的免费午餐;真正要动手的是**全局单文件路径**和**币安写死的符号表**。

---

## 第 14 章 · 第二家选型建议

| 候选 | API 现代度 | 负 carry | 建议 |
|---|---|---|---|
| **Bybit** | v5 统一、文档好、WS 规整 | 统一账户技术上可借,**首版不做** | **先接**,工程摩擦最小 |
| **OKX** | v5 成熟,签名要 passphrase、符号 `-SWAP`、按张 | 统一账户强 | 次选 |
| **Gate** | 相对小众、细节坑多 | 视品种 | 最后接 |

> **Bybit 首版负 carry 决策(消除 §14 与 P4 矛盾)**:Bybit 统一账户**技术上**支持借币,但**首版工程上只做正 carry**(`SupportsNegativeCarry=false`),负 carry 留二期。因此 P4 调仓用 **50/50**(spot/perp)与此一致——不矛盾。二期开 Bybit 负 carry 时,再把分配规则改成 Bybit 统一账户的借币占用口径,并置 `SupportsNegativeCarry=true`。

---

## 附录 · 落地检查清单(逐项打勾)

- [x] P0:`internal/venue` 接口族 + 中性类型定义完成 ✅(`internal/venue/{types,venue}.go` + 单测;`go build/vet/test` 全绿)
- [x] P0:`venue/binance` 适配器包住现有 client,引擎依赖接口,行为零变化,race 绿 ✅
  - `internal/venue/binance/{adapter,mapping,marketdata,account,safety,pending}.go`:实现完整 `venue.Venue`(编译期 `var _ venue.Venue` 断言)。可直接委托的能力域(符号映射/`FetchInstruments`/`FetchFundingSnapshot`/`ServerTime`/`FetchTradingCostProfile`/`MaxBorrowable`/used-weight 预算)已逐方法委托并翻译为中性类型;Trading/Streaming/持仓余额/调仓等仍在 app 层的逻辑返回 `ErrUnsupported`,标注 P1/P4 下沉。
  - 引擎接缝:`FundingRateSniperEngine` 新增 `venueAdapter venue.Venue` 字段(包住**同一个** `*BinanceClient` 指针,零额外状态)+ `Venue()` 访问器;**未改任何 `e.client.XXX` 调用点**,故行为逐字节一致。
  - 验证:`go build ./... && go vet ./...` 全绿;`go test ./...` 全绿;`go test -race ./internal/app/... ./internal/venue/...` 全绿。
  - 注:引擎内 `e.client.XXX` → `e.venueAdapter.XXX` 的**逐调用点迁移属于 P1**(下沉端点知识),P0 只建接缝、不动调用点。
- [~] P1:引擎层无交易所 URL 字面量(grep 钉死),影子拦截上移到 `PlaceOrder`
  - [x] P1a:端点路径收敛为单一事实源 `internal/venue/binance/endpoints.go`(全部 `Path*` 常量);引擎 10 个生产文件(`engine_settlement`/`engine_fees_market`/`engine_open_exec`/`execution_protection`/`engine_recovery`/`engine_risk_util`/`directional_spot_execution`/`dead_mans_switch`/`runtime_capital_adjustment`/`user_data_stream`)内 `/fapi`·`/api/v3`·`/sapi` 字面量清零(grep 钉死,仅余 `_test.go` mock 断言真实路径)。最终 URL 逐字节不变,`go build/vet/test`+race 全绿。✅
  - [x] P1b:**签名/发送原语下沉适配器** ✅——`internal/venue/binance/transport.go` 的 `(*Adapter).SignedSend` 接管「怎么签」(query 编码 + recvWindow/timestamp + HMAC `&signature=` + `X-MBX-APIKEY` 头 + used-weight 记账);`binance_gateway.go` 仅保留与所无关中间件(熔断/限频/used-weight 预算/-1021 重试编排/平台计数),`sendSignedOnce` 改委托 `SignedSend` 并把 `HTTPOutcome` 映射回 `signedRequestError`(分类语义逐字节不变)。引擎经 `binanceVenue()` 复用同一适配器实例(无状态、热路径不回写共享字段以避免 race)。`go build/vet/test`+race 全绿。
    - 备注:`getJSON`(公共 GET,签名无关)仍走 `client.GetJSON`;`apiKeyRequest`(listenKey)签名头逻辑下沉留作 P1b 收尾小项,不阻塞 P2。
  - [ ] P1c:影子拦截上移到 `Trading.PlaceOrder`。**依赖 P2 的 `PlaceOrder` 中性方法**(下单语义方法尚未存在,提前造会成 P2 丢弃的临时件),故随 P2 一并落地。**安全约束(已确认)**:当前 `signedRequest` 单咽喉点拦截**所有**非 GET 签名请求(下单 + 划转 + 借还 + 杠杆 + marginType + dead-man 等),上移到 `PlaceOrder` 后须**保留**这层「非下单资金动作」影子保护作为双层兜底,绝不缩小防护面;同时支持一所 shadow、一所 live 混合运维。
- [~] P2:`EconomicParams` 落地,`FeeReserveRate`/`MaxEstimatedRate` 改实拉派生(借币储备保持独立;`MaxEstimatedRate` 走 §6.1 方案 B),撞限豁免走 `FundingSnapshot`
  - [x] P2.1:`internal/app/economic_params.go` 建 `EconomicParams` 类型 + `BreakEven()` + `economicParamsFromConfig()`;盈亏平衡公式收敛为单一归属(`coherentMinFundingRate` 改薄委托)。引擎新增 `econ` 字段(构造器 `economicParamsFromConfig(cfg)` 实例化)+ `economicParams()` 访问器(对直建结构体测试兜底);引擎侧经济闸读取(scan 平衡线/净利门/退出预留/maxEstimated 展示,见 `engine_cost_guard`/`open_protocol`/`funding_sniper_engine`/`scan_observation`/`engine_fees_market`)统一改走 `e.economicParams()`。**值 1:1 镜像 cfg,零行为变更**;`go build/vet/test`+race 全绿。
  - [x] P2.2:`FeeReserveRate=2*SpotTaker+2*FuturesTaker`(实拉 CostProfile)落地 ✅。`economic_params.go` 加 `deriveFeeReserveRate()` + `applyDerivedEconomicParams()`;`initialize` 在实拉 taker 费率写回 `HistoricalConfig` 后调用,把派生值**写回 `cfg.StrategyConfig.FeeReserveRate`**(与既有 taker 写回同模式)→ 策略层(读 cfg)与引擎(读 econ)口径一致**不漂移**。新增开关 `StrategyConfig.AutoDeriveFeeReserveEnabled`(默认 true,`core/config.go`+`defaults.go`)可回退。**fail-safe**:开关关 / 实拉费率 ≤0 → 保持手填 0.0030,绝不塌成 0。`MaxEstimatedRate` 按 §6.1 方案 B 保留全局默认(逐币精度交 cap-pinned 豁免)。等价性 + fail-safe + 不漂移单测齐全;`go build/vet/test`+race 全绿。
- [~] P2:**经济等价单测**——币安 `coherentMinFundingRate` 派生值 vs 旧手填值 diff < ε(锚定)
  - [x] P2.1 锚定:`economic_params_test.go` 断言 `economicParamsFromConfig` 各字段镜像 cfg、`BreakEven()` 与旧手写公式逐位等价、`coherentMinFundingRate` 委托等价、访问器兜底自洽。
  - [x] P2.2 锚定:`economic_params_test.go` 断言默认费率(spot 0.0010/futures 0.0005)派生 `FeeReserveRate` ≈ 手填 0.0030(diff<ε,零漂移);BNB 抵扣费率派生值更低;`applyDerivedEconomicParams` 开/关/fail-safe 三态 + 策略↔引擎口径一致。
- [x] P2:调度退役全局 `WakeMinute`/`NextWake` 新开兜底,改目标调度模型(§P2 框)✅。`scheduler.go` 加 `NextScanWake`(最近未来结算 − ScanWindow)+ `NextMarketWake`(扫描唤醒 ∪ 逐币复核 :55,**完全不引用 WakeMinute**);主循环(`funding_sniper_engine.go` Start)弃用 `EnableRollingHold` 唤醒分叉,统一走 `NextMarketWake`,新增 `marketNextFundings` 缓存(扫描写/循环读,`platformRiskMu` 守护)。**受控行为变更**:逐结算唤醒取代每小时 :57 空扫。**对照测试**:① 机会捕获等价(币安固定 8h 边界 24h 跨度,新模型捕获集 = 旧 :57 全扫,且唤醒次数显著下降);② `NextMarketWake` 结果与 `WakeMinute` 取值无关(grep 钉死解绑)。**退避守卫**:补回旧 :57 模型隐含的失败退避——立即唤醒若非因续持复核(冷启动/扫描失败/边界刚过),施加 `scanBootstrapRetryInterval`(15s),防扫描持续失败空转打满接口招熔断。`NextWake`/`NextPortfolioWake` 仅留作回归锁,生产调度路径不再引用。`go build/vet/test`+race 全绿。
- [~] P2:`symbolRiskProfile` 分层键改 base asset/canonical `Symbol`(与符号中性化同 PR),`GetSymbolRiskProfile` 不再吃币安原始符号
  - [x] P2.3a:分层表(`majorRiskTierAssets`/`mainstreamRiskTierAssets`,`engine_types.go`)改按 **base asset** 归类;新增 venue-neutral `baseAssetOf()`(兼容原始 `BTCUSDT` 与 canonical `BTC-USDT`),`GetSymbolRiskProfile` 经它解析后查表。币安原始符号分层结果与改造前逐一致(锚定单测 `symbol_risk_profile_test.go`),零行为变更;`go build/vet/test`+race 全绿。
  - [ ] P2.3b:`FundingCandidate`/`MonitoredTarget`/`OpenHedge`/记录等引擎内符号字段是否全面切 `venue.Symbol`(见下「实现者注记」——对单所多引擎架构,建议**边界翻译**而非大爆炸式类型替换)。
- [ ] P3:**扩展** YAML schema(venues 层级,非迁移库)+ 每所 secret 节
- [~] P3:**Orchestrator + per-venue `RuntimeController`/`ReportStore`**(拆共用 store,`records/<venue>/` 各一套)— review 列为与适配器同级的核心缺口
  - [x] P3a:`internal/app/orchestrator.go` 建 `Orchestrator`+`venueRuntime` 骨架(Register/Get/Primary/All/Names);`DashboardServer` 改持有 `Orchestrator`,handler 仍走 `Primary()`(= 唯一注册的 "binance",包住现有单例)→ 路由零变化;`run.go` 用 Orchestrator 组装。零行为变更 + 单测;`go build/vet/test`+race 全绿。
  - [x] P3b:per-venue `RuntimeController`/`ReportStore` 真隔离 —— **完成**。`recordDirForVenue(cfg, venue)` 为单一路径事实源(币安=`records/` 后向兼容,其它所=`records/<venue>/`);`buildVenueRuntime` 为每所组装独立 `(ReportStore, RuntimeController)`,各持「以该所 recordDir 为 RecordDir 的 cfg 副本」→ 单写租约(`.lingxi-live.lock`)/风控状态/halt 文件天然按所隔离。`startLiveStack` 经 `buildVenueRuntime` 注册币安(取值逐字节同改造前)。锁定单测 `TestRecordDirForVenue`,`go build`/`vet`/`test -race` 全绿。
  - [x] P3c:控制/读 API `?venue=` 经 Orchestrator 路由 + `venue=all` 聚合 —— **完成**。`dashboard_venue_routing.go` 加 `venueParam`(裁空格+小写归一)、`resolveVenue`(空/all→Primary、具名→命中或报错)、`readTargets`(空→[Primary]、具名→[该所]、all→全部所注册序)、`resolveControlTarget`(写操作:空→Primary、具名→命中或 404、**all→400 显式拒绝写扇出**)及泛型聚合器 `aggregateRecords`/`aggregateStats`。列表读接口(settlements/monitor/backtest/residual-risk/severity/scenario/unified-events/execution-incidents/domain-events)改走 `readTargets`+聚合;单所视图读(summary/status/logs/config GET/preview)走 `resolveVenue`;控制写(config POST/rollback/clear-*/capital/start/stop)走 `resolveControlTarget`。**缺省(无 ?venue=)= Primary,单所逐字节同改造前**。路由/聚合/拒绝单测齐全(`dashboard_venue_routing_test.go`);`go build`/`vet`/`test -race` 全绿。
- [x] P3:**双级 emergency stop**(全局 `.halt-live` + `records/<venue>/.halt-live`)—— **完成**(P3d)。`core/config.go` `NetworkConfig` 加 `GlobalEmergencyStopFile`(运行期由 `venueEngineConfig` 注入为基记录目录 `.halt-live`);`live_safety.go` 加 `emergencyStopPaths`/`dedupEmergencyStopPaths`/`ensureEmergencyStopsClear`,`checkEmergencyStop` 改为「全局在前、本所在后,任一存在即停,全清才放行」;引擎新增 `globalEmergencyStopFile` 字段;`runtime_startup_checks.go` 启动前检查同改双级。**币安基目录=本所目录 → 两路径去重为一条,逐字节同改造前**;okx 等隔离为 `records/<venue>/.halt-live` + 共享总闸 `records/.halt-live`。单测 `live_safety_dual_halt_test.go`(路径派生/去重/任一级触发/全清放行);`go build`/`vet`/`test -race`+core 全绿。
- [ ] P3:**启动隔离**——一所 key/连接失败 → 该所 disabled + 告警,其它所正常 Start。**随 P3f 落地**:当前仅注册唯一必需的币安所(失败应整体退出,无可隔离对象);多所注册循环在 P3f 扩 YAML `venues` schema 时引入,届时每所 build/Start 包 recover + disabled 标记。
- [ ] P3:控制 API venue 化(`/api/runtime/{stop,start,config}?venue=` + `venue=all`,缺省写操作拒绝)—— 由 P3c 覆盖(见上)。
- [x] P3:`internal/metrics/performance.go` + 单测 —— **完成**(P3e 核心,纯函数)。`ComputePerformance(PerformanceInputs)` 按 §9.4 锁定口径算 TotalReturn/AnnualizedReturn(几何 365 年化)/MaxDrawdown(USDT)+Rate/Sharpe(N-1 std)/Sortino(N 下行偏差)/Calmar/Alpha/Beta/WinRate(日胜率),含 `Insufficient`(N<2)与 `BenchmarkMissing`(基准缺失/长度不齐/Var=0 → Alpha/Beta 失效不静默换源)铁律;管道助手 `BuildDailySeries`(按 UTC 日聚合 + 首末间补零)/`ReturnsFromDaily`(÷分配本金)/`EquityCurve`(权益+水下回撤点);`UTCDay` 归一。手算锚定单测(Sharpe≈5.06/Sortino≈11.03、Beta=1/0.5、补零、Calmar 自洽)`performance_test.go` 全绿。
  - [x] P3e 收尾(P3f.3 完成):`GET /api/performance?venue={..|all}` 读 `records/<venue>/settlements.json`(仅 `FundingIncomePending=false`)装管道(`performance_api.go`),单所/`all` 组合聚合,÷该所 `allocatedCapitalUSDT`(venueRuntime 注入)→ `ComputePerformance`+`EquityCurve`,路由注册 `/api/performance`;capital=0 判 Insufficient。单测 `performance_api_test.go` 全绿。**待补**:Binance 现货 BTCUSDT 日 K 基准(→ Alpha/Beta;当前 BenchmarkMissing)。
  - [ ] P3e 收尾:面板 venue Tab + 绩效卡片 + 权益/回撤图(前端 webpanel/index.html,无新依赖)——大块前端,独立推进。
- [ ] P3:面板 venue Tab + 总览 + 绩效卡片 + 权益/回撤图(小样本默认突出 MaxDD/Sortino/胜率)
- [ ] P3:**metrics/logs/trace 加 `venue` label**;`reconcile`/`shadow` 支持 `--venue=`
- [ ] P3:**`POST /api/runtime/capital?venue=`** + 面板 Tab 内独立调仓(localStorage per-venue);运行约束仅该所 running 时拒绝(§7.8)
- [ ] P3:`clear-*`/下载链接 per-venue;`clear-all` 强制 confirm;审计日志带 venue
- [ ] P3:performance benchmark 固定 **Binance BTC spot 日 K**(§9.1)
- [ ] P3:`summary?venue=all` 响应注入 `venue` 字段(读时注入,不改历史落盘)(§9.2)
- [~] P3:`BindConfig` `venues` 采用 **path B**(§8)—— **P3f.1 完成(schema 解析 + 合并)**。`EngineConfig` 加顶层 `allocated_capital_usdt`(绩效分母,§9.1);`config_yaml.go` 全局 flatten 在根层跳过 `venues:` 子树;`config_venues.go` 加 `VenuePlan`/`parseVenuePlans`/`loadVenuePlans`/`enabledVenuePlans`:`venues:` 独立解析,每所 = 全局 cfg 副本 + 该所覆盖(`allocated_capital_usdt`/`strategy`/`risk`/`execution`/`portfolio`/`active_profile` 经 BindConfig 逐字段合并),`enabled`(默认 true)/`autostart` 抽为编排元信息,重复所名/非法布尔/合并后非法配置均报错。**无 `venues:` 段 → nil 计划,单所旧路径零行为变更**。单测 `config_venues_test.go`(无段/合并+元信息/禁用继承/重复/坏布尔/flatten 跳过)全绿。**待续(随实际接第二所 + 密钥)**:secret 每所一节含 `passphrase`/`proxy_url`(`readSimpleYAML` 仅支持扁平,需扩);编排注册循环消费 `enabledVenuePlans` + 启动隔离。
- [ ] P3:`capital_ramp`/`ActiveProfile` 确认随 per-engine cfg 隔离(免改,§13.2);仅做展示层选所
- [ ] P3:Orchestrator 启动语义定稿(§7.9):enabled 所建 runtime 但默认 idle、Tab 内 Start/Stop、mode 走 `execution.shadow_trading`、去掉「启动任务」手填 secret-path
- [ ] P3:单端口/单令牌服务所有 Tab(§9.6);runtime 状态区(Run/Cycle/阶段)随 Tab per-venue
- [ ] P3:`WinRate` 用日胜率 `count(r_t>0)/N`(§9.4);benchmark 即使 `binance.enabled=false` 也可只读拉公共 K 线
- [ ] P1/P4:`CapitalManagement` 抽象(§3.1,Venue 接口已 embed);P3 币安先整文件复用,P4 才拆进 `venue/binance/capital.go`;`venue/<vendor>/capital.go` 模板就位
- [ ] P4:`venue/bybit` 过一致性套件 → shadow → 小额实盘 → reconcile HEALTHY
- [ ] P4:Bybit 调仓 `PlanAllocation`(统一账户 spot/perp);`SupportsNegativeCarry=false` 用 50/50;conformance 测 wallet plan
- [ ] P5:`venue/okx`、`venue/gate` 同上(Gate 可先 `SupportsCapitalRebalance=false`,只读快照人工调)
- [ ] 全程:每阶段 `go build ./... && make vet test race` 全绿,币安实盘不中断

