package app

// directional_exitsim_test.go 出场策略模拟器(研究用,默认跳过)。
//
// 目的:在历史 K 线上回放「生产入场信号 computeDirectionalSignal + 完整出场逻辑
// decideDirectionalPositionAction(原函数)」,扣交易成本,对比不同止盈/锁利参数的净期望。
// 用来在动实盘前,用数据判定「降 QuickTP / 调棘轮间距」到底是净赚还是把大赢单砍了。
//
// 运行:EDGE_BT=1 go test ./internal/app/ -run TestDirectionalExitPolicySim -v -timeout 600s
// 复用 directional_edge_backtest_test.go 的 OKX 公共 K 线拉取。
//
// 口径:盈亏以「账户%」(=价格变动% × 杠杆)累计,扣每腿成本(账户% = 单腿成本bps × 杠杆)。
// 关闭骑乘加仓(MaxAdds=0)以保证两套配置可公平比较;分批止盈按仓位比例分腿结算。

import (
	"fmt"
	"os"
	"testing"
	"time"

	"lingxi/internal/core"
)

type exitSimResult struct {
	trades  int
	wins    int
	netAcct float64 // 账户% 累计(已扣成本)
	grossTP int     // 走到快进快出止盈的次数
}

// simulateExitPolicy 在一组币种的收盘序列上回放入场+出场,返回净期望(账户%)。
// legCostFrac 为单腿成本(名义比例,如 0.0005=5bps);账户成本 = legCostFrac × 杠杆。
func simulateExitPolicy(cfg core.OKXDirectionalConfig, symbolCloses map[string][]float64, barMin int, legCostFrac float64) exitSimResult {
	lev := float64(cfg.Leverage)
	if lev <= 0 {
		lev = 5
	}
	legCostAcct := legCostFrac * lev
	stopBase := -cfg.StopLossPct
	if stopBase >= 0 {
		stopBase = -0.20
	}
	warm := cfg.SignalWindowBars
	if warm < cfg.EMASlow {
		warm = cfg.EMASlow
	}
	if warm < 30 {
		warm = 30
	}
	var res exitSimResult

	for _, closes := range symbolCloses {
		n := len(closes)
		type pos struct {
			side       DirectionSide
			entry      float64
			peak       float64
			openBar    int
			frac       float64 // 剩余仓位比例
			partialOn  bool
			realized   float64 // 该仓已实现账户%(含分批与成本)
		}
		var p *pos
		for i := warm; i < n; i++ {
			win := closes[:i+1]
			price := closes[i]
			if p == nil {
				sig := computeDirectionalSignal(win, 0, cfg)
				okConv := cfg.MinOpenConviction <= 0 || sig.Conviction >= cfg.MinOpenConviction
				if (sig.Side == DirectionLong || sig.Side == DirectionShort) && okConv {
					p = &pos{side: sig.Side, entry: price, peak: 0, openBar: i, frac: 1}
					p.realized -= legCostAcct // 入场成本
				}
				continue
			}
			uNot := directionalUPnLPct(p.side, p.entry, price)
			uAcct := uNot * lev
			if uAcct > p.peak {
				p.peak = uAcct
			}
			holdMin := (i - p.openBar) * barMin
			fresh := computeDirectionalSignal(win, 0, cfg)
			stopFloor := stopBase
			if p.partialOn {
				stopFloor = 0 // 分批后移到保本(镜像引擎)
			}
			act := decideDirectionalPositionAction(uAcct, p.peak, stopFloor, holdMin, p.side, fresh, p.partialOn, 0, cfg)
			switch act.Kind {
			case dirActClose:
				p.realized += p.frac*uAcct - legCostAcct*p.frac
				res.trades++
				res.netAcct += p.realized
				if p.realized > 0 {
					res.wins++
				}
				p = nil
			case dirActPartialTP:
				f := cfg.PartialTPFraction
				if f <= 0 || f >= 1 {
					f = 0.5
				}
				p.realized += f*uAcct - legCostAcct*f
				p.frac -= f
				p.partialOn = true
			case dirActScaleIn:
				// MaxAdds=0 时不会触发;保险起见当作持有。
			default:
				// hold
			}
		}
		// 末尾强平在场仓(按最后价),计成本。
		if p != nil {
			last := closes[n-1]
			uAcct := directionalUPnLPct(p.side, p.entry, last) * lev
			p.realized += p.frac*uAcct - legCostAcct*p.frac
			res.trades++
			res.netAcct += p.realized
			if p.realized > 0 {
				res.wins++
			}
		}
	}
	return res
}

// TestDirectionalScalpExitSweep 验证 GPT「时间尺度」出场建议:在 B1 后的实盘配置基线
// (5% 止损 / MaxHold30 / 信心闸0.70)上,扫描 StaleCutMinutes × QuickTPPct,
// 看「更早砍无正反馈单 + 更小止盈」是否净期望更优(还是把 30min 才兑现的赢单提前砍了)。
// 运行:EDGE_BT=1 go test ./internal/app/ -run TestDirectionalScalpExitSweep -v -timeout 600s
func TestDirectionalScalpExitSweep(t *testing.T) {
	if os.Getenv("EDGE_BT") == "" {
		t.Skip("研究用:设 EDGE_BT=1 且需外网 OKX 公共行情")
	}
	topN := 80
	if v := os.Getenv("EDGE_BT_TOPN"); v != "" {
		fmt.Sscanf(v, "%d", &topN)
	}
	barMin := 5
	syms, err := edgeBTTopSymbols(topN)
	if err != nil {
		t.Fatalf("取 symbols 失败: %v", err)
	}
	symbolCloses := make(map[string][]float64)
	for _, s := range syms {
		c, e := edgeBTCandles(s, "5m", 300)
		if e == nil && len(c) >= 60 {
			symbolCloses[s] = c
		}
		time.Sleep(60 * time.Millisecond)
	}
	t.Logf("%d 币种 5m K线", len(symbolCloses))

	// B1 后实盘基线配置。
	base := func() core.OKXDirectionalConfig {
		c := exitSimBaseCfg()
		c.StopLossPct = 0.05
		c.MinOpenConviction = 0.70
		c.MaxHoldMinutes = 30
		c.AbsMaxHoldMinutes = 60
		c.ReversalMinHoldMinutes = 30
		c.StaleCutMinProfitPct = 0
		return c
	}
	stales := []int{5, 10, 15, 40}
	quickTPs := []float64{0.06, 0.08, 0.12}
	// 双成本档:乐观(全 maker 5bps/腿)与现实(含急平吃单,8bps/腿)。
	for _, legBps := range []float64{5, 8} {
		legCost := legBps / 10000
		fmt.Printf("\n=== Scalp 出场扫描(单腿%.0fbps,账户口径×5)===\n", legBps)
		fmt.Printf("  %-30s %-6s %-9s %-9s %-6s\n", "配置", "笔数", "净账户%", "单笔均值%", "胜率")
		for _, st := range stales {
			for _, qt := range quickTPs {
				cfg := base()
				cfg.StaleCutMinutes = st
				cfg.QuickTPPct = qt
				r := simulateExitPolicy(cfg, symbolCloses, barMin, legCost)
				avg, wr := 0.0, 0.0
				if r.trades > 0 {
					avg = r.netAcct / float64(r.trades)
					wr = float64(r.wins) / float64(r.trades) * 100
				}
				name := fmt.Sprintf("stale%-2d QuickTP%.0f%%", st, qt*100)
				fmt.Printf("  %-30s %-6d %+8.2f %+8.3f %5.0f%%\n", name, r.trades, r.netAcct*100, avg*100, wr)
			}
		}
	}
}

// simulateExitPolicyFiltered 同 simulateExitPolicy,但可选只在指定 regime 入场(regimeOnly!=""),
// 用于检验「只做 range / 只做 trend」是否改变净期望。
func simulateExitPolicyFiltered(cfg core.OKXDirectionalConfig, symbolCloses map[string][]float64, barMin int, legCostFrac float64, regimeOnly string) exitSimResult {
	lev := float64(cfg.Leverage)
	if lev <= 0 {
		lev = 5
	}
	legCostAcct := legCostFrac * lev
	stopBase := -cfg.StopLossPct
	if stopBase >= 0 {
		stopBase = -0.20
	}
	warm := cfg.SignalWindowBars
	if warm < cfg.EMASlow {
		warm = cfg.EMASlow
	}
	if warm < 30 {
		warm = 30
	}
	var res exitSimResult
	for _, closes := range symbolCloses {
		n := len(closes)
		type pos struct {
			side      DirectionSide
			entry     float64
			peak      float64
			openBar   int
			frac      float64
			partialOn bool
			realized  float64
		}
		var p *pos
		for i := warm; i < n; i++ {
			win := closes[:i+1]
			price := closes[i]
			if p == nil {
				sig := computeDirectionalSignal(win, 0, cfg)
				okConv := cfg.MinOpenConviction <= 0 || sig.Conviction >= cfg.MinOpenConviction
				okReg := regimeOnly == "" || sig.Regime.String() == regimeOnly
				if (sig.Side == DirectionLong || sig.Side == DirectionShort) && okConv && okReg {
					p = &pos{side: sig.Side, entry: price, openBar: i, frac: 1}
					p.realized -= legCostAcct
				}
				continue
			}
			uAcct := directionalUPnLPct(p.side, p.entry, price) * lev
			if uAcct > p.peak {
				p.peak = uAcct
			}
			holdMin := (i - p.openBar) * barMin
			fresh := computeDirectionalSignal(win, 0, cfg)
			stopFloor := stopBase
			if p.partialOn {
				stopFloor = 0
			}
			act := decideDirectionalPositionAction(uAcct, p.peak, stopFloor, holdMin, p.side, fresh, p.partialOn, 0, cfg)
			switch act.Kind {
			case dirActClose:
				p.realized += p.frac*uAcct - legCostAcct*p.frac
				res.trades++
				res.netAcct += p.realized
				if p.realized > 0 {
					res.wins++
				}
				p = nil
			case dirActPartialTP:
				f := cfg.PartialTPFraction
				if f <= 0 || f >= 1 {
					f = 0.5
				}
				p.realized += f*uAcct - legCostAcct*f
				p.frac -= f
				p.partialOn = true
			}
		}
		if p != nil {
			last := closes[n-1]
			uAcct := directionalUPnLPct(p.side, p.entry, last) * lev
			p.realized += p.frac*uAcct - legCostAcct*p.frac
			res.trades++
			res.netAcct += p.realized
			if p.realized > 0 {
				res.wins++
			}
		}
	}
	return res
}

// TestDirectionalScalpEntryQuality 找净正配置:信心闸 × 出场风格(当前 vs 极简持到30min)× regime。
// 运行:EDGE_BT=1 go test ./internal/app/ -run TestDirectionalScalpEntryQuality -v -timeout 600s
func TestDirectionalScalpEntryQuality(t *testing.T) {
	if os.Getenv("EDGE_BT") == "" {
		t.Skip("研究用:设 EDGE_BT=1")
	}
	topN := 80
	if v := os.Getenv("EDGE_BT_TOPN"); v != "" {
		fmt.Sscanf(v, "%d", &topN)
	}
	barMin := 5
	syms, err := edgeBTTopSymbols(topN)
	if err != nil {
		t.Fatalf("symbols: %v", err)
	}
	symbolCloses := make(map[string][]float64)
	for _, s := range syms {
		c, e := edgeBTCandles(s, "5m", 300)
		if e == nil && len(c) >= 60 {
			symbolCloses[s] = c
		}
		time.Sleep(60 * time.Millisecond)
	}
	t.Logf("%d 币种 5m K线", len(symbolCloses))

	base := func() core.OKXDirectionalConfig {
		c := exitSimBaseCfg()
		c.StopLossPct = 0.05
		c.MaxHoldMinutes = 30
		c.AbsMaxHoldMinutes = 30
		c.ReversalMinHoldMinutes = 30
		c.StaleCutMinutes = 40
		return c
	}
	// 极简出场:只保留硬止损 + 持到 MaxHold 强平,关掉停滞/反转/棘轮/分批/快止盈。
	minimal := func(c *core.OKXDirectionalConfig) {
		c.StaleCutMinutes = 9999
		c.ReversalMinHoldMinutes = 99999
		c.QuickTPPct = 9.99
		c.BreakevenArmPct = 9.99
		c.PartialTPTriggerPct = 9.99
	}
	for _, legBps := range []float64{5, 8, 12} {
		legCost := legBps / 10000
		fmt.Printf("\n=== 极限信心扫描(极简持到30m,单腿%.0fbps)===\n", legBps)
		fmt.Printf("  %-22s %-6s %-9s %-9s %-6s\n", "配置", "笔数", "净账户%", "单笔均值%", "胜率")
		for _, conv := range []float64{0.85, 0.90, 0.95, 0.99} {
			cfg := base()
			cfg.MinOpenConviction = conv
			minimal(&cfg)
			r := simulateExitPolicyFiltered(cfg, symbolCloses, barMin, legCost, "")
			avg, wr := 0.0, 0.0
			if r.trades > 0 {
				avg = r.netAcct / float64(r.trades)
				wr = float64(r.wins) / float64(r.trades) * 100
			}
			fmt.Printf("  信心%.2f 极简持到30m   %-6d %+8.2f %+8.3f %5.0f%%\n", conv, r.trades, r.netAcct*100, avg*100, wr)
		}
	}
}

func exitSimBaseCfg() core.OKXDirectionalConfig {
	return core.OKXDirectionalConfig{
		SignalWindowBars: 24, TrendEfficiencyThreshold: 0.30,
		EMAFast: 9, EMASlow: 30, ZScoreEntry: 1.2,
		RequireFundingAlignment:     false,
		SpikeBarPct:                 0.03,
		TrendMaxExtensionPct:        0.02,
		VolExpansionMaxRatio:        1.8,
		ReversionKnifePct:           0.015,
		TrendEntryPullbackMaxBarPct: 0.005,
		StopLossPct:                 0.20,
		Leverage:                    5,
		MaxConcurrent:               3,
		MaxAdds:                     0, // 关闭骑乘加仓,公平比较
		AddConvictionMin:            0.6,
		PartialTPFraction:           0.5,
		PartialTPTriggerPct:         0.06,
		TrailLockFraction:           0.5,
		BreakevenArmPct:             0.05,
		TrailGapPct:                 0.06,
		QuickTPPct:                  0.12,
		StaleCutMinutes:             40,
		StaleCutMinProfitPct:        0,
		MaxHoldMinutes:              60,
		AbsMaxHoldMinutes:           180,
	}
}

func TestDirectionalExitPolicySim(t *testing.T) {
	if os.Getenv("EDGE_BT") == "" {
		t.Skip("研究用:设 EDGE_BT=1 且需外网 OKX 公共行情")
	}
	topN := 80
	if v := os.Getenv("EDGE_BT_TOPN"); v != "" {
		fmt.Sscanf(v, "%d", &topN)
	}
	bar := "5m"
	if v := os.Getenv("EDGE_BT_BAR"); v != "" {
		bar = v
	}
	barMin := 5
	switch bar {
	case "15m":
		barMin = 15
	case "1H":
		barMin = 60
	}
	legCost := 0.0005 // 单腿 5bps(maker+小滑点);可用 EDGE_BT_LEGBPS 覆盖
	if v := os.Getenv("EDGE_BT_LEGBPS"); v != "" {
		var bps float64
		fmt.Sscanf(v, "%g", &bps)
		legCost = bps / 10000
	}

	syms, err := edgeBTTopSymbols(topN)
	if err != nil {
		t.Fatalf("取 symbols 失败: %v", err)
	}
	symbolCloses := make(map[string][]float64)
	for _, s := range syms {
		c, e := edgeBTCandles(s, bar, 300)
		if e == nil && len(c) >= 60 {
			symbolCloses[s] = c
		}
		time.Sleep(60 * time.Millisecond)
	}
	t.Logf("%d 币种 %s K线;单腿成本=%.1fbps(账户口径×%dx=%.2f%%/腿)", len(symbolCloses), bar, legCost*10000, 5, legCost*5*100)

	type variant struct {
		name string
		mut  func(*core.OKXDirectionalConfig)
	}
	variants := []variant{
		{"当前(QuickTP12 棘轮0.5 gap6 arm5 分批6)", func(c *core.OKXDirectionalConfig) {}},
		{"用户提案(QuickTP8 arm4 gap5)", func(c *core.OKXDirectionalConfig) {
			c.QuickTPPct = 0.08
			c.BreakevenArmPct = 0.04
			c.TrailGapPct = 0.05
		}},
		{"持有更久(QuickTP20 stale60 hold120)", func(c *core.OKXDirectionalConfig) {
			c.QuickTPPct = 0.20
			c.StaleCutMinutes = 60
			c.MaxHoldMinutes = 120
		}},
		{"无快进快出(只棘轮+分批,QuickTP99)", func(c *core.OKXDirectionalConfig) {
			c.QuickTPPct = 0.99
		}},
		{"信心闸0.60", func(c *core.OKXDirectionalConfig) { c.MinOpenConviction = 0.60 }},
		{"信心闸0.65(当前)", func(c *core.OKXDirectionalConfig) { c.MinOpenConviction = 0.65 }},
		{"信心闸0.70", func(c *core.OKXDirectionalConfig) { c.MinOpenConviction = 0.70 }},
		{"信心闸0.75", func(c *core.OKXDirectionalConfig) { c.MinOpenConviction = 0.75 }},
		{"信心闸0.80", func(c *core.OKXDirectionalConfig) { c.MinOpenConviction = 0.80 }},
		{"当前 stale40 hold60", func(c *core.OKXDirectionalConfig) { c.MinOpenConviction = 0.70; c.ReversalMinHoldMinutes = 30 }},
		{"stale60 hold90", func(c *core.OKXDirectionalConfig) { c.MinOpenConviction = 0.70; c.ReversalMinHoldMinutes = 30; c.StaleCutMinutes = 60; c.MaxHoldMinutes = 90 }},
		{"stale off hold90", func(c *core.OKXDirectionalConfig) { c.MinOpenConviction = 0.70; c.ReversalMinHoldMinutes = 30; c.StaleCutMinutes = 0; c.MaxHoldMinutes = 90 }},
		{"stale off hold120", func(c *core.OKXDirectionalConfig) { c.MinOpenConviction = 0.70; c.ReversalMinHoldMinutes = 30; c.StaleCutMinutes = 0; c.MaxHoldMinutes = 120 }},
	}
	fmt.Printf("\n=== 出场策略净期望对比(账户%%,已扣成本)===\n")
	for _, v := range variants {
		cfg := exitSimBaseCfg()
		v.mut(&cfg)
		r := simulateExitPolicy(cfg, symbolCloses, barMin, legCost)
		avg := 0.0
		wr := 0.0
		if r.trades > 0 {
			avg = r.netAcct / float64(r.trades)
			wr = float64(r.wins) / float64(r.trades) * 100
		}
		fmt.Printf("  %-38s 笔数=%-4d 净=%+8.2f%% 单笔均值=%+.3f%% 胜率=%.0f%%\n",
			v.name, r.trades, r.netAcct*100, avg*100, wr)
	}
}

// scratchAcc 一种 scratch 策略(或基线)在全样本上的累计。
type scratchAcc struct {
	trades  int
	wins    int
	sumWin  float64 // 盈仓账户% 合计
	sumLoss float64 // 亏仓账户% 合计(负)
}

func (a *scratchAcc) add(r float64) {
	a.trades++
	if r > 0 {
		a.wins++
		a.sumWin += r
	} else {
		a.sumLoss += r
	}
}

func (a *scratchAcc) net() float64 { return a.sumWin + a.sumLoss }
func (a *scratchAcc) pf() float64 {
	if a.sumLoss < 0 {
		return a.sumWin / -a.sumLoss
	}
	if a.sumWin > 0 {
		return 999
	}
	return 0
}

// simBaselineFromBar 从 5m 入场 bar j 起按生产出场逻辑回放到平仓,返回净账户%(已扣进出场成本)。
func simBaselineFromBar(cfg core.OKXDirectionalConfig, c5 []float64, j int, entry float64, side DirectionSide, lev, legCostAcct float64) float64 {
	stopBase := -cfg.StopLossPct
	if stopBase >= 0 {
		stopBase = -0.20
	}
	peak, frac, realized := 0.0, 1.0, -legCostAcct // 入场成本
	partialOn := false
	n := len(c5)
	for i := j + 1; i < n; i++ {
		uAcct := directionalUPnLPct(side, entry, c5[i]) * lev
		if uAcct > peak {
			peak = uAcct
		}
		holdMin := (i - j) * 5
		fresh := computeDirectionalSignal(c5[:i+1], 0, cfg)
		stopFloor := stopBase
		if partialOn {
			stopFloor = 0
		}
		act := decideDirectionalPositionAction(uAcct, peak, stopFloor, holdMin, side, fresh, partialOn, 0, cfg)
		switch act.Kind {
		case dirActClose:
			return realized + frac*uAcct - legCostAcct*frac
		case dirActPartialTP:
			f := cfg.PartialTPFraction
			if f <= 0 || f >= 1 {
				f = 0.5
			}
			realized += f*uAcct - legCostAcct*f
			frac -= f
			partialOn = true
		}
	}
	uAcct := directionalUPnLPct(side, entry, c5[n-1]) * lev
	return realized + frac*uAcct - legCostAcct*frac
}

// TestDirectionalScratch1mProbe (研究用,EDGE_BT=1,需外网)。
// 用 1m K 线在「5m 生产入场信号」之上,probe「成交后 W 内 MFE≤阈值就砍」的反事实净效果。
//   - 入场信号:把 1m 重采样成 5m(每5根取收盘)后跑 computeDirectionalSignal,对齐生产 5m 口径;
//   - 基线出场:decideDirectionalPositionAction 回放(同 simulateExitPolicy);
//   - scratch:若入场后 W 窗口内 MFE(账户%)≤阈值,则在窗口末以 1m 收盘价全平;否则走基线;
//   - 局限:1m 粒度 ⇒ 30s 无法表示(需实盘亚分钟埋点);maker/taker 历史K线无成交类型,无法分桶。
// 运行:EDGE_BT=1 go test ./internal/app/ -run TestDirectionalScratch1mProbe -v -timeout 600s
func TestDirectionalScratch1mProbe(t *testing.T) {
	if os.Getenv("EDGE_BT") == "" {
		t.Skip("研究用:设 EDGE_BT=1 且需外网 OKX 公共行情")
	}
	topN := 80
	if v := os.Getenv("EDGE_BT_TOPN"); v != "" {
		fmt.Sscanf(v, "%d", &topN)
	}
	legBps := 5.0
	if v := os.Getenv("EDGE_BT_LEGBPS"); v != "" {
		fmt.Sscanf(v, "%g", &legBps)
	}
	syms, err := edgeBTTopSymbols(topN)
	if err != nil {
		t.Fatalf("symbols: %v", err)
	}
	closes1m := make(map[string][]float64)
	for _, s := range syms {
		c, err := edgeBTCandles(s, "1m", 300) // 1m×300 ≈ 5 小时
		if err == nil && len(c) >= 60 {
			closes1m[s] = c
		}
		time.Sleep(40 * time.Millisecond)
	}
	t.Logf("%d 币种 1m K线(重采样5m入场)", len(closes1m))

	// 生产「实际生效」口径:zEntry=1.0 / minConv=0.55 / stop5% / maxHold60 / stale30。
	cfg := exitSimBaseCfg()
	cfg.ZScoreEntry = 1.0
	cfg.MinOpenConviction = 0.55
	cfg.StopLossPct = 0.05
	cfg.MaxHoldMinutes = 60
	cfg.StaleCutMinutes = 30
	lev := float64(cfg.Leverage)
	if lev <= 0 {
		lev = 5
	}
	legCostAcct := legBps / 10000 * lev

	windows := []struct {
		name string
		bars int // 1m 根数
	}{{"60s", 1}, {"120s", 2}, {"180s", 3}}
	thrs := []float64{0.0, 0.005, 0.01} // 正反馈阈值(账户口径%)

	baseline := &scratchAcc{}
	scratch := map[string]*scratchAcc{}
	type cutStat struct {
		loserCut    int
		loserSaved  float64 // Σ(scratch − baseline),亏仓上为正=改善
		winnerCut   int
		winnerLost  float64 // Σ(baseline − scratch),盈仓上为正=代价
		fired       int
	}
	cuts := map[string]*cutStat{}
	for _, w := range windows {
		for _, thr := range thrs {
			k := fmt.Sprintf("%s|%.1f%%", w.name, thr*100)
			scratch[k] = &scratchAcc{}
			cuts[k] = &cutStat{}
		}
	}

	warm := 30
	entries := 0
	for _, c1 := range closes1m {
		// 重采样 5m:取每个 5 分钟桶的收盘(1m 下标 4,9,14,...)。
		var c5 []float64
		for i := 4; i < len(c1); i += 5 {
			c5 = append(c5, c1[i])
		}
		n5 := len(c5)
		for j := warm; j < n5; j++ {
			sig := computeDirectionalSignal(c5[:j+1], 0, cfg)
			okConv := cfg.MinOpenConviction <= 0 || sig.Conviction >= cfg.MinOpenConviction
			if !((sig.Side == DirectionLong || sig.Side == DirectionShort) && okConv) {
				continue
			}
			entries++
			entry := c5[j]
			br := simBaselineFromBar(cfg, c5, j, entry, sig.Side, lev, legCostAcct)
			baseline.add(br)
			base1m := 5*j + 4 // 入场对应 1m 下标
			for _, w := range windows {
				endIdx := base1m + w.bars
				haveWin := endIdx < len(c1)
				mfe := 0.0
				if haveWin {
					for k := base1m + 1; k <= endIdx; k++ {
						u := directionalUPnLPct(sig.Side, entry, c1[k]) * lev
						if u > mfe {
							mfe = u
						}
					}
				}
				for _, thr := range thrs {
					key := fmt.Sprintf("%s|%.1f%%", w.name, thr*100)
					a := scratch[key]
					cc := cuts[key]
					if haveWin && mfe <= thr {
						// scratch 触发:窗口末 1m 收盘全平(进+出场成本)。
						sr := directionalUPnLPct(sig.Side, entry, c1[endIdx])*lev - legCostAcct*2
						a.add(sr)
						cc.fired++
						if br < 0 {
							cc.loserCut++
							cc.loserSaved += sr - br
						} else if br > 0 {
							cc.winnerCut++
							cc.winnerLost += br - sr
						}
					} else {
						a.add(br) // 不触发:走基线
					}
				}
			}
		}
	}

	fmt.Printf("\n=== 1m scratch probe(入场=5m生产信号,单腿%.0fbps×5,账户口径)===\n", legBps)
	fmt.Printf("入场样本=%d  基线: 笔数=%d 净=%+.2f%% PF=%.2f 胜率=%.0f%%\n",
		entries, baseline.trades, baseline.net()*100, baseline.pf(), float64(baseline.wins)/float64(max1(baseline.trades))*100)
	fmt.Printf("⚠ 30s 在 1m 粒度无法表示(需实盘亚分钟埋点);maker/taker 历史K线无成交类型,无法分桶。\n")
	fmt.Printf("%-14s %-7s %-8s %-7s | 触发  砍亏仓(改善%%)   误杀盈仓(代价%%)  净Δvs基线%%\n", "窗口", "阈值", "PF", "胜率")
	for _, w := range windows {
		for _, thr := range thrs {
			key := fmt.Sprintf("%s|%.1f%%", w.name, thr*100)
			a := scratch[key]
			cc := cuts[key]
			wr := float64(a.wins) / float64(max1(a.trades)) * 100
			fmt.Printf("%-14s %-7s %-8.2f %-6.0f%% | %-4d  %-3d(%+.2f)      %-3d(%+.2f)     %+.2f\n",
				w.name, fmt.Sprintf("MFE≤%.1f%%", thr*100), a.pf(), wr,
				cc.fired, cc.loserCut, cc.loserSaved*100, cc.winnerCut, cc.winnerLost*100,
				(a.net()-baseline.net())*100)
		}
	}
}

func max1(n int) int {
	if n < 1 {
		return 1
	}
	return n
}

func (a *scratchAcc) avgWin() float64 {
	if a.wins == 0 {
		return 0
	}
	return a.sumWin / float64(a.wins)
}
func (a *scratchAcc) avgLoss() float64 {
	l := a.trades - a.wins
	if l == 0 {
		return 0
	}
	return a.sumLoss / float64(l)
}

// TestDirectionalEntryConfirmProbe (研究用,EDGE_BT=1,需外网)。
// 测「过滤坏入场」而非「砍坏仓」:5m 生产信号只作候选,等 1m 反转确认再进,对比立即进。
//   - immediate:5m 信号即在 c5[j] 收盘进(现状);
//   - confirm_turn:信号后最多等 K 根 1m,出现 1 根有利方向 1m(多:上涨/空:下跌)才进,否则放弃(miss);
//   - confirm_2up:更严,需连续 2 根有利 1m(持续转向)才进;
//   - 出场统一用生产 decideDirectionalPositionAction(从入场后回放)。
// 报告每策略:入场数 / 错过率 / 净% / PF / 胜率 / 均盈 / 均亏。
// 局限:spread 稳定性 / bid-ask 移动 / 跨所反向 历史K线无法测(只能实盘),本 probe 只测「价格反转确认」;
//       30s 在 1m 粒度无法表示;maker/taker 不可从历史K线分桶。
// 运行:EDGE_BT=1 go test ./internal/app/ -run TestDirectionalEntryConfirmProbe -v -timeout 600s
func TestDirectionalEntryConfirmProbe(t *testing.T) {
	if os.Getenv("EDGE_BT") == "" {
		t.Skip("研究用:设 EDGE_BT=1 且需外网 OKX 公共行情")
	}
	topN := 80
	if v := os.Getenv("EDGE_BT_TOPN"); v != "" {
		fmt.Sscanf(v, "%d", &topN)
	}
	legBps := 5.0
	if v := os.Getenv("EDGE_BT_LEGBPS"); v != "" {
		fmt.Sscanf(v, "%g", &legBps)
	}
	K := 3 // 最多等 3 根 1m(≈180s)确认
	if v := os.Getenv("EDGE_BT_CONFIRM_K"); v != "" {
		fmt.Sscanf(v, "%d", &K)
	}
	syms, err := edgeBTTopSymbols(topN)
	if err != nil {
		t.Fatalf("symbols: %v", err)
	}
	closes1m := make(map[string][]float64)
	for _, s := range syms {
		c, err := edgeBTCandles(s, "1m", 300)
		if err == nil && len(c) >= 60 {
			closes1m[s] = c
		}
		time.Sleep(40 * time.Millisecond)
	}
	t.Logf("%d 币种 1m K线(确认探针,K=%d)", len(closes1m), K)

	cfg := exitSimBaseCfg()
	cfg.ZScoreEntry = 1.0
	cfg.MinOpenConviction = 0.55
	cfg.StopLossPct = 0.05
	cfg.MaxHoldMinutes = 60
	cfg.StaleCutMinutes = 30
	lev := float64(cfg.Leverage)
	if lev <= 0 {
		lev = 5
	}
	legCostAcct := legBps / 10000 * lev

	imm := &scratchAcc{}
	turn := &scratchAcc{}
	two := &scratchAcc{}
	missTurn, missTwo := 0, 0
	warm, signals := 30, 0
	for _, c1 := range closes1m {
		var c5 []float64
		for i := 4; i < len(c1); i += 5 {
			c5 = append(c5, c1[i])
		}
		n5 := len(c5)
		for j := warm; j < n5; j++ {
			sig := computeDirectionalSignal(c5[:j+1], 0, cfg)
			okConv := cfg.MinOpenConviction <= 0 || sig.Conviction >= cfg.MinOpenConviction
			if !((sig.Side == DirectionLong || sig.Side == DirectionShort) && okConv) {
				continue
			}
			signals++
			side := sig.Side
			base := 5*j + 4
			imm.add(simBaselineFromBar(cfg, c5, j, c5[j], side, lev, legCostAcct))

			fav := func(idx int) bool { // 该 1m 是否有利方向
				if idx < 1 || idx >= len(c1) {
					return false
				}
				if side == DirectionLong {
					return c1[idx] > c1[idx-1]
				}
				return c1[idx] < c1[idx-1]
			}
			// confirm_turn:首根有利 1m 即进。
			tIdx := -1
			for k := 1; k <= K; k++ {
				idx := base + k
				if idx >= len(c1) {
					break
				}
				if fav(idx) {
					tIdx = idx
					break
				}
			}
			if tIdx < 0 {
				missTurn++
			} else {
				turn.add(simBaselineFromBar(cfg, c5, j, c1[tIdx], side, lev, legCostAcct))
			}
			// confirm_2up:连续 2 根有利 1m,进在第 2 根。
			twoIdx := -1
			for k := 1; k <= K; k++ {
				idx := base + k
				if idx >= len(c1) || idx < 1 {
					break
				}
				if fav(idx) && fav(idx-1) {
					twoIdx = idx
					break
				}
			}
			if twoIdx < 0 {
				missTwo++
			} else {
				two.add(simBaselineFromBar(cfg, c5, j, c1[twoIdx], side, lev, legCostAcct))
			}
		}
	}

	fmt.Printf("\n=== 入场确认 probe(5m信号候选 + 1m确认,K=%d,单腿%.0fbps×5,账户口径)===\n", K, legBps)
	fmt.Printf("总信号=%d  ⚠ spread/盘口/跨所 历史K线测不了(仅实盘);30s 测不了;maker/taker 不可分桶。\n", signals)
	fmt.Printf("%-14s %-6s %-8s %-8s %-7s %-9s %-9s\n", "策略", "错过%", "净%", "PF", "胜率", "均盈%", "均亏%")
	row := func(name string, a *scratchAcc, miss int) {
		entries := a.trades
		missRate := float64(miss) / float64(max1(signals)) * 100
		fmt.Printf("%-14s %-5.0f%% %-+8.2f %-8.2f %-6.0f%% %-+9.3f %-+9.3f (入场%d)\n",
			name, missRate, a.net()*100, a.pf(), float64(a.wins)/float64(max1(entries))*100,
			a.avgWin()*100, a.avgLoss()*100, entries)
	}
	row("immediate", imm, 0)
	row("confirm_turn", turn, missTurn)
	row("confirm_2up", two, missTwo)
	fmt.Printf("判读:看确认后 PF/胜率/均盈亏是否比 immediate 改善,以及错过率代价是否可接受。\n")
}
