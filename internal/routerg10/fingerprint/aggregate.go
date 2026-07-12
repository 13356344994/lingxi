package fingerprint

import (
	"sort"
	"strings"
	"time"
)

type sampleReservoir struct {
	values []float64
	limit  int
	count  int
}

func newSampleReservoir(limit int) *sampleReservoir {
	if limit <= 0 {
		limit = MaxReservoir
	}
	return &sampleReservoir{limit: limit}
}

func (r *sampleReservoir) add(v float64) {
	if len(r.values) < r.limit {
		r.values = append(r.values, v)
		r.count++
		return
	}
	idx := r.count % r.limit
	r.values[idx] = v
	r.count++
}

type rateCounter struct {
	hits  int
	total int
}

func (c *rateCounter) add(hit bool) {
	c.total++
	if hit {
		c.hits++
	}
}

type cellAccumulator struct {
	key        CellKey
	feeBPS     *sampleReservoir
	entrySlip  *sampleReservoir
	exitSlip   *sampleReservoir
	makerWait  *sampleReservoir
	latencyMS  *sampleReservoir
	funding    *sampleReservoir
	makerFill  rateCounter
	cancelRisk rateCounter
	samples    int
	instrument InstrumentConstraints
}

func newCellAccumulator(key CellKey) *cellAccumulator {
	return &cellAccumulator{
		key:        key,
		feeBPS:     newSampleReservoir(MaxReservoir),
		entrySlip:  newSampleReservoir(MaxReservoir),
		exitSlip:   newSampleReservoir(MaxReservoir),
		makerWait:  newSampleReservoir(MaxReservoir),
		latencyMS:  newSampleReservoir(MaxReservoir),
		funding:    newSampleReservoir(MaxReservoir),
		instrument: InstrumentConstraints{Status: StatusUnknown},
	}
}

func (c *cellAccumulator) finalize() ExecutionCell {
	return ExecutionCell{
		CellKey:        c.key,
		FeeBPS:         finalizeQuantile(c.feeBPS.values),
		EntrySlipBPS:   finalizeQuantile(c.entrySlip.values),
		ExitSlipBPS:    finalizeQuantile(c.exitSlip.values),
		MakerFillRate:  finalizeRate(c.makerFill.hits, c.makerFill.total),
		MakerWaitMS:    finalizeQuantile(c.makerWait.values),
		LatencyMS:      finalizeQuantile(c.latencyMS.values),
		CancelRiskRate: finalizeRate(c.cancelRisk.hits, c.cancelRisk.total),
		FundingRate:    finalizeQuantile(c.funding.values),
		Instrument:     c.instrument,
		SampleCount:    c.samples,
	}
}

type store struct {
	cells map[string]*cellAccumulator
}

func newStore() *store {
	return &store{cells: map[string]*cellAccumulator{}}
}

func cellKeyString(k CellKey) string {
	parts := []string{
		k.Venue,
		k.Symbol,
		k.HourUTC,
		k.Direction,
		k.OrderType,
		k.NotionalBucket,
	}
	return strings.Join(parts, "|")
}

func (s *store) cell(key CellKey) *cellAccumulator {
	id := cellKeyString(key)
	if acc, ok := s.cells[id]; ok {
		return acc
	}
	acc := newCellAccumulator(key)
	s.cells[id] = acc
	return acc
}

func (s *store) sortedCells() []ExecutionCell {
	keys := make([]string, 0, len(s.cells))
	for k := range s.cells {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]ExecutionCell, 0, len(keys))
	for _, k := range keys {
		out = append(out, s.cells[k].finalize())
	}
	return out
}

func hourUTC(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	return t.UTC().Format("15")
}

func normalizeDirection(side string) string {
	switch strings.ToLower(strings.TrimSpace(side)) {
	case "long", "buy":
		return "long"
	case "short", "sell":
		return "short"
	default:
		return "unknown"
	}
}

func orderTypeFromFlags(openViaTaker, makerEntry bool) string {
	if openViaTaker {
		return "taker"
	}
	if makerEntry {
		return "maker"
	}
	return "unknown"
}

func orderTypeFromMakerAttempt(makerFilled bool) string {
	return "maker"
}

func orderTypeFromDecision(openViaTaker bool, result string) string {
	if openViaTaker || strings.EqualFold(result, "taker_filled") {
		return "taker"
	}
	if strings.EqualFold(result, "maker_not_filled") || strings.EqualFold(result, "opened") {
		return "maker"
	}
	return "unknown"
}

func notionalBucket(notional float64) string {
	switch {
	case notional <= 0:
		return "unknown"
	case notional < 30:
		return "micro"
	case notional < 100:
		return "small"
	case notional < 500:
		return "medium"
	default:
		return "large"
	}
}

func feeBPS(feeUSDT, notional float64) float64 {
	if notional <= 0 || feeUSDT < 0 {
		return 0
	}
	return feeUSDT / notional * 10000
}

func attachInstrument(acc *cellAccumulator, inst InstrumentConstraints) {
	if inst.Status == StatusAvailable {
		acc.instrument = inst
		return
	}
	if acc.instrument.Status != StatusAvailable {
		acc.instrument = inst
	}
}
