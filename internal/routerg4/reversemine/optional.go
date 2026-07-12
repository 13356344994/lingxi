package reversemine

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	historydata "lingxi/internal/datatruth/history"
	"lingxi/internal/routerg4/profitmap"
)

const optionalMaxStaleness = 24 * time.Hour

type optionalPoint struct {
	Time  time.Time
	First float64
	Last  float64
}

type optionalSeries struct {
	okxFunding   []optionalPoint
	bybitFunding []optionalPoint
	rubikTaker   []optionalPoint
	rubikRatio   []optionalPoint
}

func loadOptionalSeries(catalog historydata.HistoryCatalog, root, symbol string, maxLines int) (optionalSeries, error) {
	canonical := strings.ReplaceAll(strings.TrimSuffix(symbol, "-SWAP"), "-", "")
	base := strings.TrimSuffix(canonical, "USDT")
	specs := []struct {
		rel  string
		kind string
	}{
		{filepath.ToSlash(filepath.Join("okx_funding", symbol+".jsonl")), "okx"},
		{filepath.ToSlash(filepath.Join("bybit_funding", canonical+".jsonl")), "bybit"},
		{filepath.ToSlash(filepath.Join("rubik_taker_1h", base+".jsonl")), "taker"},
		{filepath.ToSlash(filepath.Join("rubik_lsratio_1h", base+".jsonl")), "ratio"},
	}
	var out optionalSeries
	for _, spec := range specs {
		stats, declared := profitmap.CatalogFile(catalog, spec.rel)
		if !declared || stats.Empty {
			continue
		}
		if err := profitmap.VerifyCatalogFile(catalog, root, spec.rel); err != nil {
			return optionalSeries{}, fmt.Errorf("reversemine: optional %s verification: %w", spec.rel, err)
		}
		points, err := loadOptionalJSONL(filepath.Join(root, filepath.FromSlash(spec.rel)), maxLines, spec.kind)
		if err != nil {
			return optionalSeries{}, fmt.Errorf("reversemine: optional %s parse: %w", spec.rel, err)
		}
		switch spec.kind {
		case "okx":
			out.okxFunding = points
		case "bybit":
			out.bybitFunding = points
		case "taker":
			out.rubikTaker = points
		case "ratio":
			out.rubikRatio = points
		}
	}
	return out, nil
}

func loadOptionalJSONL(path string, maxLines int, kind string) ([]optionalPoint, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var points []optionalPoint
	for line := 1; scanner.Scan() && line <= maxLines; line++ {
		var point optionalPoint
		switch kind {
		case "okx":
			var row struct {
				Data struct {
					Rate string `json:"fundingRate"`
					Time string `json:"fundingTime"`
				} `json:"data"`
			}
			if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
				return nil, err
			}
			point, err = parsePoint(row.Data.Time, row.Data.Rate, "")
		case "bybit":
			var row struct {
				Data struct {
					Rate string `json:"fundingRate"`
					Time string `json:"fundingRateTimestamp"`
				} `json:"data"`
			}
			if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
				return nil, err
			}
			point, err = parsePoint(row.Data.Time, row.Data.Rate, "")
		case "taker", "ratio":
			var row struct {
				Data []string `json:"data"`
			}
			if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
				return nil, err
			}
			required := 2
			if kind == "taker" {
				required = 3
			}
			if len(row.Data) < required {
				return nil, fmt.Errorf("line %d: insufficient data fields", line)
			}
			second := ""
			if kind == "taker" {
				second = row.Data[2]
			}
			point, err = parsePoint(row.Data[0], row.Data[1], second)
		default:
			return nil, fmt.Errorf("unsupported optional kind %q", kind)
		}
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		points = append(points, point)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	sort.Slice(points, func(i, j int) bool { return points[i].Time.Before(points[j].Time) })
	return points, nil
}

func parsePoint(msRaw, firstRaw, lastRaw string) (optionalPoint, error) {
	ms, err := strconv.ParseInt(msRaw, 10, 64)
	if err != nil || ms <= 0 {
		return optionalPoint{}, fmt.Errorf("invalid timestamp %q", msRaw)
	}
	first, err := strconv.ParseFloat(firstRaw, 64)
	if err != nil || math.IsNaN(first) || math.IsInf(first, 0) {
		return optionalPoint{}, fmt.Errorf("invalid value %q", firstRaw)
	}
	var last float64
	if lastRaw != "" {
		last, err = strconv.ParseFloat(lastRaw, 64)
		if err != nil || math.IsNaN(last) || math.IsInf(last, 0) {
			return optionalPoint{}, fmt.Errorf("invalid value %q", lastRaw)
		}
	}
	return optionalPoint{Time: time.UnixMilli(ms).UTC(), First: first, Last: last}, nil
}

func (s optionalSeries) featuresAt(decision time.Time) map[string]float64 {
	out := map[string]float64{}
	okx, okxOK := latestPoint(s.okxFunding, decision, 0)
	bybit, bybitOK := latestPoint(s.bybitFunding, decision, 0)
	if okxOK && bybitOK {
		out["okx_bybit_funding_spread"] = okx.First - bybit.First
		divergence := 0.0
		if (okx.First < 0) != (bybit.First < 0) {
			divergence = 1
		}
		out["okx_bybit_funding_sign_divergence"] = divergence
	}
	if taker, ok := latestPoint(s.rubikTaker, decision, time.Hour); ok {
		sum := taker.First + taker.Last
		if sum > 0 {
			out["rubik_taker_imbalance_1h"] = (taker.First - taker.Last) / sum
		}
	}
	if ratio, ok := latestPoint(s.rubikRatio, decision, time.Hour); ok && ratio.First > 0 {
		out["rubik_lsratio_1h"] = ratio.First
	}
	return out
}

func latestPoint(points []optionalPoint, decision time.Time, closeDelay time.Duration) (optionalPoint, bool) {
	var found optionalPoint
	ok := false
	for _, point := range points {
		closedAt := point.Time.Add(closeDelay)
		if closedAt.After(decision) {
			break
		}
		found, ok = point, true
	}
	if !ok {
		return optionalPoint{}, false
	}
	age := decision.Sub(found.Time.Add(closeDelay))
	if age < 0 || age > optionalMaxStaleness {
		return optionalPoint{}, false
	}
	return found, true
}
