package app

import (
	"context"
	"errors"
	"log"
	"time"

	"lingxi/internal/money"
	"lingxi/internal/venue"
)

type accountFundFlowLookup func(venueName string) venue.ExternalFundFlowProvider

func defaultAccountFundFlowLookup(venues map[string]venue.Venue) accountFundFlowLookup {
	return func(name string) venue.ExternalFundFlowProvider {
		if venues == nil {
			return nil
		}
		v, ok := venues[name]
		if !ok || v == nil {
			return nil
		}
		p, ok := v.(venue.ExternalFundFlowProvider)
		if !ok {
			return nil
		}
		return p
	}
}

func fetchAccountRealFundFlowUSDT(
	ctx context.Context,
	obs accountEquityObservation,
	lookup accountFundFlowLookup,
	since time.Time,
) (total float64, ok bool) {
	if lookup == nil || len(obs.ByVenue) == 0 {
		return 0, false
	}
	for venueName := range obs.ByVenue {
		p := lookup(venueName)
		if p == nil {
			return 0, false
		}
		net, err := p.FetchNetExternalFundFlowUSDT(ctx, since)
		if err != nil {
			if errors.Is(err, venue.ErrUnsupported) {
				return 0, false
			}
			return 0, false
		}
		total = money.SumUSDT(total, net)
	}
	return total, true
}

func applyAccountFundFlowAdjustment(
	ctx context.Context,
	st *accountRiskEquityState,
	obs accountEquityObservation,
	lookup accountFundFlowLookup,
	dayStart time.Time,
	currentTotal float64,
	rawDailyDelta float64,
	dailyRealized float64,
	pos accountRiskPositionSummary,
	settlementCount int,
	minStep float64,
	logger *log.Logger,
) (source string, step float64) {
	if st == nil {
		return "heuristic", 0
	}
	if realTotal, ok := fetchAccountRealFundFlowUSDT(ctx, obs, lookup, dayStart); ok {
		prev := st.DailyFundFlowAdjustUSDT
		st.DailyFundFlowAdjustUSDT = realTotal
		step = money.SumUSDT(realTotal, -prev)
		if logger != nil {
			if !st.fundFlowSourceLogged {
				logger.Printf("[账户风控] 充提流水源=real cumulative=%.4f (首次确认)", realTotal)
				st.fundFlowSourceLogged = true
			} else if step != 0 {
				logger.Printf("[账户风控] 资金流入/流出(source=real) step=%.4f cumulative=%.4f venues=%d",
					step, realTotal, len(obs.ByVenue))
			}
		}
		return "real", step
	}
	step = detectAccountFundFlowStep(
		st,
		currentTotal,
		rawDailyDelta,
		dailyRealized,
		pos,
		settlementCount,
		minStep,
	)
	if logger != nil {
		if !st.fundFlowSourceLogged {
			logger.Printf("[账户风控] 充提流水源=heuristic cumulative=%.4f (首次确认)", st.DailyFundFlowAdjustUSDT)
			st.fundFlowSourceLogged = true
		} else if step != 0 {
			logger.Printf("[账户风控] 资金流入/流出(source=heuristic) step=%.4f cumulative=%.4f", step, st.DailyFundFlowAdjustUSDT)
		}
	}
	return "heuristic", step
}

func crossVenueFundFlowLookup(cv *CrossVenueExecutor) accountFundFlowLookup {
	if cv == nil {
		return nil
	}
	return defaultAccountFundFlowLookup(cv.venues)
}
