package app

import (
	"context"
	"fmt"
	"log"

	"lingxi/internal/venue"
)

type walletTransferer interface {
	TransferWallet(ctx context.Context, from, to string, amountUSDT float64) error
}

// CapitalRoutingExecResult 是 ExecuteCapitalRouting 的执行结果。
type CapitalRoutingExecResult struct {
	Executed                     int
	Errs                         []error
	FailedTransferOpportunityIDs []string // 所内划转失败、须阻断 OPEN 的机会 ID
}

// ExecuteCapitalRouting 执行资金路由计划中的所内划转;跨所建议仅记录日志,绝不执行。
// live==false 时只打 dry-run 日志,不调用任何划转接口。
func ExecuteCapitalRouting(ctx context.Context, plan CapitalRoutingPlan, venues map[string]venue.Venue, live bool, logger *log.Logger) CapitalRoutingExecResult {
	if logger == nil {
		logger = log.Default()
	}

	result := CapitalRoutingExecResult{}
	failedOpp := map[string]struct{}{}

	recordFailedTransfer := func(tr WalletTransfer) {
		for _, id := range tr.OpportunityIDs {
			if id == "" {
				continue
			}
			if _, ok := failedOpp[id]; ok {
				continue
			}
			failedOpp[id] = struct{}{}
			result.FailedTransferOpportunityIDs = append(result.FailedTransferOpportunityIDs, id)
		}
	}

	for _, tr := range plan.WithinVenueTransfers {
		from := string(tr.From)
		to := string(tr.To)
		if !live {
			logger.Printf("[dry-run] 本应划转 venue=%s %s→%s 金额=%.4f 原因=%s", tr.Venue, from, to, tr.AmountUSDT, tr.Reason)
			continue
		}
		v, ok := venues[tr.Venue]
		if !ok {
			err := fmt.Errorf("capital routing: venue %q not found", tr.Venue)
			result.Errs = append(result.Errs, err)
			recordFailedTransfer(tr)
			continue
		}
		wt, ok := v.(walletTransferer)
		if !ok {
			err := fmt.Errorf("capital routing: venue %q does not support wallet transfer", tr.Venue)
			result.Errs = append(result.Errs, err)
			recordFailedTransfer(tr)
			continue
		}
		if err := wt.TransferWallet(ctx, from, to, tr.AmountUSDT); err != nil {
			result.Errs = append(result.Errs, fmt.Errorf("capital routing: venue %q %s→%s %.4f: %w", tr.Venue, from, to, tr.AmountUSDT, err))
			recordFailedTransfer(tr)
			continue
		}
		result.Executed++
	}

	for _, sug := range plan.CrossVenueSuggestions {
		logger.Printf("[跨所建议] %s→%s 金额=%.4f 原因=%s（不自动执行）", sug.FromVenue, sug.ToVenue, sug.AmountUSDT, sug.Reason)
	}

	return result
}
