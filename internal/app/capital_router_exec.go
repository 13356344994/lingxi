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

// ExecuteCapitalRouting 执行资金路由计划中的所内划转;跨所建议仅记录日志,绝不执行。
// live==false 时只打影子日志,不调用任何划转接口。
func ExecuteCapitalRouting(ctx context.Context, plan CapitalRoutingPlan, venues map[string]venue.Venue, live bool, logger *log.Logger) (executed int, errs []error) {
	if logger == nil {
		logger = log.Default()
	}

	for _, tr := range plan.WithinVenueTransfers {
		from := string(tr.From)
		to := string(tr.To)
		if !live {
			logger.Printf("[影子] 本应划转 venue=%s %s→%s 金额=%.4f 原因=%s", tr.Venue, from, to, tr.AmountUSDT, tr.Reason)
			continue
		}
		v, ok := venues[tr.Venue]
		if !ok {
			errs = append(errs, fmt.Errorf("capital routing: venue %q not found", tr.Venue))
			continue
		}
		wt, ok := v.(walletTransferer)
		if !ok {
			errs = append(errs, fmt.Errorf("capital routing: venue %q does not support wallet transfer", tr.Venue))
			continue
		}
		if err := wt.TransferWallet(ctx, from, to, tr.AmountUSDT); err != nil {
			errs = append(errs, fmt.Errorf("capital routing: venue %q %s→%s %.4f: %w", tr.Venue, from, to, tr.AmountUSDT, err))
			continue
		}
		executed++
	}

	for _, sug := range plan.CrossVenueSuggestions {
		logger.Printf("[跨所建议] %s→%s 金额=%.4f 原因=%s（不自动执行）", sug.FromVenue, sug.ToVenue, sug.AmountUSDT, sug.Reason)
	}

	return executed, errs
}
