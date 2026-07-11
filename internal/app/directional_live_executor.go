package app

import (
	"context"
	"errors"
	"time"

	"lingxi/internal/execution"
	"lingxi/internal/venue"
)

// ErrDirectionalOpenSealed 表示首战完全体重构期间自动方向新开仓被物理封死。
// verified reduce-only 平仓不受影响，确保系统始终可以收敛真实敞口。
var ErrDirectionalOpenSealed = errors.New("directional live opens are sealed")

// automaticLivePromotionSealed 将考试/自治生命周期与真钱晋级物理分离。
// 解除必须经过新的签名 scorecard 与用户单独批准，不能由旧监督器自动改控制面。
const automaticLivePromotionSealed = true

// legacyAutonomousResearchEnabled 关闭旧粒子组合挖掘/自动阅卷/自动生命周期。
// 原始粒子账本与方向 shadow 仍持续采集；新机制研究由密封考场接管。
const legacyAutonomousResearchEnabled = false

type directionalOrderExecutor interface {
	PlaceOrder(context.Context, venue.OrderRequest) (venue.OrderResult, error)
}

type guardedDirectionalOrderExecutor struct {
	venue       DirectionalVenue
	opensSealed bool
}

func newGuardedDirectionalOrderExecutor(v DirectionalVenue, opensSealed bool) directionalOrderExecutor {
	return &guardedDirectionalOrderExecutor{venue: v, opensSealed: opensSealed}
}

func (x *guardedDirectionalOrderExecutor) PlaceOrder(ctx context.Context, req venue.OrderRequest) (venue.OrderResult, error) {
	if x == nil || x.venue == nil {
		return venue.OrderResult{}, errors.New("directional order executor is not wired")
	}
	if !req.ReduceOnly && x.opensSealed {
		return venue.OrderResult{}, ErrDirectionalOpenSealed
	}
	return x.venue.PlaceOrder(ctx, req)
}

// placeDirectionalOrder 是方向主线唯一的新开仓下单入口。字段未接线时 fail-closed；
// reduce-only 仍允许直接使用 venue，以免升级过程阻断安全平仓。
func (e *OKXDirectionalEngine) placeDirectionalOrder(ctx context.Context, req venue.OrderRequest) (venue.OrderResult, error) {
	if e != nil && e.orderExecutor != nil {
		return e.orderExecutor.PlaceOrder(ctx, req)
	}
	if req.ReduceOnly && e != nil && e.v != nil {
		return e.v.PlaceOrder(ctx, req)
	}
	return venue.OrderResult{}, ErrDirectionalOpenSealed
}

func (e *OKXDirectionalEngine) acquireExecutionClaim(symbol, owner string) (func(), error) {
	if e == nil {
		return nil, execution.ErrSymbolClaimed
	}
	e.mu.Lock()
	if e.executionClaims == nil {
		e.executionClaims = execution.NewClaimManager()
	}
	claims := e.executionClaims
	e.mu.Unlock()
	return claims.TryAcquire(symbol, owner, time.Now().UTC())
}
