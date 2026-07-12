package contextdata

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"lingxi/internal/datatruth"
)

type MidsPayload struct {
	RecordedAt time.Time          `json:"recorded_at"`
	Mids       map[string]float64 `json:"mids"`
}

type OIPayload struct {
	RecordedAt time.Time                       `json:"recorded_at"`
	Venues     map[string]map[string][]float64 `json:"venues"`
}

type XVenuePayload struct {
	RecordedAt time.Time `json:"recorded_at"`
	Venue      string    `json:"venue"`
	Symbol     string    `json:"symbol"`
	RawSymbol  string    `json:"raw_symbol,omitempty"`
	SideRaw    string    `json:"side_raw,omitempty"`
	PosSide    string    `json:"pos_side"`
	Qty        float64   `json:"qty"`
	Price      float64   `json:"price"`
	ExchTS     int64     `json:"exch_ts,omitempty"`
}

type DepthPayload struct {
	RecordedAt    time.Time `json:"recorded_at"`
	Symbol        string    `json:"symbol"`
	BidPx         float64   `json:"bid_px"`
	BidQty        float64   `json:"bid_qty"`
	AskPx         float64   `json:"ask_px"`
	AskQty        float64   `json:"ask_qty"`
	DepthNotional float64   `json:"depth_notional"`
}

// DecodeLegacy converts one existing record-only JSONL row to a canonical three-clock record.
// For local snapshots (mids/OI/depth), recorded_at is the sampling and receive instant. Legacy
// rows do not persist a distinct process clock, so process_time uses receive_time and records
// that provenance explicitly rather than pretending an exchange timestamp existed.
func DecodeLegacy(kind Kind, raw []byte, sequence int64) (Record, bool, error) {
	var (
		source      string
		stream      string
		instrument  string
		eventTime   time.Time
		receiveTime time.Time
		payload     any
		missing     bool
		clocks      = ClockProvenance{
			EventTime:   "recorded_at",
			ReceiveTime: "recorded_at",
			ProcessTime: "legacy_receive_proxy",
		}
	)

	// Canonical input can be validated and passed through, which makes materialization idempotent.
	var canonical Record
	if json.Unmarshal(raw, &canonical) == nil && canonical.SchemaVersion == SchemaVersion {
		if canonical.Kind != kind {
			return Record{}, false, fmt.Errorf("contextdata: canonical kind %q does not match requested %q", canonical.Kind, kind)
		}
		if canonical.Event.EventTime.IsZero() || canonical.Event.ReceiveTime.IsZero() || canonical.Event.ProcessTime.IsZero() {
			return Record{}, false, fmt.Errorf("contextdata: canonical record missing three clocks")
		}
		return canonical, false, nil
	}

	switch kind {
	case KindMids:
		var row MidsPayload
		if err := json.Unmarshal(raw, &row); err != nil {
			return Record{}, false, err
		}
		source, stream = "okx", "fast_market_mids"
		eventTime, receiveTime, payload = row.RecordedAt, row.RecordedAt, row
		missing = row.RecordedAt.IsZero() || len(row.Mids) == 0
	case KindOI:
		var row OIPayload
		if err := json.Unmarshal(raw, &row); err != nil {
			return Record{}, false, err
		}
		source, stream = "xmeta", "fast_xmeta_snapshots"
		eventTime, receiveTime, payload = row.RecordedAt, row.RecordedAt, row
		missing = row.RecordedAt.IsZero() || !hasPositiveOI(row.Venues)
	case KindXVenue:
		var row XVenuePayload
		if err := json.Unmarshal(raw, &row); err != nil {
			return Record{}, false, err
		}
		// Historical Bybit pos_side before the corrected writer may be wrong; side_raw is the
		// immutable source of truth and is deterministically remapped here.
		if strings.EqualFold(row.Venue, "bybit") && strings.TrimSpace(row.SideRaw) != "" {
			switch strings.ToUpper(strings.TrimSpace(row.SideRaw)) {
			case "BUY":
				row.PosSide = "long"
			case "SELL":
				row.PosSide = "short"
			}
		}
		source, stream, instrument = row.Venue, "fast_xvenue_liq_events", row.Symbol
		eventTime, receiveTime, payload = row.RecordedAt, row.RecordedAt, row
		if row.ExchTS > 0 {
			eventTime = time.UnixMilli(row.ExchTS).UTC()
			clocks.EventTime = "exchange_ms"
		}
		missing = row.RecordedAt.IsZero() || strings.TrimSpace(row.Venue) == "" ||
			strings.TrimSpace(row.Symbol) == "" || (row.PosSide != "long" && row.PosSide != "short") ||
			row.Qty <= 0 || row.Price <= 0
	case KindDepth:
		var legacy struct {
			RecordedAt         time.Time `json:"recorded_at"`
			Symbol             string    `json:"symbol"`
			BidPx              float64   `json:"bid_px"`
			Bid                float64   `json:"bid"`
			BidQty             float64   `json:"bid_qty"`
			TopBidQty          float64   `json:"top_bid_qty"`
			AskPx              float64   `json:"ask_px"`
			Ask                float64   `json:"ask"`
			AskQty             float64   `json:"ask_qty"`
			TopAskQty          float64   `json:"top_ask_qty"`
			DepthNotional      float64   `json:"depth_notional"`
			DepthNotional10BPS float64   `json:"depth_notional_10bps"`
		}
		if err := json.Unmarshal(raw, &legacy); err != nil {
			return Record{}, false, err
		}
		row := DepthPayload{
			RecordedAt:    legacy.RecordedAt,
			Symbol:        legacy.Symbol,
			BidPx:         firstPositive(legacy.BidPx, legacy.Bid),
			BidQty:        firstPositive(legacy.BidQty, legacy.TopBidQty),
			AskPx:         firstPositive(legacy.AskPx, legacy.Ask),
			AskQty:        firstPositive(legacy.AskQty, legacy.TopAskQty),
			DepthNotional: firstPositive(legacy.DepthNotional, legacy.DepthNotional10BPS),
		}
		source, stream, instrument = "okx", "depth", row.Symbol
		eventTime, receiveTime, payload = row.RecordedAt, row.RecordedAt, row
		missing = row.RecordedAt.IsZero() || strings.TrimSpace(row.Symbol) == "" ||
			row.BidPx <= 0 || row.AskPx <= 0 || row.AskPx < row.BidPx ||
			(row.DepthNotional <= 0 && (row.BidQty <= 0 || row.AskQty <= 0))
	default:
		return Record{}, false, fmt.Errorf("contextdata: kind %q is not a JSONL context stream", kind)
	}

	processTime := receiveTime
	env, err := datatruth.NewEnvelope(source, stream, instrument, sequence, eventTime, receiveTime, processTime, payload)
	if err != nil {
		return Record{}, false, err
	}
	return Record{SchemaVersion: SchemaVersion, Kind: kind, Event: env, Clocks: clocks}, missing, nil
}

func hasPositiveOI(venues map[string]map[string][]float64) bool {
	for _, symbols := range venues {
		for _, values := range symbols {
			if len(values) > 1 && values[1] > 0 {
				return true
			}
		}
	}
	return false
}

func firstPositive(values ...float64) float64 {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}
