package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"time"

	"lingxi/internal/venue"
)

const defaultDustCloseThresholdUSDT = 1.0

// dustOrphanDecision 是两腿策略孤腿检测结论。
type dustOrphanDecision struct {
	IsOrphan              bool
	IsDustOrphan          bool
	NakedRisk             bool
	RemainingNotionalUSDT float64
}

func dustCloseThresholdUSDT(cfg CrossVenueExecutorConfig) float64 {
	if cfg.DustCloseThresholdUSDT > 0 {
		return cfg.DustCloseThresholdUSDT
	}
	return defaultDustCloseThresholdUSDT
}

func effectiveDustThresholdUSDT(cfg CrossVenueExecutorConfig, exchangeMinNotional float64) float64 {
	floor := dustCloseThresholdUSDT(cfg)
	if exchangeMinNotional > floor {
		return exchangeMinNotional
	}
	return floor
}

func venueInstrumentMinNotional(ctx context.Context, v venue.Venue, symbol string, market venue.Market) float64 {
	if v == nil {
		return 0
	}
	insts, err := v.FetchInstruments(ctx)
	if err != nil {
		return 0
	}
	want := strings.ToUpper(strings.TrimSpace(symbol))
	for _, inst := range insts {
		if inst.Market != market {
			continue
		}
		if strings.ToUpper(string(inst.Symbol)) == want {
			return inst.MinNotional
		}
	}
	return 0
}

func legNotionalUSDT(ctx context.Context, v venue.Venue, sym venue.Symbol, market venue.Market, qty, entryPrice float64) float64 {
	if qty <= executionQtyTolerance {
		return 0
	}
	price := entryPrice
	if v != nil {
		if bt, err := v.FetchBookTicker(ctx, market, sym); err == nil {
			if mid := (bt.BidPrice + bt.AskPrice) / 2; mid > 0 {
				price = mid
			} else if bt.AskPrice > 0 {
				price = bt.AskPrice
			} else if bt.BidPrice > 0 {
				price = bt.BidPrice
			}
		}
	}
	if price <= 0 {
		return 0
	}
	return qty * price
}

// evaluatePositiveCarryOrphan 正 carry(现货多 + 永续空):永续为风险腿,现货残余可为 dust。
func evaluatePositiveCarryOrphan(
	ctx context.Context,
	cfg CrossVenueExecutorConfig,
	venues map[string]venue.Venue,
	symbol, spotVenue, perpVenue string,
	spotOpen, perpOpen bool,
	spotQty, perpQty, spotEntryPrice float64,
) dustOrphanDecision {
	if spotOpen == perpOpen {
		return dustOrphanDecision{}
	}
	d := dustOrphanDecision{IsOrphan: true}
	if perpOpen && !spotOpen {
		d.NakedRisk = true
		return d
	}
	sym := venue.Symbol(symbol)
	spotV := venues[spotVenue]
	notional := legNotionalUSDT(ctx, spotV, sym, venue.MarketSpot, spotQty, spotEntryPrice)
	minN := venueInstrumentMinNotional(ctx, spotV, symbol, venue.MarketSpot)
	threshold := effectiveDustThresholdUSDT(cfg, minN)
	d.RemainingNotionalUSDT = notional
	if notional > 0 && notional < threshold {
		d.IsDustOrphan = true
	} else {
		d.NakedRisk = true
	}
	return d
}

// evaluateNegativeCarryOrphan 负 carry(现货空/借 + 永续多):永续残余可为 dust;
// 现货空孤腿若名义低于 minNotional 亦视为 dust(交易所下不出还币单,应收口而非无限重试)。
func evaluateNegativeCarryOrphan(
	ctx context.Context,
	cfg CrossVenueExecutorConfig,
	venues map[string]venue.Venue,
	symbol, spotVenue, perpVenue string,
	spotOpen, perpOpen bool,
	spotShort, perpLong, spotEntryPrice, perpEntryPrice float64,
) dustOrphanDecision {
	if spotOpen == perpOpen {
		return dustOrphanDecision{}
	}
	d := dustOrphanDecision{IsOrphan: true}
	sym := venue.Symbol(symbol)

	if spotOpen && !perpOpen {
		spotV := venues[spotVenue]
		notional := legNotionalUSDT(ctx, spotV, sym, venue.MarketSpot, spotShort, spotEntryPrice)
		minN := venueInstrumentMinNotional(ctx, spotV, symbol, venue.MarketSpot)
		threshold := effectiveDustThresholdUSDT(cfg, minN)
		d.RemainingNotionalUSDT = notional
		if notional > 0 && notional < threshold {
			d.IsDustOrphan = true
		} else {
			d.NakedRisk = true
		}
		return d
	}

	perpV := venues[perpVenue]
	notional := legNotionalUSDT(ctx, perpV, sym, venue.MarketPerp, perpLong, perpEntryPrice)
	minN := venueInstrumentMinNotional(ctx, perpV, symbol, venue.MarketPerp)
	threshold := effectiveDustThresholdUSDT(cfg, minN)
	d.RemainingNotionalUSDT = notional
	if notional > 0 && notional < threshold {
		d.IsDustOrphan = true
	} else {
		d.NakedRisk = true
	}
	return d
}

// isNotionalFilterError 识别交易所 minNotional 拒单(-1013 NOTIONAL filter)。
func isNotionalFilterError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToUpper(err.Error())
	if !strings.Contains(msg, "NOTIONAL") {
		return false
	}
	var ve *venue.VenueError
	if errors.As(err, &ve) && ve.RawCode == "-1013" {
		return true
	}
	return strings.Contains(msg, "-1013") || strings.Contains(msg, "FILTER FAILURE")
}

// negativeCarryDustFromNotionalReject 平仓回 -1013 NOTIONAL 时补判 dust(与 evaluate 口径一致)。
func negativeCarryDustFromNotionalReject(
	ctx context.Context,
	cfg CrossVenueExecutorConfig,
	venues map[string]venue.Venue,
	symbol, spotVenue, perpVenue string,
	spotOpen, perpOpen bool,
	spotShort, perpLong, spotEntryPrice, perpEntryPrice float64,
) dustOrphanDecision {
	return evaluateNegativeCarryOrphan(ctx, cfg, venues, symbol, spotVenue, perpVenue, spotOpen, perpOpen, spotShort, perpLong, spotEntryPrice, perpEntryPrice)
}

type marginBorrowRepayer interface {
	BorrowRepay(ctx context.Context, asset string, amt float64, dir venue.BorrowDir) error
}

// bestEffortRepayMarginBorrow 对借币空头 dust 尝试 margin 直接还币(不经买卖);失败静默跳过。
func bestEffortRepayMarginBorrow(ctx context.Context, v venue.Venue, symbol string, qty float64) {
	if v == nil || qty <= executionQtyTolerance {
		return
	}
	repayer, ok := v.(marginBorrowRepayer)
	if !ok {
		return
	}
	asset := baseAssetFromCanonicalSymbol(symbol)
	_ = repayer.BorrowRepay(ctx, asset, qty, venue.BorrowDirRepay)
}

func negativeCarryDustResidualQty(spotOpen, perpOpen bool, spotShort, perpLong float64) float64 {
	switch {
	case spotOpen && !perpOpen:
		return spotShort
	case perpOpen && !spotOpen:
		return perpLong
	default:
		return 0
	}
}

// appendCarryDustLedgerBestEffort 把 carry 执行器 dust 残腿记入 dust_ledger.json(失败静默)。
func appendCarryDustLedgerBestEffort(recordDir, phase, symbol, asset string, qty, notionalUSDT float64, reason string) {
	if strings.TrimSpace(recordDir) == "" || qty <= executionQtyTolerance {
		return
	}
	path := filepath.Join(strings.TrimSpace(recordDir), "dust_ledger.json")
	records, _ := readJSONFile[DustLedgerRecord](path)
	record := DustLedgerRecord{
		RecordedAt:   time.Now().UTC().Format(time.RFC3339),
		Phase:        phase,
		Symbol:       symbol,
		Pair:         symbol,
		Asset:        asset,
		Quantity:     formatFloat(qty),
		NotionalUSDT: formatFloat(maxFloat(notionalUSDT, 0)),
		Reason:       reason,
	}
	records = append(records, record)
	_ = writeJSONFile(path, records)
}

func logDustOrphanClosed(logger *log.Logger, strategyLabel, symbol string, notional float64) {
	if logger == nil {
		return
	}
	logger.Printf("[%s] 残余 dust ≈$%.4f 低于最小可平,标记已平,保留零头 %s", strategyLabel, notional, symbol)
}

func appendDustCloseReason(base string, notional float64) string {
	suffix := fmt.Sprintf("dust_residual≈$%.4f", notional)
	if strings.TrimSpace(base) == "" {
		return suffix
	}
	return base + ";" + suffix
}
