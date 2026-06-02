package main

import (
	"errors"
	"strings"
)

// ExecutionStatus 是系统内部统一的订单执行语义。
type ExecutionStatus string

const (
	ExecutionStatusUnknown         ExecutionStatus = "unknown"
	ExecutionStatusUncertain       ExecutionStatus = "uncertain"
	ExecutionStatusNew             ExecutionStatus = "new"
	ExecutionStatusPartiallyFilled ExecutionStatus = "partially_filled"
	ExecutionStatusFilled          ExecutionStatus = "filled"
	ExecutionStatusCanceled        ExecutionStatus = "canceled"
	ExecutionStatusExpired         ExecutionStatus = "expired"
	ExecutionStatusRejected        ExecutionStatus = "rejected"
)

// ExecutionSemanticView 是平台原始订单结果映射后的统一只读视图。
type ExecutionSemanticView struct {
	Market        string
	Symbol        string
	ClientOrderID string
	OrderID       int64
	RawStatus     string
	Status        ExecutionStatus
	ExecutedQty   float64
	CumQuoteQty   float64
	Confirmed     bool
	Terminal      bool
	Closed        bool
	Uncertain     bool
}

// ExecutionErrorSemantic 用于统一 unknown/un­certain 类错误的执行语义。
type ExecutionErrorSemantic struct {
	Status        ExecutionStatus
	Market        string
	Symbol        string
	ClientOrderID string
	Reason        string
	Uncertain     bool
}

func normalizeExecutionStatus(status string) ExecutionStatus {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "NEW":
		return ExecutionStatusNew
	case "PARTIALLY_FILLED":
		return ExecutionStatusPartiallyFilled
	case "FILLED":
		return ExecutionStatusFilled
	case "CANCELED":
		return ExecutionStatusCanceled
	case "EXPIRED":
		return ExecutionStatusExpired
	case "REJECTED":
		return ExecutionStatusRejected
	default:
		return ExecutionStatusUnknown
	}
}

func isTerminalExecutionStatus(status ExecutionStatus) bool {
	switch status {
	case ExecutionStatusFilled, ExecutionStatusCanceled, ExecutionStatusExpired, ExecutionStatusRejected:
		return true
	default:
		return false
	}
}

func isClosedExecutionStatus(status ExecutionStatus) bool {
	return isTerminalExecutionStatus(status)
}

func isConfirmedExecution(orderID int64, executedQty float64, status ExecutionStatus) bool {
	return orderID != 0 && (executedQty > executionQtyTolerance || isClosedExecutionStatus(status))
}

func semanticViewFromSnapshot(snapshot OrderExecutionSnapshot) ExecutionSemanticView {
	status := normalizeExecutionStatus(snapshot.Status)
	return ExecutionSemanticView{
		Market:        strings.TrimSpace(snapshot.Market),
		Symbol:        strings.TrimSpace(snapshot.Symbol),
		ClientOrderID: strings.TrimSpace(snapshot.ClientOrderID),
		OrderID:       snapshot.OrderID,
		RawStatus:     strings.TrimSpace(snapshot.Status),
		Status:        status,
		ExecutedQty:   snapshot.ExecutedQty,
		CumQuoteQty:   snapshot.CumQuoteQty,
		Confirmed:     isConfirmedExecution(snapshot.OrderID, snapshot.ExecutedQty, status),
		Terminal:      isTerminalExecutionStatus(status),
		Closed:        isClosedExecutionStatus(status),
		Uncertain:     status == ExecutionStatusUnknown || status == ExecutionStatusUncertain,
	}
}

func semanticViewFromSpotOrder(order SpotOrderResponse) ExecutionSemanticView {
	status := normalizeExecutionStatus(order.Status)
	return ExecutionSemanticView{
		Market:        "spot",
		Symbol:        strings.TrimSpace(order.Symbol),
		ClientOrderID: strings.TrimSpace(order.ClientOrderID),
		OrderID:       order.OrderID,
		RawStatus:     strings.TrimSpace(order.Status),
		Status:        status,
		ExecutedQty:   parseFloat(order.ExecutedQty),
		CumQuoteQty:   parseFloat(order.CummulativeQuoteQty),
		Confirmed:     isConfirmedExecution(order.OrderID, parseFloat(order.ExecutedQty), status),
		Terminal:      isTerminalExecutionStatus(status),
		Closed:        isClosedExecutionStatus(status),
		Uncertain:     status == ExecutionStatusUnknown || status == ExecutionStatusUncertain,
	}
}

func semanticViewFromFuturesResult(result FuturesBatchOrderResult) ExecutionSemanticView {
	status := normalizeExecutionStatus(result.Status)
	return ExecutionSemanticView{
		Market:        "futures",
		Symbol:        strings.TrimSpace(result.Symbol),
		ClientOrderID: strings.TrimSpace(result.ClientOrderID),
		OrderID:       result.OrderID,
		RawStatus:     strings.TrimSpace(result.Status),
		Status:        status,
		ExecutedQty:   parseFloat(result.ExecutedQty),
		Confirmed:     isConfirmedExecution(result.OrderID, parseFloat(result.ExecutedQty), status),
		Terminal:      isTerminalExecutionStatus(status),
		Closed:        isClosedExecutionStatus(status),
		Uncertain:     status == ExecutionStatusUnknown || status == ExecutionStatusUncertain,
	}
}

func semanticViewFromExecutionError(err error) ExecutionErrorSemantic {
	if err == nil {
		return ExecutionErrorSemantic{}
	}
	var unknown unknownOrderStateError
	if errors.As(err, &unknown) {
		return ExecutionErrorSemantic{
			Status:        ExecutionStatusUnknown,
			Market:        strings.TrimSpace(unknown.Market),
			Symbol:        strings.TrimSpace(unknown.Symbol),
			ClientOrderID: strings.TrimSpace(unknown.ClientOrderID),
			Reason:        strings.TrimSpace(unknown.Reason),
			Uncertain:     true,
		}
	}
	var blind blindStateError
	if errors.As(err, &blind) {
		return ExecutionErrorSemantic{
			Status:        ExecutionStatusUncertain,
			Market:        strings.TrimSpace(blind.Market),
			Symbol:        strings.TrimSpace(blind.Symbol),
			ClientOrderID: strings.TrimSpace(blind.ClientOrderID),
			Reason:        strings.TrimSpace(blind.Reason),
			Uncertain:     true,
		}
	}
	if isUnknownOrderRequestError(err) {
		return ExecutionErrorSemantic{
			Status:    ExecutionStatusUncertain,
			Reason:    strings.TrimSpace(err.Error()),
			Uncertain: true,
		}
	}
	return ExecutionErrorSemantic{
		Status: ExecutionStatusUnknown,
		Reason: strings.TrimSpace(err.Error()),
	}
}
