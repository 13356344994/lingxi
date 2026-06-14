package app

import (
	"context"
	"fmt"
	"log"
	"math"
	"strings"

	"lingxi/internal/venue"
	"lingxi/internal/venue/bybit"
)

// cross_venue_funding.go 跨所开仓前永续保证金兜底:读取真实可用、从闲置钱包自动补足。

type bybitAgreementSigner interface {
	SignTradingAgreements(ctx context.Context) error
}

func fetchVenuePerpAvailableUSDT(ctx context.Context, v venue.Venue) (float64, error) {
	p, ok := v.(venue.PerpMarginProvider)
	if !ok {
		return 0, fmt.Errorf("venue %q 未实现 PerpMarginProvider", v.Name())
	}
	avail, err := p.FetchPerpAvailableUSDT(ctx)
	if err != nil {
		return 0, err
	}
	return math.Max(avail, 0), nil
}

func conservativeCrossVenueRequiredMargin(cfg CrossVenueExecutorConfig) float64 {
	notional := cfg.MaxPositionUSDT
	if notional <= 0 {
		notional = 50
	}
	levCap := cfg.MaxEffectiveLeverage
	if levCap <= 0 {
		levCap = 2
	}
	marginFactor := cfg.MarginSafetyFactor
	if marginFactor <= 0 {
		marginFactor = 1.5
	}
	buffer := cfg.PerpMarginTopUpBuffer
	if buffer <= 0 {
		buffer = 1.05
	}
	return notional / levCap * marginFactor * buffer
}

// ensureCrossVenuePerpMargin 对将参与开仓的两所,若永续可用 < required,从闲置钱包 best-effort 补足差额。
func ensureCrossVenuePerpMargin(
	ctx context.Context,
	logger *log.Logger,
	venues map[string]venue.Venue,
	highVenue, lowVenue string,
	requiredUSDT float64,
) {
	if requiredUSDT <= 0 {
		return
	}
	for _, name := range []string{highVenue, lowVenue} {
		v := venues[name]
		if v == nil {
			continue
		}
		topUpVenuePerpMargin(ctx, logger, name, v, requiredUSDT)
	}
}

func topUpVenuePerpMargin(ctx context.Context, logger *log.Logger, venueName string, v venue.Venue, requiredUSDT float64) {
	p, ok := v.(venue.PerpMarginProvider)
	if !ok {
		if logger != nil {
			logger.Printf("[跨所资金] venue=%s 无 PerpMarginProvider,跳过自动划转", venueName)
		}
		return
	}
	before, err := p.FetchPerpAvailableUSDT(ctx)
	if err != nil {
		if logger != nil {
			logger.Printf("[跨所资金] venue=%s 读取永续可用失败,跳过自动划转: %v", venueName, err)
		}
		return
	}
	if before+1e-6 >= requiredUSDT {
		return
	}
	transferred, err := p.TopUpPerpMarginUSDT(ctx, requiredUSDT)
	if err != nil {
		if logger != nil {
			logger.Printf("[跨所资金] venue=%s 永续保证金补足失败(WARN,闸门将兜底): %v", venueName, err)
		}
		return
	}
	if transferred <= 1e-6 {
		if logger != nil {
			logger.Printf("[跨所资金] venue=%s 永续可用 %.2f < 所需 %.2f,闲置钱包无可划 USDT", venueName, before, requiredUSDT)
		}
		return
	}
	after, _ := p.FetchPerpAvailableUSDT(ctx)
	if logger != nil {
		logger.Printf("[跨所资金] venue=%s 永续保证金补足: %.2f→%.2f (划转 %.2f,目标 %.2f)",
			venueName, before, after, transferred, requiredUSDT)
	}
}

func ensureBybitTradingAgreement(ctx context.Context, logger *log.Logger, v venue.Venue) {
	if v == nil {
		return
	}
	signer, ok := v.(bybitAgreementSigner)
	if !ok {
		return
	}
	if err := signer.SignTradingAgreements(ctx); err != nil {
		if logger != nil {
			logger.Printf("[跨所] bybit 协议签署 best-effort 失败(可能已签): %v", err)
		}
	}
}

func isBybitAgreementRequiredError(err error) bool {
	return bybit.IsAgreementRequiredError(err)
}

func crossVenueUsesBybit(highVenue, lowVenue string) bool {
	return highVenue == bybit.VenueName || lowVenue == bybit.VenueName
}

func extractBybitAgreementSymbol(err error, opp CrossVenueOpportunity) string {
	sym := strings.TrimSpace(opp.Symbol)
	if sym == "" && err != nil {
		sym = "unknown"
	}
	return sym
}
