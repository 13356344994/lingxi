package binance

import (
	"context"
	"time"

	"lingxi/internal/venue"
)

// ReserveOrderBudget 委托币安 used-weight 预算闸(请求权重预算)。
func (a *Adapter) ReserveOrderBudget(units int) bool {
	_, _, ok := a.client.ReserveUsedWeightBudget(units)
	return ok
}

// ReleaseOrderBudget 归还预留的 used-weight 预算。
func (a *Adapter) ReleaseOrderBudget(units int) {
	a.client.ReleaseUsedWeightBudget(units)
}

// Classify 把币安错误归类。P1 接入现有 exchange.ClassifyException 的完整码表后替换此占位。
func (a *Adapter) Classify(err error) (category string, severity string) {
	if err == nil {
		return "", ""
	}
	return "request_error", "minor" // TODO(P1): 迁入 exchange.ClassifyException 码表
}

// ArmDeadMansSwitch 保持 ErrUnsupported:币安 countdownCancelAll 必须按 symbol 武装,
// venue 适配器无持仓/挂单上下文,无法表达「账户级兜底」语义;由 app 层 dead_mans_switch.go
// 持有持仓列表后逐 symbol 设置。HasNativeDeadMan=true 表示交易所有该能力,非本方法已实现。
func (a *Adapter) ArmDeadMansSwitch(ctx context.Context, countdown time.Duration) error {
	return venue.ErrUnsupported
}
