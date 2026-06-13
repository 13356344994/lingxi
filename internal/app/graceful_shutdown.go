package app

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// graceful_shutdown.go 实现 S7-C「优雅停机安全收尾」(R14):主循环因 SIGTERM/Interrupt 取消 ctx 退出时,
// 执行一次有界、幂等的善后——停用交易所 dead-man 倒计时、落盘最终风控状态与持仓账本、发清晰的计划内停机事件
// (便于外部看门狗区分「计划停机」与「崩溃」)。崩溃路径走不到这里,由 dead-man(S7-B)在交易所侧兜底撤单。

const gracefulShutdownTimeout = 10 * time.Second

// finalizeGracefulShutdown 执行一次有界的安全收尾。用独立的有界 context(runCtx 此时已取消),
// 避免收尾动作被同一取消信号立即打断。幂等:多次调用只执行一次。
func (e *FundingRateSniperEngine) finalizeGracefulShutdown(cause error) {
	if e == nil {
		return
	}
	e.shutdownOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), gracefulShutdownTimeout)
		defer cancel()

		// 区分「计划停机」(ctx 取消)与「异常退出/崩溃」(致命熔断/panic 等真实错误):前者 warning，
		// 后者 critical,且通知文案明确标注崩溃,便于运营者与外部看门狗区分处置。
		abnormal := cause != nil && !errors.Is(cause, context.Canceled)
		reason := "ctx 取消(SIGTERM/Interrupt)"
		if abnormal {
			reason = cause.Error()
		}
		active := e.snapshotActiveOpenHedges()
		if e.logger != nil {
			e.logger.Printf("[shutdown] 开始优雅停机收尾: 原因=%s 活跃对冲=%d 异常退出=%t", reason, len(active), abnormal)
		}

		// 1) 停用交易所 dead-man 倒计时:优雅停机我们自己负责善后,避免旧倒计时误撤后续实例挂单。
		if e.cfg.RiskConfig.DeadMansSwitchEnabled {
			e.disarmDeadMansSwitch(ctx)
		}

		// 2) 落盘最终风控状态与持仓账本(best-effort,失败仅记日志)。
		// 用**同步**落盘而非异步入队:停机时异步 writer 可能已随 ctx 退出,入队的最后一次计数变更会丢失,
		// 导致重启后日级亏损/下单计数被低估、绕过当日风控上限。flushDailyRiskStateNow 直接写盘保证必达。
		e.flushDailyRiskStateNow()
		if err := e.syncPersistedOpenHedges(active); err != nil && e.logger != nil {
			e.logger.Printf("[shutdown] 落盘持仓账本失败: %v", err)
		}

		// 3) 发清晰的停机/崩溃事件 → 路由到告警/落盘,并经 notifyLifecycle 保证必达 Telegram(不被 MinSeverity 过滤)。
		runID, cycleID := e.traceIDs()
		eventType := EventTypeGracefulShutdown
		level := EventLevelWarning
		title := "实盘引擎已停止(计划停机)"
		detail := fmt.Sprintf("计划停机: %s; 活跃对冲 %d 笔已落盘, dead-man 已停用", reason, len(active))
		if abnormal {
			eventType = EventTypeEngineCrashed
			level = EventLevelCritical
			title = "实盘引擎异常退出(崩溃)"
			detail = fmt.Sprintf("异常退出: %s; 活跃对冲 %d 笔已落盘, dead-man 已停用(交易所侧到期兜底撤单)。请尽快人工核查持仓与账户。", reason, len(active))
		}
		// 落盘 + 观测(不经 dispatcher.Notify,告警必达由下面的 notifyLifecycle 单路负责,避免重复推送)。
		e.persistDomainEvents([]DomainEvent{{
			Type:       eventType,
			Level:      level,
			OccurredAt: time.Now().UTC(),
			RunID:      runID,
			CycleID:    cycleID,
			Reason:     detail,
		}})
		// 计划停机仅落盘/本地日志;异常退出仍推 Telegram,便于与崩溃区分。
		if abnormal {
			e.notifyLifecycle(level, title, detail)
		}
		if e.logger != nil {
			e.logger.Printf("[shutdown] 停机收尾完成(异常退出=%t)", abnormal)
		}
	})
}
