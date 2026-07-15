package datadrivenv3

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"lingxi/internal/datadrivenv3/fundingarchive"
)

type optionalSeries struct {
	lines        *reverseLines
	currentMS    int64
	currentValue float64
	eof          bool
	kind         string
}

func openOptionalSeries(path, kind string) (*optionalSeries, error) {
	lines, err := openReverseLines(path)
	if os.IsNotExist(err) {
		return &optionalSeries{eof: true, kind: kind}, nil
	}
	if err != nil {
		return nil, err
	}
	s := &optionalSeries{lines: lines, kind: kind}
	if err := s.advance(); err != nil && err != io.EOF {
		lines.Close()
		return nil, err
	}
	return s, nil
}
func (s *optionalSeries) advance() error {
	if s.lines == nil {
		s.eof = true
		return io.EOF
	}
	line, err := s.lines.Next()
	if err != nil {
		s.eof = true
		return err
	}
	var row struct {
		Data []string `json:"data"`
	}
	if err := json.Unmarshal(line, &row); err != nil {
		return err
	}
	if len(row.Data) < 2 {
		return errors.New("optional row too short")
	}
	s.currentMS, err = strconv.ParseInt(row.Data[0], 10, 64)
	if err != nil {
		return err
	}
	if s.kind == "taker" {
		if len(row.Data) < 3 {
			return errors.New("taker row too short")
		}
		buy, e := strconv.ParseFloat(row.Data[1], 64)
		if e != nil {
			return e
		}
		sell, e := strconv.ParseFloat(row.Data[2], 64)
		if e != nil {
			return e
		}
		if sell == 0 {
			s.currentValue = 0
		} else {
			s.currentValue = buy / sell
		}
	} else {
		s.currentValue, err = strconv.ParseFloat(row.Data[1], 64)
	}
	return err
}
func (s *optionalSeries) exact(ts int64) (float64, bool, error) {
	for !s.eof && s.currentMS < ts {
		if err := s.advance(); err != nil && err != io.EOF {
			return 0, false, err
		}
	}
	if !s.eof && s.currentMS == ts {
		return s.currentValue, true, nil
	}
	return 0, false, nil
}
func (s *optionalSeries) Close() error {
	if s.lines != nil {
		return s.lines.Close()
	}
	return nil
}

type symbolOptional struct{ taker, ls *optionalSeries }
type fundingPoint struct {
	symbol string
	timeMS int64
	rate   float64
}

// HistoricalOptionalLookup is a point-in-time streaming join. It retains only
// one Rubik point per symbol, one latest funding point per symbol, and a
// three-day funding event window.
type HistoricalOptionalLookup struct {
	root         string
	series       map[string]*symbolOptional
	maxLoadedDay time.Time
	scheduled    []fundingPoint
	latest       map[string]fundingPoint
	lastMS       int64
}

func NewHistoricalOptionalLookup(root string) *HistoricalOptionalLookup {
	return &HistoricalOptionalLookup{root: root, series: map[string]*symbolOptional{}, latest: map[string]fundingPoint{}}
}
func (h *HistoricalOptionalLookup) At(instrument string, decisionMS int64) (OptionalPoint, error) {
	if decisionMS < h.lastMS {
		return OptionalPoint{}, errors.New("optional lookup time regressed")
	}
	if decisionMS > h.lastMS {
		if err := h.advanceFunding(decisionMS); err != nil {
			return OptionalPoint{}, err
		}
		h.lastMS = decisionMS
	}
	s := h.series[instrument]
	if s == nil {
		base := strings.TrimSuffix(instrument, "-USDT-SWAP")
		taker, err := openOptionalSeries(filepath.Join(h.root, "records/history/rubik_taker_1h", base+".jsonl"), "taker")
		if err != nil {
			return OptionalPoint{}, err
		}
		ls, err := openOptionalSeries(filepath.Join(h.root, "records/history/rubik_lsratio_1h", base+".jsonl"), "ls")
		if err != nil {
			taker.Close()
			return OptionalPoint{}, err
		}
		s = &symbolOptional{taker: taker, ls: ls}
		h.series[instrument] = s
	}
	out := OptionalPoint{ObservedAtMS: decisionMS}
	if value, ok, err := s.taker.exact(decisionMS); err != nil {
		return out, err
	} else if ok {
		out.TakerRatio = value
		out.HasTaker = true
	}
	if value, ok, err := s.ls.exact(decisionMS); err != nil {
		return out, err
	} else if ok {
		out.LSRatio = value
		out.HasLS = true
	}
	if point, ok := h.latest[instrument]; ok {
		out.HasFunding = true
		out.FundingRate = point.rate
		out.FundingMS = point.timeMS
	}
	return out, nil
}
func (h *HistoricalOptionalLookup) advanceFunding(ts int64) error {
	day := time.UnixMilli(ts).UTC().Truncate(24 * time.Hour)
	start := day.AddDate(0, 0, -1)
	if h.maxLoadedDay.IsZero() {
		start = time.Date(2023, 12, 31, 0, 0, 0, 0, time.UTC)
	} else {
		start = h.maxLoadedDay.AddDate(0, 0, 1)
	}
	for d := start; !d.After(day.AddDate(0, 0, 1)); d = d.AddDate(0, 0, 1) {
		key := d.Format("2006-01-02")
		h.maxLoadedDay = d
		path := filepath.Join(h.root, "records/history/okx_funding_v3/daily", key+".jsonl")
		f, err := os.Open(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 1024), 1<<20)
		for scanner.Scan() {
			var event fundingarchive.Event
			if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
				f.Close()
				return err
			}
			rate, err := strconv.ParseFloat(event.FundingRate, 64)
			if err != nil {
				f.Close()
				return err
			}
			h.scheduled = append(h.scheduled, fundingPoint{symbol: event.InstID, timeMS: event.FundingTime, rate: rate})
		}
		if err := scanner.Err(); err != nil {
			f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
	}
	sort.Slice(h.scheduled, func(i, j int) bool {
		if h.scheduled[i].timeMS != h.scheduled[j].timeMS {
			return h.scheduled[i].timeMS < h.scheduled[j].timeMS
		}
		return h.scheduled[i].symbol < h.scheduled[j].symbol
	})
	used := 0
	for used < len(h.scheduled) && h.scheduled[used].timeMS <= ts {
		point := h.scheduled[used]
		if prior, ok := h.latest[point.symbol]; ok && prior.timeMS == point.timeMS && prior.rate != point.rate {
			return errors.New("conflicting funding timestamp")
		}
		if prior, ok := h.latest[point.symbol]; !ok || point.timeMS >= prior.timeMS {
			h.latest[point.symbol] = point
		}
		used++
	}
	h.scheduled = append([]fundingPoint(nil), h.scheduled[used:]...)
	return nil
}
func (h *HistoricalOptionalLookup) Close() error {
	for _, s := range h.series {
		_ = s.taker.Close()
		_ = s.ls.Close()
	}
	return nil
}
