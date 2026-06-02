package main

import (
	"context"
	"fmt"
)

// ExecutionWorkflow 定义了所有开仓、平仓、恢复动作的统一步骤骨架。
// 目的是强制隔离：下单 -> 校验 -> 对账 -> 补偿 -> 升级。
type ExecutionWorkflow struct {
	engine *FundingRateSniperEngine
}

func newExecutionWorkflow(engine *FundingRateSniperEngine) *ExecutionWorkflow {
	return &ExecutionWorkflow{engine: engine}
}

// ExecutionWorkflowResult 统一下单动作生命周期返回
type ExecutionWorkflowResult struct {
	TargetSymbol    string
	TargetMarket    string // "spot" | "futures"
	ActionType      string // "open" | "close" | "recovery"
	InitialStatus   ExecutionStatus
	FinalStatus     ExecutionStatus
	ExecutedQty     float64
	IsSuccess       bool
	IsUncertain     bool
	IsEscalated     bool
	Summary         string
}

// Execute 统一动作执行骨架
func (w *ExecutionWorkflow) Execute(
	ctx context.Context,
	symbol string,
	market string,
	actionType string,
	submit func(context.Context) (ExecutionSemanticView, error),
	compensate func(context.Context, ExecutionSemanticView) (ExecutionSemanticView, error),
	escalate func(context.Context, ExecutionSemanticView, error) error,
) ExecutionWorkflowResult {

	result := ExecutionWorkflowResult{
		TargetSymbol: symbol,
		TargetMarket: market,
		ActionType:   actionType,
	}

	// 1. Submit (发单与观测)
	view, err := submit(ctx)
	result.InitialStatus = view.Status
	result.FinalStatus = view.Status
	result.ExecutedQty = view.ExecutedQty

	if err != nil {
		if isUnknownOrderRequestError(err) || isUnknownOrderStateError(err) {
			result.IsUncertain = true
			result.Summary = fmt.Sprintf("提交订单后状态未知: %v", err)
		} else {
			result.Summary = fmt.Sprintf("提交订单失败: %v", err)
		}
	} else if !view.Confirmed {
		result.IsUncertain = true
		result.Summary = "订单已提交但未获得确定性状态"
	}

	// 2. Compensate (部分成交/未知状态的回补与对账)
	if result.IsUncertain || (view.Status == ExecutionStatusPartiallyFilled) {
		if compensate != nil {
			compView, compErr := compensate(ctx, view)
			if compErr != nil {
				err = compErr
				result.Summary = fmt.Sprintf("补偿执行失败: %v", compErr)
			} else {
				view = compView
				result.FinalStatus = view.Status
				result.ExecutedQty = view.ExecutedQty
				result.IsUncertain = !view.Confirmed
				result.Summary = "补偿执行已完成"
			}
		}
	}

	// 3. Escalate (仍未决时强制升级)
	if result.IsUncertain || err != nil {
		result.IsSuccess = false
		if escalate != nil {
			escErr := escalate(ctx, view, err)
			if escErr != nil {
				result.IsEscalated = true
				result.Summary = fmt.Sprintf("动作升级失败并挂起: %v", escErr)
			} else {
				result.Summary = "动作已升级"
			}
		}
	} else {
		result.IsSuccess = true
		if result.Summary == "" {
			result.Summary = "动作执行成功"
		}
	}

	return result
}
