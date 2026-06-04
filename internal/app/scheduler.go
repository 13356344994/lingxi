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

func applyLead(t time.Time, lead time.Duration) time.Time {
	if lead <= 0 {
		return t
	}
	return t.Add(-lead)
}
