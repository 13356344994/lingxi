package app

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"lingxi/internal/venue"
)

// bybit_exec_plan.go 是精简 Bybit 执行器(Approach B)的**纯规划核心**:正资金费选币 + 50/50 delta-neutral
// 定量 + 成本后净利估算。完全不触网、不下单、不依赖币安引擎,可独立单测,对币安实盘零影响。
//
// 策略(正 carry,统一账户,无借币):对**高正资金费**永续,空永续腿(正费率时空头收资金费)+ 等额多现货腿,
// delta 中性;资金各半(现货名义 = 永续名义,永续 1x),跨结算持有收资金费,结算后双边平。
// 负 carry(借币做空现货吃负费率)Bybit 暂不做(Capabilities.SupportsNegativeCarry=false)。

// BybitExecConfig 是精简执行器的可调参数(由 venue/config 注入;此处只做纯计算)。
type BybitExecConfig struct {
	AllocatedUSDT    float64 // 该所总交易额
	TopN             int     // 每周期最多并发标的数
	MinFundingRate   float64 // 仅做单次结算正费率 ≥ 此阈值的标的(如 0.0005=0.05%)
	MinNetProfitRate float64 // 成本后净利率阈值(相对部署资金),低于则放弃
	SpotTakerFee     float64 // 现货吃单费率(实拉,见 FetchTradingCostProfile)
	PerpTakerFee     float64 // 永续吃单费率
	// EnableNegativeCarry=true 时,除正费率外也吃【负费率】标的:多永续(收负费率)+ 借币做空现货(UTA spot margin)。
	// 默认 false=仅正 carry(零行为变更)。负 carry 依赖统一账户开通现货杠杆借币,且借不到的现货卖单会被
	// 执行器回补永续兜底(绝不裸腿)。
	EnableNegativeCarry bool
	// PerpLeverage 永续腿杠杆(1~3;0 视为 1)。delta 中性下现货多腿全额抵御价格风险,永续腿用 >1x
	// 只是降低保证金占用、放大两腿可开名义:总资金 = 现货名义 + 永续名义/杠杆。统一账户全仓下现货本身
	// 是保证金抵押物,2x 的强平距离仍然很远;硬顶 3x(再高保证金收益递减、尾部风险陡增)。
	PerpLeverage int
	// EVGateEnabled=true 时,开仓门槛从「单次结算净利 ≥ MinNetProfitRate」切换为「多结算期望值 > 0」:
	// 用历史费率估计费率维持有利的期望结算次数 N,EV = N×费率×名义 − 四腿费用(− 负 carry 借币利息)。
	// 单次结算模型在 taker 费率下注定算出负数(被迫开负门纯交学费);滚动持有的真实经济学是多结算摊薄,
	// EV 门槛让「该开的单开出来、纯送钱的单不开」。历史样本不足时回退单次门(保守)。
	EVGateEnabled bool
	// RollEnabled / MaxHoldSettlements 与运行时滚动持有配置同源,EV 估计期望结算次数时的上限。
	RollEnabled        bool
	MaxHoldSettlements int
	// BorrowHourlyRate 负 carry 借币的每小时利率估计(如 0.00003=0.003%/h≈26% APR,保守取值)。
	// 计入单次净利与 EV 的成本侧;0=不计。正 carry 不借币,不受影响。
	BorrowHourlyRate float64
}

// bybitEffectiveLeverage 把配置杠杆规整到 [1,3]。
func bybitEffectiveLeverage(lev int) int {
	if lev < 1 {
		return 1
	}
	if lev > 3 {
		return 3
	}
	return lev
}

// bybitRatePerHour 把资金费率按结算间隔归一化为「每小时费率」。资金是按时间占用的,
// 1h 间隔 0.03% 的标的优于 8h 间隔 0.10%(0.0125%/h)——选币与排序必须用该口径,不能用原始费率。
func bybitRatePerHour(rate float64, intervalHours int) float64 {
	if intervalHours <= 0 {
		intervalHours = 8 // Bybit 默认结算间隔
	}
	return math.Abs(rate) / float64(intervalHours)
}

// BybitCandidate 是一个通过费率筛选的候选标的。
type BybitCandidate struct {
	Symbol          venue.Symbol
	FundingRate     float64
	IntervalHours   int
	NextFundingTime time.Time
	// Negative=true 表示负 carry 候选(费率为负,多永续吃负费率 + 借币做空现货);false=正 carry。
	Negative bool
}

// BybitOpenPlan 是一笔 delta-neutral 开仓计划(纯计算产物,供 shadow 打印或实盘下单)。
type BybitOpenPlan struct {
	Symbol           venue.Symbol
	BaseQty          float64 // 两腿共同的币数量(delta 中性)
	SpotPrice        float64
	PerpPrice        float64
	SpotNotional     float64
	PerpNotional     float64
	DeployedUSDT     float64 // 现货名义 + 永续保证金(1x 即 = 永续名义)
	FundingRate      float64
	EstGrossFunding  float64 // 单次结算预计毛资金费(空永续收)
	EstFeeCost       float64 // 开+平双边吃单费合计
	EstNetProfit     float64
	EstNetProfitRate float64 // 相对 DeployedUSDT
	NextFundingTime  time.Time
	// Negative=true 表示负 carry(多永续 + 借币做空现货);false=正 carry(空永续 + 多现货)。
	Negative bool
	// IntervalHours 结算间隔(小时),用于「费率/小时」口径的排序与续持换仓比较。
	IntervalHours int
	// PerpMarginUSDT 永续腿保证金占用(= 永续名义/杠杆;1x 即 = 名义)。开仓余额闸按它而非名义计。
	PerpMarginUSDT float64
	// PerpLeverage 本计划使用的永续腿杠杆(已规整到 1~3)。
	PerpLeverage int
}

// selectBybitCandidates 过滤「|费率| ≥ 阈值」的标的,按 |费率| 降序取 TopN。纯函数。
// 正费率恒纳入(正 carry);负费率仅在 EnableNegativeCarry 时纳入(负 carry,Negative=true)。
func selectBybitCandidates(snaps []venue.FundingSnapshot, cfg BybitExecConfig) []BybitCandidate {
	out := make([]BybitCandidate, 0, len(snaps))
	for _, s := range snaps {
		rate := s.LastFundingRate
		switch {
		case rate >= cfg.MinFundingRate && rate > 0:
			out = append(out, BybitCandidate{
				Symbol:          s.Symbol,
				FundingRate:     rate,
				IntervalHours:   s.IntervalHours,
				NextFundingTime: s.NextFundingTime,
				Negative:        false,
			})
		case cfg.EnableNegativeCarry && rate <= -cfg.MinFundingRate && rate < 0:
			out = append(out, BybitCandidate{
				Symbol:          s.Symbol,
				FundingRate:     rate,
				IntervalHours:   s.IntervalHours,
				NextFundingTime: s.NextFundingTime,
				Negative:        true,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		ai := bybitRatePerHour(out[i].FundingRate, out[i].IntervalHours)
		aj := bybitRatePerHour(out[j].FundingRate, out[j].IntervalHours)
		if ai != aj {
			return ai > aj // 按「费率/小时」降序:正负 carry 同台竞争,吃单位时间最肥的(间隔归一化)
		}
		return out[i].Symbol < out[j].Symbol // 平手按符号稳定排序
	})
	n := cfg.TopN
	if n <= 0 || n > len(out) {
		n = len(out)
	}
	return out[:n]
}

// roundDownToStep 把数量向下取整到 step 的整数倍(step<=0 时原样返回),并按 step 的小数位重新定标,
// 消除 steps*step 的浮点尾巴。否则下单数量字符串会带一长串小数,撞 Bybit retCode=170137
// "Order quantity has too many decimals."(平仓卖真实余额时尤其会触发)。
func roundDownToStep(v, step float64) float64 {
	if step <= 0 {
		return v
	}
	steps := math.Floor(v/step + 1e-9) // 小 eps 容忍浮点除法把恰好在边界的值算成差一格
	dec := decimalsForStep(step)
	pow := math.Pow(10, float64(dec))
	return math.Floor(steps*step*pow+0.5) / pow
}

// decimalsForStep 返回 step 的小数位数(如 0.01→2, 1→0, 0.001→3),用于把数量定标到交易所允许的精度。
func decimalsForStep(step float64) int {
	s := strconv.FormatFloat(step, 'f', -1, 64)
	if i := strings.IndexByte(s, '.'); i >= 0 {
		return len(s) - i - 1
	}
	return 0
}

// planBybitDeltaNeutralOpen 为单个候选计算 50/50 delta-neutral 开仓计划。
// perCandidateUSDT 为分给本标的的资金(现货名义 + 永续保证金各半)。
// 返回 (计划, 是否可下单, 原因)。原因用于 shadow 日志解释放弃。
func planBybitDeltaNeutralOpen(
	c BybitCandidate,
	spotPrice, perpPrice float64,
	spotInstr, perpInstr venue.Instrument,
	perCandidateUSDT float64,
	cfg BybitExecConfig,
) (BybitOpenPlan, bool, string) {
	if spotPrice <= 0 || perpPrice <= 0 {
		return BybitOpenPlan{}, false, "价格非正(行情缺失)"
	}
	if perCandidateUSDT <= 0 {
		return BybitOpenPlan{}, false, "分配资金非正"
	}
	// 资金约束:现货名义 + 永续保证金(=永续名义/杠杆) = perCandidateUSDT,且两腿名义相等(delta 中性)。
	// → 每腿名义 = 资金 × lev/(lev+1)。lev=1 退化为各半(行为不变);lev=2 时同样资金名义放大 33%。
	lev := bybitEffectiveLeverage(cfg.PerpLeverage)
	legUSDT := perCandidateUSDT * float64(lev) / float64(lev+1)

	targetSpotQty := legUSDT / spotPrice
	targetPerpQty := legUSDT / perpPrice
	// 两腿须同币数量;取较粗 step 向下取整(假设细 step 整除粗 step,交易所通常 10 的幂,成立)。
	step := math.Max(spotInstr.StepSize, perpInstr.StepSize)
	baseQty := roundDownToStep(math.Min(targetSpotQty, targetPerpQty), step)
	if baseQty <= 0 {
		return BybitOpenPlan{}, false, "取整后数量为 0(资金过小或步进过粗)"
	}

	spotNotional := baseQty * spotPrice
	perpNotional := baseQty * perpPrice
	if spotInstr.MinNotional > 0 && spotNotional < spotInstr.MinNotional {
		return BybitOpenPlan{}, false, "现货名义低于最小下单额"
	}
	if perpInstr.MinNotional > 0 && perpNotional < perpInstr.MinNotional {
		return BybitOpenPlan{}, false, "永续名义低于最小下单额"
	}

	// 毛资金费按 |费率| 计:正 carry 空永续收正费率;负 carry 多永续收负费率(付方→收方),两者收益都 = 名义×|费率|。
	grossFunding := perpNotional * math.Abs(c.FundingRate)
	// 开+平各一次吃单,双腿:现货 2 次 + 永续 2 次。
	feeCost := spotNotional*cfg.SpotTakerFee*2 + perpNotional*cfg.PerpTakerFee*2
	// 负 carry 的现货空腿要借币,持有一个结算间隔的利息计入成本(正 carry 不借币)。
	if c.Negative && cfg.BorrowHourlyRate > 0 {
		ih := c.IntervalHours
		if ih <= 0 {
			ih = 8
		}
		feeCost += spotNotional * cfg.BorrowHourlyRate * float64(ih)
	}
	netProfit := grossFunding - feeCost
	perpMargin := perpNotional / float64(lev)
	deployed := spotNotional + perpMargin // 现货全额 + 永续保证金(名义/杠杆)
	netRate := 0.0
	if deployed > 0 {
		netRate = netProfit / deployed
	}

	plan := BybitOpenPlan{
		Symbol:           c.Symbol,
		BaseQty:          baseQty,
		SpotPrice:        spotPrice,
		PerpPrice:        perpPrice,
		SpotNotional:     spotNotional,
		PerpNotional:     perpNotional,
		DeployedUSDT:     deployed,
		FundingRate:      c.FundingRate,
		EstGrossFunding:  grossFunding,
		EstFeeCost:       feeCost,
		EstNetProfit:     netProfit,
		EstNetProfitRate: netRate,
		NextFundingTime:  c.NextFundingTime,
		Negative:         c.Negative,
		IntervalHours:    c.IntervalHours,
		PerpMarginUSDT:   perpMargin,
		PerpLeverage:     lev,
	}
	if netRate < cfg.MinNetProfitRate {
		return plan, false, "成本后净利率低于阈值"
	}
	return plan, true, "ok"
}

// bybitEVMinSamples 是 EV 门槛要求的最少历史费率样本数;不足则回退单次结算门(保守:不凭少量样本下注)。
const bybitEVMinSamples = 8

// bybitFavorableRunStats 统计历史费率序列(顺序无关)中「对本方向有利」的连跑:
// 返回 (平均连跑长度, 有利样本的平均|费率|, 有利样本数)。有利定义:正 carry 费率 ≥ floor;负 carry 费率 ≤ -floor。
func bybitFavorableRunStats(history []float64, negative bool, floor float64) (avgRun float64, avgRate float64, favorable int) {
	if floor <= 0 {
		floor = 1e-9
	}
	isFav := func(r float64) bool {
		if negative {
			return r <= -floor
		}
		return r >= floor
	}
	runs, curRun := 0, 0
	rateSum := 0.0
	totalRunLen := 0
	for _, r := range history {
		if isFav(r) {
			favorable++
			rateSum += math.Abs(r)
			curRun++
			continue
		}
		if curRun > 0 {
			runs++
			totalRunLen += curRun
			curRun = 0
		}
	}
	if curRun > 0 {
		runs++
		totalRunLen += curRun
	}
	if favorable > 0 {
		avgRate = rateSum / float64(favorable)
	}
	if runs > 0 {
		avgRun = float64(totalRunLen) / float64(runs)
	}
	return avgRun, avgRate, favorable
}

// bybitMultiSettlementEV 计算一笔开仓计划的「多结算期望值」:
//
//	EV = 期望结算次数 N × 保守单次费率 × 永续名义 − 四腿费用 −(负 carry)借币利息×N
//
// N = min(历史有利连跑均长, 滚动持有上限);单次费率 = min(当前费率, 历史有利均值)(保守:不指望当前
// 尖峰费率一直维持)。history 样本 < bybitEVMinSamples 时返回 ok=false,调用方回退单次门。
func bybitMultiSettlementEV(plan BybitOpenPlan, history []float64, cfg BybitExecConfig) (ev float64, detail string, ok bool) {
	if len(history) < bybitEVMinSamples {
		return 0, "", false
	}
	avgRun, avgRate, favorable := bybitFavorableRunStats(history, plan.Negative, cfg.MinFundingRate)
	if favorable == 0 {
		// 历史从未有利过,当前费率大概率是一次性尖峰:按 1 次结算估。
		avgRun = 1
		avgRate = math.Abs(plan.FundingRate)
	}
	maxN := 1.0
	if cfg.RollEnabled && cfg.MaxHoldSettlements > 1 {
		maxN = float64(cfg.MaxHoldSettlements)
	}
	expN := math.Min(math.Max(avgRun, 1), maxN)
	perRate := math.Min(math.Abs(plan.FundingRate), avgRate)
	income := expN * perRate * plan.PerpNotional
	cost := plan.EstFeeCost
	if plan.Negative && cfg.BorrowHourlyRate > 0 {
		ih := plan.IntervalHours
		if ih <= 0 {
			ih = 8
		}
		// EstFeeCost 已含 1 个间隔的利息,这里补上其余 (expN−1) 个间隔。
		cost += plan.SpotNotional * cfg.BorrowHourlyRate * float64(ih) * (expN - 1)
	}
	ev = income - cost
	detail = fmt.Sprintf("期望%.1f次×%.4f%%×%.2fU=%.4f−成本%.4f", expN, perRate*100, plan.PerpNotional, income, cost)
	return ev, detail, true
}
