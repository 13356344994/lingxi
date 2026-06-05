package app

import "time"

// cycleScheduler 把「一次狙击周期的时点判断」从 God object(FundingRateSniperEngine)中抽出为纯函数式组件
// (M-S2.2,R10/R21 解耦):仅依赖配置里的时点参数 + 输入时间,无引擎状态、无副作用、无锁,可独立单测。
//
// 它不改变任何既有时点口径——引擎对应方法改为薄包装委托给它,旧 timing 测试作回归锁。当前唤醒仍是全局
// :wakeMinute:wakeSecond(保持现状);逐币 funding 时钟(R31)是新功能,留作后续 opt-in,不在本步。
type cycleScheduler struct {
	wakeMinute            int
	wakeSecond            int
	scanWindow            time.Duration
	monitorStopLead       time.Duration
	openPreparationLead   time.Duration
	preOpenNoGoLead       time.Duration
	exitDelayAfterFunding time.Duration
	// revalidateLead 是逐币 funding 时钟(R31,§16.4)的续持复核提前量(即 :55):每个持仓在其自身
	// nextFundingTime − revalidateLead 触发 keep/drop 复核。仅滚动持有(EnableRollingHold)路径使用,
	// 旧全局 :57 单周期路径不受影响。
	revalidateLead time.Duration
}

func newCycleScheduler(cfg EngineConfig) cycleScheduler {
	return cycleScheduler{
		wakeMinute:            cfg.StrategyConfig.WakeMinute,
		wakeSecond:            cfg.StrategyConfig.WakeSecond,
		scanWindow:            cfg.StrategyConfig.ScanWindow,
		monitorStopLead:       cfg.StrategyConfig.MonitorStopLead,
		openPreparationLead:   cfg.ExecutionConfig.OpenPreparationLead,
		preOpenNoGoLead:       cfg.ExecutionConfig.PreOpenNoGoLead,
		exitDelayAfterFunding: cfg.ExecutionConfig.ExitDelayAfterFunding,
		revalidateLead:        cfg.PortfolioConfig.RevalidateLead,
	}
}

// NextWake 返回下一个整点 :wakeMinute:wakeSecond 唤醒时刻(全局唤醒,保持现状)。
func (s cycleScheduler) NextWake(now time.Time) time.Time {
	return nextWakeTime(now, s.wakeMinute, s.wakeSecond)
}

// ScanEligible 判断某 symbol 的下次结算是否落在扫描窗口内:0 < 剩余 <= scanWindow。
func (s cycleScheduler) ScanEligible(now, nextFundingTime time.Time) bool {
	remaining := nextFundingTime.Sub(now)
	return remaining > 0 && remaining <= s.scanWindow
}

// OpenFireAt 返回开仓触发时刻 = 最早结算时间 − MonitorStopLead。
func (s cycleScheduler) OpenFireAt(earliestFunding time.Time) time.Time {
	return earliestFunding.Add(-s.monitorStopLead)
}

// OpenPreparationStartAt = OpenFireAt − OpenPreparationLead(lead<=0 时即 OpenFireAt)。
func (s cycleScheduler) OpenPreparationStartAt(earliestFunding time.Time) time.Time {
	return applyLead(s.OpenFireAt(earliestFunding), s.openPreparationLead)
}

// OpenNoGoCheckAt = OpenFireAt − PreOpenNoGoLead(lead<=0 时即 OpenFireAt)。
func (s cycleScheduler) OpenNoGoCheckAt(earliestFunding time.Time) time.Time {
	return applyLead(s.OpenFireAt(earliestFunding), s.preOpenNoGoLead)
}

// CloseDeadline 返回平仓监控窗口的硬截止 = 结算时间 + ExitDelayAfterFunding。
func (s cycleScheduler) CloseDeadline(fundingTime time.Time) time.Time {
	return fundingTime.Add(s.exitDelayAfterFunding)
}

// symbolFundingClock 描述一个持仓的逐币资金费时钟:它自身的下次结算时间(R31,§16.4)。
// 不同合约结算间隔可能不同(8h/4h/可变),组合里各币结算时刻不同步,故续持复核必须逐币驱动而非全局单点。
type symbolFundingClock struct {
	Symbol          string
	NextFundingTime time.Time
}

// RevalidateAt 返回某持仓的续持复核时刻 = 其下次结算时间 − revalidateLead(逐币的 :55)。
// lead<=0 时即结算时刻本身。
func (s cycleScheduler) RevalidateAt(nextFundingTime time.Time) time.Time {
	return applyLead(nextFundingTime, s.revalidateLead)
}

// RevalidateDue 报告某持仓此刻是否落在续持复核窗口内:[RevalidateAt, nextFundingTime)。
// 复核窗口在结算前开启、结算时刻关闭——确保 keep/drop 决策在资金费实际发生之前完成。
func (s cycleScheduler) RevalidateDue(now, nextFundingTime time.Time) bool {
	if nextFundingTime.IsZero() {
		return false
	}
	return !now.Before(s.RevalidateAt(nextFundingTime)) && now.Before(nextFundingTime)
}

// NextPortfolioWake 返回组合下一个需要被唤醒处理的时刻:取「所有持仓各自的续持复核时刻」与「全局新开唤醒
// (找新币补位)」中的最早者,用于逐币 funding 时钟驱动主循环定时(取代全局单点 :57)。
//
// 若已有持仓落入复核窗口但尚未结算,返回 now(立即唤醒)。纯函数:无副作用、确定性、无锁。
func (s cycleScheduler) NextPortfolioWake(now time.Time, clocks []symbolFundingClock) time.Time {
	earliest := s.NextWake(now) // 全局新开唤醒兜底(始终存在,确保即使无持仓也按 :57 找币)
	for _, c := range clocks {
		if c.NextFundingTime.IsZero() {
			continue
		}
		if s.RevalidateDue(now, c.NextFundingTime) {
			return now // 已到复核窗口、未结算 → 立即唤醒做 keep/drop
		}
		if revalidate := s.RevalidateAt(c.NextFundingTime); revalidate.After(now) && revalidate.Before(earliest) {
			earliest = revalidate
		}
	}
	return earliest
}

func applyLead(t time.Time, lead time.Duration) time.Time {
	if lead <= 0 {
		return t
	}
	return t.Add(-lead)
}
