package app

import (
	"context"
	"fmt"
	"log"
	"strings"

	"lingxi/internal/money"
	"lingxi/internal/reconcile"
)

// reconcileSummary 汇总「实际 vs 模型预期」对账的总量信息，用于给出可读结论。
type reconcileSummary struct {
	Cycles               int
	ActualNetTotal       float64
	ExpectedNetTotal     float64
	ActualFundingTotal   float64
	ExpectedFundingTotal float64
	FrictionTotal        float64
	WinningCycles        int
}

// buildLiveVsExpectedReconcileRecords 把已落盘的结算记录映射成 reconcile 框架的两侧输入：
//   - 实际侧(live)：完全取自落盘真实值（净利 / 资金费实收 / 交易盈亏 / 手续费）。
//   - 模型预期侧(expected)：把资金费换成开仓时的「预期资金费」，交易盈亏按 delta 中性对冲假设取 0，
//     手续费 / 借币沿用实际值（逐周期预测费用未单独落盘，置同值以免凭空制造噪声）。
//
// 于是 net_pnl 的偏差恰好 = 「资金费预测误差 + 对冲/执行的交易盈亏泄漏」，funding_income 的偏差 =
// 资金费预测误差——这正是小额实盘验证最该盯的两件事：钱到底是不是按模型假设赚到的、滑点/对冲泄漏多大。
// 仅纳入已定稿(funding 实收已结算)的周期；仍 pending 的周期净利未定稿，跳过以免污染对账。
func buildLiveVsExpectedReconcileRecords(records []SettlementCSVRecord) (live, expected []reconcile.TradeRecord) {
	for _, record := range records {
		if record.FundingIncomePending {
			continue
		}
		key := strings.TrimSpace(record.RunID) + "/" + strings.TrimSpace(record.CycleID)
		cycleID := strings.TrimSpace(record.CycleID)
		// R23 修复:结算记录是 cycle 级(可含多 symbol,见 Targets),没有单一 symbol。过去把 CycleID 塞进 Symbol
		// 字段导致诊断语义混淆;现在 CycleID 走专用字段,Symbol 留空(无意义不强填)。
		friction := money.Sum(
			parseFloat(record.TotalOpenSlippageUSDT),
			parseFloat(record.TotalCloseSlippageUSDT),
			parseFloat(record.TotalRecoveryFrictionUSDT),
		).USDT()

		actualFunding := parseFloat(record.FundingIncome)
		expectedFunding := parseFloat(record.ExpectedFundingIncome)
		tradingPnL := money.Sum(parseFloat(record.SpotTradingPnL), parseFloat(record.FuturesTradingPnL)).USDT()
		totalFees := parseFloat(record.TotalFees)
		totalBorrow := parseFloat(record.TotalBorrowCosts)
		actualNet := parseFloat(record.NetPnL)
		notional := parseFloat(record.CycleNotionalUSDT)
		// 成本侧用「模型预期手续费」(M-S3.1,R6):不再把实测手续费抄到预期侧,从而让 total_fees 字段的
		// 偏差暴露成本侧模型误差(BNB 抵扣/挂单返佣/部分成交)。历史记录无该字段时回退到实测费用(旧行为,零回归)。
		expectedFees := totalFees
		if strings.TrimSpace(record.ExpectedTotalFees) != "" {
			expectedFees = parseFloat(record.ExpectedTotalFees)
		}
		// 模型预期净利：预期资金费 + 0(delta 中性) - 模型预期手续费 - 实际借币成本(借币侧本身即模型推导,
		// 暂无独立实测可比口径,故沿用实测值)。
		expectedNet := money.Sum(expectedFunding, -expectedFees, -totalBorrow).USDT()

		live = append(live, reconcile.TradeRecord{
			Key:           key,
			CycleID:       cycleID,
			NetPnL:        actualNet,
			FundingIncome: actualFunding,
			TradingPnL:    tradingPnL,
			TotalFees:     totalFees,
			Friction:      friction,
			Notional:      notional,
		})
		expected = append(expected, reconcile.TradeRecord{
			Key:           key,
			CycleID:       cycleID,
			NetPnL:        expectedNet,
			FundingIncome: expectedFunding,
			TradingPnL:    0,
			TotalFees:     expectedFees,
			// 模型理想执行摩擦为 0(零滑点 delta 中性);live.Friction - 0 即执行泄漏,仅展示不卡 breach。
			Friction: 0,
			Notional: notional,
		})
	}
	return live, expected
}

// summarizeReconcile 汇总两侧记录的总量与胜率。live 与 expected 一一对应（同序、同 Key）。
func summarizeReconcile(live, expected []reconcile.TradeRecord) reconcileSummary {
	summary := reconcileSummary{Cycles: len(live)}
	var actualNet, expectedNet, actualFunding, expectedFunding, friction money.Micros
	for i := range live {
		actualNet = actualNet.Add(money.FromUSDT(live[i].NetPnL))
		actualFunding = actualFunding.Add(money.FromUSDT(live[i].FundingIncome))
		friction = friction.Add(money.FromUSDT(live[i].Friction))
		if live[i].NetPnL > 0 {
			summary.WinningCycles++
		}
	}
	summary.FrictionTotal = friction.USDT()
	for i := range expected {
		expectedNet = expectedNet.Add(money.FromUSDT(expected[i].NetPnL))
		expectedFunding = expectedFunding.Add(money.FromUSDT(expected[i].FundingIncome))
	}
	summary.ActualNetTotal = actualNet.USDT()
	summary.ExpectedNetTotal = expectedNet.USDT()
	summary.ActualFundingTotal = actualFunding.USDT()
	summary.ExpectedFundingTotal = expectedFunding.USDT()
	return summary
}

// formatReconcileReport 把对账结果渲染成可读文本，给出 HEALTHY/UNHEALTHY 结论与小额实盘下一步建议。
func formatReconcileReport(report reconcile.Report, summary reconcileSummary, thresholds reconcile.Thresholds) string {
	var b strings.Builder
	b.WriteString("===== 实盘 ↔ 模型预期 对账报告 =====\n")
	if summary.Cycles == 0 {
		b.WriteString("无已定稿的结算周期可对账（还没有完成 funding 实收结算的周期）。\n")
		b.WriteString("建议：先用最小档位(SmallCapital)跑出若干完整周期，再回来运行 reconcile。\n")
		return b.String()
	}
	winRate := float64(summary.WinningCycles) / float64(summary.Cycles) * 100
	netGap := summary.ActualNetTotal - summary.ExpectedNetTotal
	fundingGap := summary.ActualFundingTotal - summary.ExpectedFundingTotal

	b.WriteString(fmt.Sprintf("已结算周期: %d  其中盈利周期: %d  胜率: %.1f%%\n", summary.Cycles, summary.WinningCycles, winRate))
	b.WriteString(fmt.Sprintf("实盘累计净利: %+.4f USDT   模型预期净利: %+.4f USDT   净利偏差: %+.4f USDT\n", summary.ActualNetTotal, summary.ExpectedNetTotal, netGap))
	b.WriteString(fmt.Sprintf("资金费实收累计: %+.4f USDT   预期资金费累计: %+.4f USDT   资金费预测误差: %+.4f USDT\n", summary.ActualFundingTotal, summary.ExpectedFundingTotal, fundingGap))
	b.WriteString(fmt.Sprintf("执行摩擦累计(开/平滑点+恢复): %+.4f USDT（诊断:执行泄漏,已隐含在净利偏差里,不单独卡 HEALTHY）\n", summary.FrictionTotal))
	b.WriteString(fmt.Sprintf("配对周期: %d  逐周期偏差超阈值: %d  净利偏差绝对值之和: %.4f USDT\n", report.MatchedCount, report.BreachedCount, report.TotalNetAbsGap))
	if thresholds.AbsUSDT > 0 {
		b.WriteString(fmt.Sprintf("单字段绝对偏差阈值: %.4f USDT\n", thresholds.AbsUSDT))
	}
	if thresholds.Rel > 0 {
		b.WriteString(fmt.Sprintf("单字段相对偏差阈值: %.1f%%（已设相对阈值时以相对差为准,抓小名义大比例泄漏）\n", thresholds.Rel*100))
	}
	if thresholds.TotalAbsUSDT > 0 {
		b.WriteString(fmt.Sprintf("净利偏差绝对值之和阈值: %.4f USDT  总闸触发: %t\n", thresholds.TotalAbsUSDT, report.TotalBreached))
	}

	if report.BreachedCount > 0 {
		b.WriteString("\n--- 偏差超阈值的周期（实盘明显偏离模型假设，重点排查滑点/对冲泄漏/资金费预测）---\n")
		shown := 0
		for _, rec := range report.Records {
			if !rec.Breached {
				continue
			}
			b.WriteString(fmt.Sprintf("  cycle=%s 触发字段=%s\n", rec.CycleID, strings.Join(rec.Breaches, ",")))
			for _, gap := range rec.Gaps {
				b.WriteString(fmt.Sprintf("    %-14s 实际=%+.4f 预期=%+.4f 绝对偏差=%.4f 相对偏差=%.2f%%\n", gap.Field, gap.Live, gap.Backtest, gap.AbsGap, gap.RelGap*100))
			}
			shown++
			if shown >= 20 {
				b.WriteString("  …（仅展示前 20 个超阈值周期）\n")
				break
			}
		}
	}

	b.WriteString("\n结论: ")
	if report.Healthy {
		b.WriteString("HEALTHY —— 实盘与模型预期在阈值内一致。\n")
		if summary.ActualNetTotal <= 0 {
			b.WriteString("注意：虽然对账一致，但实盘累计净利非正——模型本身可能就没有正期望，需检视成本地板线/选币，而非执行。\n")
		} else {
			b.WriteString("可考虑按既定节奏小步放大档位，并持续运行 reconcile 监控偏差是否随规模漂移。\n")
		}
	} else {
		b.WriteString("UNHEALTHY —— 实盘系统性偏离模型假设，暂不可放大档位。\n")
		if len(report.OnlyLiveKeys) > 0 || len(report.OnlyBacktestKeys) > 0 {
			b.WriteString(fmt.Sprintf("存在未配对周期: 仅实盘=%d 仅预期=%d（应为 0，记录可能缺失/重复）。\n", len(report.OnlyLiveKeys), len(report.OnlyBacktestKeys)))
		}
		b.WriteString("优先排查：资金费预测误差(选币/估算)、交易盈亏泄漏(滑点/对冲不平衡)、手续费档位是否与假设一致。\n")
	}
	return b.String()
}

// runReconcileMode 是 `lingxi reconcile` 子命令：只读地把已落盘结算记录与模型预期对账并打印报告，
// 不触碰任何下单/实盘路径，可在实盘运行期间安全随时执行。
func runReconcileMode(_ context.Context) error {
	log.Printf("启动对账模式: %s", buildVersionSummary())
	cfg, err := loadEngineConfig()
	if err != nil {
		return fmt.Errorf("加载主配置失败: %w", err)
	}
	store, err := NewReportStore(cfg.NetworkConfig.RecordDir)
	if err != nil {
		return fmt.Errorf("初始化记录存储失败: %w", err)
	}
	records, err := store.ListSettlementRecords(0)
	if err != nil {
		return fmt.Errorf("读取结算记录失败: %w", err)
	}
	live, expected := buildLiveVsExpectedReconcileRecords(records)
	thresholds := reconcile.Thresholds{
		AbsUSDT:      cfg.RiskConfig.SettlementDiffAlertUSDT,
		Rel:          cfg.RiskConfig.SettlementDiffRate,
		TotalAbsUSDT: cfg.RiskConfig.SettlementTotalDiffAlertUSDT,
	}
	report := reconcile.Reconcile(live, expected, thresholds)
	summary := summarizeReconcile(live, expected)
	fmt.Print(formatReconcileReport(report, summary, thresholds))
	return nil
}
