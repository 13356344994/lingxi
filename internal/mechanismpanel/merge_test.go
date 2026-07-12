package mechanismpanel

import (
	"testing"
	"time"

	"lingxi/internal/datatruth"
)

// fakeSource is an in-memory Source for exercising the k-way merge without any file I/O.
type fakeSource struct {
	name string
	kind Kind
	recs []Record
	i    int
}

func (f *fakeSource) Name() string { return f.name }
func (f *fakeSource) Kind() Kind   { return f.kind }
func (f *fakeSource) Next() (Record, bool, error) {
	if f.i >= len(f.recs) {
		return Record{}, false, nil
	}
	r := f.recs[f.i]
	f.i++
	return r, true, nil
}
func (f *fakeSource) LinesRead() int64 { return int64(f.i) }
func (f *fakeSource) Close() error     { return nil }

func mkEnvelope(t time.Time) datatruth.EventEnvelope {
	return datatruth.EventEnvelope{EventTime: t, ReceiveTime: t, ProcessTime: t}
}

func TestMergerOrdersAcrossSourcesByEventTime(t *testing.T) {
	base := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	mk := func(offsetSec int, kind Kind) Record {
		return Record{Kind: kind, Envelope: mkEnvelope(base.Add(time.Duration(offsetSec) * time.Second))}
	}
	a := &fakeSource{name: "a", kind: KindLiquidation, recs: []Record{mk(0, KindLiquidation), mk(5, KindLiquidation), mk(10, KindLiquidation)}}
	b := &fakeSource{name: "b", kind: KindMarketMids, recs: []Record{mk(1, KindMarketMids), mk(4, KindMarketMids), mk(20, KindMarketMids)}}
	c := &fakeSource{name: "c", kind: KindDepth, recs: []Record{mk(-2, KindDepth)}}

	m, err := NewMerger([]Source{a, b, c})
	if err != nil {
		t.Fatalf("NewMerger: %v", err)
	}
	var order []time.Time
	for {
		rec, ok, err := m.Next()
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if !ok {
			break
		}
		order = append(order, rec.EventTime())
	}
	if len(order) != 7 {
		t.Fatalf("expected 7 merged records, got %d", len(order))
	}
	for i := 1; i < len(order); i++ {
		if order[i].Before(order[i-1]) {
			t.Fatalf("merge not sorted at index %d: %v before %v", i, order[i], order[i-1])
		}
	}
	wantFirst := base.Add(-2 * time.Second)
	if !order[0].Equal(wantFirst) {
		t.Errorf("first record = %v, want %v", order[0], wantFirst)
	}
}

func TestMergerHandlesEmptySources(t *testing.T) {
	empty := &fakeSource{name: "empty", kind: KindDepth}
	m, err := NewMerger([]Source{empty})
	if err != nil {
		t.Fatalf("NewMerger: %v", err)
	}
	_, ok, err := m.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if ok {
		t.Fatalf("expected immediate EOF on empty source")
	}
}
