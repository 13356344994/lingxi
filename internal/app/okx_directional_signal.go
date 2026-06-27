package app

// okx_directional_signal.go OKX 方向引擎的纯信号/风控核心(无 IO、可单测)。
//
// 设计(综合日志实证 + 用户决策):
//   - 选币交给引擎:OKX 资费快照按 |费率| 排序筛"被资费标记的币"(用户的底子)。
//   - 方向由价格 regime 决定(双向 × 两态):趋势态顺势(EMA 快慢),震荡态反转(z 分),
//     既非只单方向、也非只震荡——满足"两种方向"。
//   - 资费做对齐闸(不预测"价格跟随/反向资费"这一日志无法验证的因果):只在资费不逆持仓时开
//     (多要费率≤阈值、空要费率≥−阈值),令持仓顺带收资费、绝不逆扛资费;资费强对齐时加信心。
//   - 成本地板:预期移动 bps 须 ≥ 成本地板,挡薄盘穿价(OKX 实测往返成本中位 ~0、p95 ~177bps)。
//
// 本文件只做"算",不下单、不落盘、不拉数据;由 okx_directional engine(plumbing)调用。

import (
	"fmt"
	"math"
)

// DirectionSide 方向信号。
type DirectionSide int

const (
	DirectionFlat DirectionSide = iota
	DirectionLong
	DirectionShort
)

func (d DirectionSide) String() string {
	switch d {
	case DirectionLong:
		return "long"
	case DirectionShort:
		return "short"
	default:
		return "flat"
	}
}

// DirectionRegime 价格态。
type DirectionRegime int

const (
	RegimeRange DirectionRegime = iota
	RegimeTrend
)

func (r DirectionRegime) String() string {
	if r == RegimeTrend {
		return "trend"
	}
	return "range"
}

// DirectionalSignal 单币方向判定结果。
type DirectionalSignal struct {
	Side            DirectionSide
	Regime          DirectionRegime
	Conviction      float64 // [0,1] 信心
	ExpectedMoveBps float64 // 预期可捕获移动(bps),供成本地板对比
	Reason          string
}

// emaLast 返回收盘序列的最后一个 EMA 值(period≤1 或样本不足时回退末值/简单均值)。
func emaLast(closes []float64, period int) float64 {
	n := len(closes)
	if n == 0 {
		return 0
	}
	if period <= 1 || n == 1 {
		return closes[n-1]
	}
	k := 2.0 / (float64(period) + 1.0)
	ema := closes[0]
	for i := 1; i < n; i++ {
		ema = closes[i]*k + ema*(1-k)
	}
	return ema
}

// efficiencyRatio Kaufman 效率比 = |净位移| / Σ|逐根位移|,∈[0,1];越高越趋势,越低越震荡。
func efficiencyRatio(closes []float64) float64 {
	n := len(closes)
	if n < 2 {
		return 0
	}
	net := math.Abs(closes[n-1] - closes[0])
	var path float64
	for i := 1; i < n; i++ {
		path += math.Abs(closes[i] - closes[i-1])
	}
	if path <= 0 {
		return 0
	}
	return net / path
}

// meanStd 返回均值与样本标准差(总体口径,n 分母)。
func meanStd(xs []float64) (mean, std float64) {
	n := len(xs)
	if n == 0 {
		return 0, 0
	}
	for _, x := range xs {
		mean += x
	}
	mean /= float64(n)
	var v float64
	for _, x := range xs {
		d := x - mean
		v += d * d
	}
	return mean, math.Sqrt(v / float64(n))
}

// closeZScore 最新收盘相对窗口均值的 z 分(std=0 时返回 0)。
func closeZScore(closes []float64) (z, mean, std float64) {
	n := len(closes)
	if n < 2 {
		return 0, 0, 0
	}
	mean, std = meanStd(closes)
	if std <= 0 {
		return 0, mean, 0
	}
	return (closes[n-1] - mean) / std, mean, std
}

// fundingPermits 资费对齐闸:返回该方向是否被资费允许(不逆扛资费)+ 是否强对齐(加信心)。
//
//	多:费率 ≤ +minAbs 允许(费率为负=做多还收资费,强对齐);
//	空:费率 ≥ −minAbs 允许(费率为正=做空还收资费,强对齐)。
func fundingPermits(side DirectionSide, fundingRate, minAbs float64) (permit, strong bool) {
	if minAbs < 0 {
		minAbs = 0
	}
	switch side {
	case DirectionLong:
		return fundingRate <= minAbs, fundingRate < 0
	case DirectionShort:
		return fundingRate >= -minAbs, fundingRate > 0
	default:
		return false, false
	}
}

// computeDirectionalSignal 纯函数:由收盘序列 + 当前资费 + 配置算出方向信号。
// closes 须为 OLDEST-first 收盘价。fundingRate 为该币 OKX 单次结算费率(小数)。
func computeDirectionalSignal(closes []float64, fundingRate float64, cfg OKXDirectionalConfig) DirectionalSignal {
	out := DirectionalSignal{Side: DirectionFlat, Regime: RegimeRange}
	n := len(closes)
	if n < 5 || closes[n-1] <= 0 {
		out.Reason = "样本不足"
		return out
	}
	last := closes[n-1]
	win := cfg.SignalWindowBars
	if win <= 0 {
		win = 24
	}
	sc := closes
	if len(closes) > win {
		sc = closes[len(closes)-win:]
	}
	er := efficiencyRatio(sc)
	trendThresh := cfg.TrendEfficiencyThreshold
	if trendThresh <= 0 {
		trendThresh = 0.30
	}

	var base DirectionSide
	if er >= trendThresh {
		// 趋势态:顺势(EMA 快慢)。
		out.Regime = RegimeTrend
		fast := emaLast(closes, cfg.EMAFast)
		slow := emaLast(closes, cfg.EMASlow)
		if fast > slow {
			base = DirectionLong
		} else if fast < slow {
			base = DirectionShort
		}
		// 预期移动:趋势态用信号窗口净位移幅度近似。
		out.ExpectedMoveBps = math.Abs(last-sc[0]) / last * 10000
		out.Conviction = clamp01(er)
	} else {
		// 震荡态:反转(z 分)。
		out.Regime = RegimeRange
		z, _, std := closeZScore(sc)
		zEntry := cfg.ZScoreEntry
		if zEntry <= 0 {
			zEntry = 1.5
		}
		if z >= zEntry {
			base = DirectionShort // 偏高 → 回归做空
		} else if z <= -zEntry {
			base = DirectionLong // 偏低 → 回归做多
		}
		// 预期移动:回归到均值约 |z|×std。
		if std > 0 {
			out.ExpectedMoveBps = math.Abs(z) * std / last * 10000
		}
		out.Conviction = clamp01(math.Abs(z) / (zEntry * 2))
	}

	if base == DirectionFlat {
		out.Reason = "无方向信号(regime=" + out.Regime.String() + ")"
		return out
	}

	// 防陷阱入场质量闸:波动/单边/暴拉回调/抄底/追高。
	if ok, reason := directionalEntryGuard(closes, base, out.Regime, emaLast(closes, cfg.EMAFast), cfg); !ok {
		out.Reason = reason
		return out
	}

	// 资费对齐闸。用独立的"强逆否决阈值"(而非选币阈值),否则会把刚过选币线的币(如 +0.0538%)在对齐方向上误杀。
	if cfg.RequireFundingAlignment {
		vetoAbs := cfg.FundingVetoAbsPerInterval
		if vetoAbs <= 0 {
			vetoAbs = 0.0015 // 0.15%/结算:仅当资费明显逆持仓才否决
		}
		permit, strong := fundingPermits(base, fundingRate, vetoAbs)
		if !permit {
			out.Reason = "资费逆持仓,放弃(" + base.String() + " 费率=" + formatRatePct(fundingRate) + ")"
			return out
		}
		if strong {
			out.Conviction = clamp01(out.Conviction + 0.2)
		}
	}

	out.Side = base
	out.Reason = out.Regime.String() + " " + base.String()
	return out
}

// directionalShouldClose 持仓出场判定(纯):止损/止盈/信号反转/超时。返回 (是否平, 原因)。
//
//	uPnLPct = 未实现盈亏 / 名义(已含方向符号,正=盈)。holdMin = 已持有分钟。
func directionalShouldClose(uPnLPct float64, holdMin int, side DirectionSide, fresh DirectionalSignal, cfg OKXDirectionalConfig) (bool, string) {
	sl := cfg.StopLossPct
	if sl <= 0 {
		sl = 0.025
	}
	tp := cfg.TakeProfitPct
	if tp <= 0 {
		tp = 0.015
	}
	if uPnLPct <= -sl {
		return true, "硬止损"
	}
	if uPnLPct >= tp {
		return true, "止盈"
	}
	if cfg.MaxHoldMinutes > 0 && holdMin >= cfg.MaxHoldMinutes {
		return true, "超时离场"
	}
	// 信号反转:新信号给出相反方向且有信心 → 平(由引擎决定是否反手)。
	if fresh.Side != DirectionFlat && fresh.Side != side {
		return true, "信号反转"
	}
	return false, ""
}

// directionalEntryGuard 防陷阱入场闸:返回 (是否放行, 否决理由)。
//
//	趋势态:① 同向暴拉尖刺不追(暴拉回调/追高)② 离快EMA过度延展不追(单边/追高)。
//	震荡态:① 近窗波动相对基线骤升=区间在破,不反转(波动陷阱)② 极值仍急速延续不接(抄底接飞刀/追空顶)。
func directionalEntryGuard(closes []float64, base DirectionSide, regime DirectionRegime, emaFast float64, cfg OKXDirectionalConfig) (bool, string) {
	n := len(closes)
	if n < 6 {
		return true, ""
	}
	last, prev := closes[n-1], closes[n-2]
	lastRet := 0.0
	if prev > 0 {
		lastRet = (last - prev) / prev
	}
	spike := cfg.SpikeBarPct
	if spike <= 0 {
		spike = 0.03
	}
	extMax := cfg.TrendMaxExtensionPct
	if extMax <= 0 {
		extMax = 0.02
	}
	volExpMax := cfg.VolExpansionMaxRatio
	if volExpMax <= 0 {
		volExpMax = 1.8
	}
	knife := cfg.ReversionKnifePct
	if knife <= 0 {
		knife = 0.015
	}

	if regime == RegimeTrend {
		// 暴拉尖刺:最后一根同向暴动,大概率紧接回调,别追。
		if math.Abs(lastRet) >= spike && ((base == DirectionLong && lastRet > 0) || (base == DirectionShort && lastRet < 0)) {
			return false, fmt.Sprintf("暴拉尖刺不追(lastRet=%.2f%%≥%.2f%%)", lastRet*100, spike*100)
		}
		// 过度延展:价格离快EMA过远,追高/杀跌风险大,等回踩。
		if emaFast > 0 {
			ext := (last - emaFast) / emaFast
			if (base == DirectionLong && ext > extMax) || (base == DirectionShort && ext < -extMax) {
				return false, fmt.Sprintf("离快EMA过远不追(ext=%.2f%%)", ext*100)
			}
		}
		// 回踩入场:趋势态只在最后一根顺势方向涨跌 ≤ 阈值(回踩/横盘)时入场,不追顺势暴动的当根——
		// 买回踩不买尖顶,根治趋势单追高(复盘 trend 胜率仅 25%、冲一下就反转)。
		pull := cfg.TrendEntryPullbackMaxBarPct
		if pull > 0 {
			if (base == DirectionLong && lastRet > pull) || (base == DirectionShort && lastRet < -pull) {
				return false, fmt.Sprintf("等回踩不追当根顺势冲(lastRet=%.2f%%>%.2f%%)", lastRet*100, pull*100)
			}
		}
		return true, ""
	}

	// 震荡态反转。
	rets := pctReturns(closes)
	if len(rets) >= 6 {
		_, baseVol := meanStd(rets)
		k := 8
		if k > len(rets) {
			k = len(rets)
		}
		_, recentVol := meanStd(rets[len(rets)-k:])
		if baseVol > 0 && recentVol/baseVol > volExpMax {
			return false, fmt.Sprintf("波动骤升区间在破,不反转(vol×%.2f)", recentVol/baseVol)
		}
	}
	// 飞刀/急拉:极值仍以高速延续,别接。
	if base == DirectionLong && lastRet <= -knife {
		return false, fmt.Sprintf("仍急跌不抄底(lastRet=%.2f%%)", lastRet*100)
	}
	if base == DirectionShort && lastRet >= knife {
		return false, fmt.Sprintf("仍急拉不追空顶(lastRet=%.2f%%)", lastRet*100)
	}
	return true, ""
}

// dirPosActionKind 持仓动作类型。
type dirPosActionKind int

const (
	dirActHold dirPosActionKind = iota
	dirActClose
	dirActPartialTP
	dirActScaleIn
)

// dirPosAction 数据驱动的单仓动作判定结果。
type dirPosAction struct {
	Kind   dirPosActionKind
	Reason string
}

// decideDirectionalPositionAction 纯函数:数据驱动 + 快进快出哲学,判断本仓该怎么做。
// 优先级:① 风险(触止损地板/信号反转/超时)永远先平;
//
//	② 浮盈达小止盈 QuickTPPct:默认"吃到就跑"全平落袋;仅当数据判定强延续(信心高+趋势态同向+未到补仓上限)才骑乘加仓博大趋势;
//	③ 未到小止盈:持有,亏由止损地板/反转/超时治理(不让亏损长大)。
//
// 小资金高频核心:赢快落袋、亏不放大、单笔净期望盖过成本(配合 maker 进场)。判据全来自实时信号/浮盈,不是到点机械执行。
func decideDirectionalPositionAction(
	uPnLAccount, peakAccount, stopFloor float64,
	holdMin int,
	side DirectionSide,
	fresh DirectionalSignal,
	partialDone bool,
	addCount int,
	cfg OKXDirectionalConfig,
) dirPosAction {
	// ① 风险闸,永远最先:触止损地板 / 信号反转。
	if uPnLAccount <= stopFloor {
		if partialDone {
			return dirPosAction{dirActClose, "移动止损保盈"}
		}
		return dirPosAction{dirActClose, "硬止损"}
	}
	if fresh.Side != DirectionFlat && fresh.Side != side {
		return dirPosAction{dirActClose, "信号反转"}
	}

	addConv := cfg.AddConvictionMin
	if addConv <= 0 {
		addConv = 0.6
	}
	maxAdds := cfg.MaxAdds
	if maxAdds <= 0 {
		maxAdds = 2
	}
	quickTP := cfg.QuickTPPct
	if quickTP <= 0 {
		quickTP = 0.12
	}
	arm := cfg.BreakevenArmPct
	if arm <= 0 {
		arm = 0.05
	}
	trailGap := cfg.TrailGapPct
	if trailGap <= 0 {
		trailGap = 0.06
	}
	lockFrac := cfg.TrailLockFraction
	if lockFrac <= 0 {
		lockFrac = 0.5
	}

	// ② 盈利保护(移动保盈棘轮):高水位过臂点后,按峰值比例锁利——
	//    锁利线 = max(峰值×lockFrac, 峰值−trailGap),浮盈回吐到锁利线即落袋。
	//    旧实现用 max(0, 峰值−0.15):峰值<15% 时锁利线恒=0(只保本),叠加 QuickTP=12%,
	//    [臂点,QuickTP] 这段最常发生的浮盈区间裸奔——冲到 +6~10% 又回头的单(趋势追高单典型轨迹)
	//    全额回吐成保本/小亏(复盘实证:这是"很少赚"的主因)。改为比例锁利后,峰值+10% 至少落袋 +5%。
	//    上涨途中(uPnL 仍在锁利线之上)不触发,赢单照常冲 QuickTP;仅"从峰值回吐"才落袋,不误伤利润源。
	if peakAccount >= arm {
		lock := peakAccount * lockFrac
		if byGap := peakAccount - trailGap; byGap > lock {
			lock = byGap // 高峰值时用更紧的绝对间距二次收紧
		}
		if lock < 0 {
			lock = 0
		}
		if uPnLAccount <= lock {
			return dirPosAction{dirActClose, fmt.Sprintf("移动保盈(峰值%.0f%%锁利%.0f%%,回吐至%.0f%%)", peakAccount*100, lock*100, uPnLAccount*100)}
		}
	}

	// ②.5 分批止盈(scale-out):浮盈首次达 PartialTPTriggerPct(< QuickTP)且未分批过 → 先落袋一部分,
	//      把"确定的利润"锁进口袋;剩余仓位继续由棘轮(②)/快进快出(③)管理博更大趋势。
	//      根治"很少赚":复盘里大量单冲到 +6~10% 却没够到 +12% 止盈、随后回吐——分批后至少这一部分已落袋。
	partialTrig := cfg.PartialTPTriggerPct
	partialFrac := cfg.PartialTPFraction
	if partialTrig > 0 && partialFrac > 0 && partialFrac < 1 && !partialDone &&
		uPnLAccount >= partialTrig && uPnLAccount < quickTP {
		return dirPosAction{dirActPartialTP, fmt.Sprintf("分批止盈(浮盈%.0f%%先落袋%.0f%%,余仓博趋势)", uPnLAccount*100, partialFrac*100)}
	}

	// ③ 快进快出小止盈:浮盈达 QuickTP 即全平落袋;仅强延续(高信心+趋势同向+未到补仓上限)才骑乘加仓。
	strongContinuation := fresh.Side == side && fresh.Conviction >= addConv && fresh.Regime == RegimeTrend
	if uPnLAccount >= quickTP {
		if strongContinuation && addCount < maxAdds {
			return dirPosAction{dirActScaleIn, fmt.Sprintf("强延续骑乘加仓(信心%.2f 趋势 浮盈%.0f%%)", fresh.Conviction, uPnLAccount*100)}
		}
		return dirPosAction{dirActClose, fmt.Sprintf("快进快出止盈(浮盈%.0f%%本金,吃到就跑)", uPnLAccount*100)}
	}

	// ④ 治超时(停滞离场):给单子 ~2/3 窗口(StaleCutMinutes)仍未盈利就提前砍。
	//    复盘实证:拖到 MaxHold 超时的单整体净亏(主失血口);提前砍既缩小单笔亏损,又腾空位给新机会。
	//    已盈利/能冲到快进快出的赢单不受影响(仅当 uPnL ≤ StaleCutMinProfitPct,默认 0,即未盈利才砍)。
	staleCut := cfg.StaleCutMinutes
	if staleCut <= 0 {
		staleCut = 40
	}
	if cfg.MaxHoldMinutes > 0 && holdMin >= staleCut && uPnLAccount <= cfg.StaleCutMinProfitPct {
		return dirPosAction{dirActClose, fmt.Sprintf("停滞离场(持%dm未盈,浮盈%.0f%%)", holdMin, uPnLAccount*100)}
	}

	// ⑤ 超时:智能续持——达 MaxHold 后若仍盈利且信号同向,续持(让利润跑,受②保盈兜底);
	//    否则离场。续持不得超过绝对上限 AbsMaxHoldMinutes。
	if cfg.MaxHoldMinutes > 0 && holdMin >= cfg.MaxHoldMinutes {
		absMax := cfg.AbsMaxHoldMinutes
		if absMax <= 0 {
			absMax = cfg.MaxHoldMinutes * 3
		}
		aligned := fresh.Side == side && fresh.Side != DirectionFlat
		if holdMin < absMax && uPnLAccount > 0 && aligned {
			return dirPosAction{dirActHold, ""}
		}
		return dirPosAction{dirActClose, "超时离场"}
	}

	// ⑤ 未到小止盈且无风险:持有(亏由止损地板/反转/超时治理)。
	_ = partialDone
	return dirPosAction{dirActHold, ""}
}

// formatRatePct 把单次费率格式化成百分比字符串(供日志)。
func formatRatePct(r float64) string {
	return fmt.Sprintf("%.4f%%", r*100)
}

// recentDirection 由收盘序列在信号窗口内的净位移判最近方向:超死区→Long/Short,否则 Flat。
func recentDirection(closes []float64, win int, deadbandPct float64) DirectionSide {
	n := len(closes)
	if n < 2 {
		return DirectionFlat
	}
	if win > 0 && n > win {
		closes = closes[n-win:]
		n = len(closes)
	}
	first, last := closes[0], closes[n-1]
	if first <= 0 {
		return DirectionFlat
	}
	move := (last - first) / first
	if deadbandPct < 0 {
		deadbandPct = 0
	}
	if move > deadbandPct {
		return DirectionLong
	}
	if move < -deadbandPct {
		return DirectionShort
	}
	return DirectionFlat
}

// peerDirState 单个 peer 所对同币的最近方向。
type peerDirState struct {
	Venue string
	Side  DirectionSide
}

// crossVenueCorroboration 跨所交叉印证(纯):对比其他所同币最近方向。
// 返回 信心增量、是否否决、原因。规则:
//   - 关闭或无方向 → 不调整;
//   - 无任何有数据 peer(known=0)→ 不调整、绝不否决(币可能为本所独有);
//   - 有数据 peer 全部反向且数量 ≥ vetoMin → 否决(单所假动作/流动性陷阱);
//   - 否则 信心增量 = step×同向数 − step×反向数。
func crossVenueCorroboration(primary DirectionSide, peers []peerDirState, cfg OKXDirectionalConfig) (convDelta float64, veto bool, reason string) {
	if !cfg.CrossVenueCorroborationEnabled || primary == DirectionFlat {
		return 0, false, ""
	}
	step := cfg.CrossVenueConvictionStep
	if step <= 0 {
		step = 0.1
	}
	vetoMin := cfg.CrossVenueVetoMinPeers
	if vetoMin <= 0 {
		vetoMin = 2
	}
	var agree, disagree, known int
	for _, p := range peers {
		if p.Side == DirectionFlat {
			continue
		}
		known++
		if p.Side == primary {
			agree++
		} else {
			disagree++
		}
	}
	if known == 0 {
		return 0, false, "无跨所数据"
	}
	if agree == 0 && known >= vetoMin {
		return 0, true, fmt.Sprintf("跨所背离:%d所均反向,疑似单所假动作", known)
	}
	return step*float64(agree) - step*float64(disagree), false, fmt.Sprintf("同向%d/反向%d", agree, disagree)
}

// sanitizeFillPrice 校验成交价:avg<=0 或偏离 mid 超过 maxDevFrac → 判为不可信,回退用 mid。
// 纯函数,无副作用,可单测。
func sanitizeFillPrice(avg, mid, maxDevFrac float64) (price float64, trusted bool) {
	if mid <= 0 {
		if avg > 0 {
			return avg, true
		}
		return 0, false
	}
	if avg <= 0 {
		return mid, false
	}
	if maxDevFrac <= 0 {
		maxDevFrac = 0.10
	}
	if math.Abs(avg/mid-1) > maxDevFrac {
		return mid, false // 离谱成交价,回退 mid
	}
	return avg, true
}
