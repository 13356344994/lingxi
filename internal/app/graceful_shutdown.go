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

		reason := "ctx 取消(SIGTERM/Interrupt)"
		if cause != nil && !errors.Is(cause, context.Canceled) {
			reason = cause.Error()
		}
		active := e.snapshotActiveOpenHedges()
		if e.logger != nil {
			e.logger.Printf("[shutdown] 开始优雅停机收尾: 原因=%s 活跃对冲=%d", reason, len(active))
		}

		// 1) 停用交易所 dead-man 倒计时:优雅停机我们自己负责善后,避免旧倒计时误撤后续实例挂单。
		if e.cfg.RiskConfig.DeadMansSwitchEnabled {
			e.disarmDeadMansSwitch(ctx)
		}

		// 2) 落盘最终风控状态与持仓账本(best-effort,失败仅记日志)。
		if err := e.persistDailyRiskState(); err != nil && e.logger != nil {
			e.logger.Printf("[shutdown] 落盘日级风控状态失败: %v", err)
		}
		if err := e.syncPersistedOpenHedges(active); err != nil && e.logger != nil {
			e.logger.Printf("[shutdown] 落盘持仓账本失败: %v", err)
		}

		// 3) 发清晰的计划内停机事件 → 路由到告警/落盘。
		runID, cycleID := e.traceIDs()
		e.emitDomainEvents([]DomainEvent{{
			Type:       EventTypeGracefulShutdown,
			Level:      EventLevelWarning,
			OccurredAt: time.Now().UTC(),
			RunID:      runID,
			CycleID:    cycleID,
			Reason:     fmt.Sprintf("优雅停机: %s; 活跃对冲 %d 笔已落盘, dead-man 已停用", reason, len(active)),
		}})
		if e.logger != nil {
			e.logger.Printf("[shutdown] 优雅停机收尾完成")
		}
	})
}
