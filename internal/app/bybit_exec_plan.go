package app

import (
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
		ai, aj := math.Abs(out[i].FundingRate), math.Abs(out[j].FundingRate)
		if ai != aj {
			return ai > aj // 按 |费率| 降序:正负 carry 同台竞争,吃最肥的
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
	legUSDT := perCandidateUSDT / 2 // 现货名义≈永续名义,delta 中性,永续 1x

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
	netProfit := grossFunding - feeCost
	deployed := spotNotional + perpNotional // 现货全额 + 永续 1x 保证金
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
	}
	if netRate < cfg.MinNetProfitRate {
		return plan, false, "成本后净利率低于阈值"
	}
	return plan, true, "ok"
}
