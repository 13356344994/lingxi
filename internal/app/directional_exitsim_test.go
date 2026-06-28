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
