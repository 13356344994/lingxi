package app

import (
	"context"
	"fmt"
	"math"
	"time"
)

// margin_health.go 是 S2「持仓期常驻监控」的负 carry 保证金腿地基（M-S2.4 / M0.3b-）。
//
// 现状（已核对代码）：负 carry 现货腿走全仓杠杆(借币卖出做空)，其账户健康(marginLevel)在 cross-margin
// 实时 WS 迁移完成前没有实时流(见 user_data_stream.go 说明)。本里程碑用 REST 轮询兜底(诚实、可立即交付)：
// 仅在持有负 carry 敞口期间常驻轮询 /sapi/v1/margin/account，算出 marginLevel(总资产/总负债)；跌破
// MinMarginLevel 即对托管敞口保守撤离(panic flatten + 熔断冷却)。WS 实时流作为后续独立项。
//
// opt-in：MinMarginLevel<=0 时整条守卫为空操作(零行为变更)。

const (
	marginHealthPollInterval = 5 * time.Second
	// marginHealthStaleAfter：margin 数据超过此时延即视为不可信，守卫不据此动作(避免拿过期数据误撤离)。
	marginHealthStaleAfter = 30 * time.Second
)

// crossMarginLevel 是纯函数：由全仓净资产(BTC)与总负债(BTC)算出 marginLevel = 总资产/总负债。
// 总资产 = 净资产 + 总负债。无负债(<=0)时返回 +Inf(无借币 = 无强平风险)。
func crossMarginLevel(netAssetBTC, liabilityBTC float64) float64 {
	if liabilityBTC <= 0 {
		return math.Inf(1)
	}
	return (netAssetBTC + liabilityBTC) / liabilityBTC
}

// startMarginHealthPoller 启动负 carry 保证金腿健康轮询（进程内只起一次，跟随 runCtx）。
func (e *FundingRateSniperEngine) startMarginHealthPoller(ctx context.Context) {
	if e == nil {
		return
	}
	e.marginHealthOnce.Do(func() {
		go e.runMarginHealthPoller(ctx)
	})
}

func (e *FundingRateSniperEngine) runMarginHealthPoller(ctx context.Context) {
	ticker := time.NewTicker(marginHealthPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		// 仅在持有负 carry 敞口时轮询，避免无谓的 REST 调用。
		if !e.hasNegativeCarryManagedExposure() {
			continue
		}
		pollCtx, cancel := context.WithTimeout(ctx, marginHealthPollInterval)
		state, err := e.fetchCrossMarginAccountState(pollCtx)
		cancel()
		if err != nil {
			if e.logger != nil {
				e.logger.Printf("负 carry 保证金腿健康轮询失败(沿用上次快照): %v", err)
			}
			continue
		}
		e.noteFeedFrame(feedMarginHealth)
		// 优先用币安返回的权威 marginLevel；缺失(<=0)时用净资产/负债自行推算兜底。
		level := state.MarginLevel
		if level <= 0 {
			level = crossMarginLevel(state.TotalNetAssetBTC, state.TotalLiabilityBTC)
		}
		e.platformRiskMu.Lock()
		e.marginHealthLevel = level
		e.marginHealthLiabilityBTC = state.TotalLiabilityBTC
		e.lastMarginHealthAt = time.Now().UTC()
		e.platformRiskMu.Unlock()
	}
}

// hasNegativeCarryManagedExposure 报告当前托管敞口里是否存在负 carry(全仓杠杆借币空头)腿。
func (e *FundingRateSniperEngine) hasNegativeCarryManagedExposure() bool {
	if e == nil {
		return false
	}
	e.platformRiskMu.Lock()
	defer e.platformRiskMu.Unlock()
	return e.hasNegativeCarryManagedExposureLocked()
}

func (e *FundingRateSniperEngine) hasNegativeCarryManagedExposureLocked() bool {
	return e.portfolioManager.hasNegativeCarryHedge()
}

// shouldTriggerMarginHealthWithdraw 判定负 carry 保证金腿是否已危险到必须保守撤离(M0.3b-)：在持有负 carry
// 敞口、且有新鲜 margin 健康数据的前提下，marginLevel 跌破 MinMarginLevel 时抢占全局 panic 在途守卫并返回
// 撤离理由，由调用方走 panicFlattenManagedExposure(方向感知双腿全平 + 熔断冷却)。opt-in，关闭时恒不触发。
//
// 安全契约：MinMarginLevel<=0 关闭;无负 carry 敞口不触发;margin 数据缺失/过期(超 marginHealthStaleAfter)
// 不据此动作(避免拿过期数据误撤离——数据失明本身可由 R32 类机制处置)。
func (e *FundingRateSniperEngine) shouldTriggerMarginHealthWithdraw() (string, bool) {
	if e == nil {
		return "", false
	}
	minLevel := e.cfg.RiskConfig.MinMarginLevel
	if minLevel <= 0 {
		return "", false
	}
	e.platformRiskMu.Lock()
	defer e.platformRiskMu.Unlock()
	if e.panicFlattenInFlight {
		return "", false
	}
	if !e.hasNegativeCarryManagedExposureLocked() {
		return "", false
	}
	if e.lastMarginHealthAt.IsZero() {
		return "", false
	}
	if time.Now().UTC().Sub(e.lastMarginHealthAt) > marginHealthStaleAfter {
		return "", false
	}
	if e.marginHealthLevel >= minLevel {
		return "", false
	}
	e.panicFlattenInFlight = true
	return fmt.Sprintf("负 carry 保证金腿健康跌破阈值保守撤离: margin_level=%.4f min=%.4f liability_btc=%.8f",
		e.marginHealthLevel, minLevel, e.marginHealthLiabilityBTC), true
}
