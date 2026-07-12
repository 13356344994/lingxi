package g3reverse

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"lingxi/internal/routerg3/protocol"
)

const (
	familyOKX1h         = "okx_1h"
	familyOKX5m         = "okx_5m"
	familyOKXFunding    = "okx_funding"
	familyBybitFunding  = "bybit_funding"
	familyRubikTaker1h  = "rubik_taker_1h"
	familyRubikTaker5m  = "rubik_taker_5m"
	familyRubikLSRatio1 = "rubik_lsratio_1h"
)

var parsedFamilies = []string{
	familyOKX1h,
	familyOKX5m,
	familyOKXFunding,
	familyBybitFunding,
	familyRubikTaker1h,
	familyRubikTaker5m,
	familyRubikLSRatio1,
}

type priceBar struct {
	OpenTime    time.Time
	Open        float64
	Close       float64
	QuoteVolume float64
	Confirmed   bool
}

type fundingPoint struct {
	Time time.Time
	Rate float64
}

type rubikTakerPoint struct {
	Time time.Time
	Buy  float64
	Sell float64
}

type rubikRatioPoint struct {
	Time  time.Time
	Ratio float64
}

// CanonicalSymbol converts an OKX instrument id to the canonical symbol key.
func CanonicalSymbol(okxInstrument string) string {
	s := strings.TrimSuffix(okxInstrument, "-SWAP")
	return strings.ReplaceAll(s, "-", "")
}

// OKXInstrument converts a canonical symbol to the okx instrument filename stem.
func OKXInstrument(symbol string) string {
	if strings.Contains(symbol, "-") {
		return symbol
	}
	if strings.HasSuffix(symbol, "USDT") {
		base := strings.TrimSuffix(symbol, "USDT")
		return base + "-USDT-SWAP"
	}
	return symbol + "-USDT-SWAP"
}

// BybitFileName is the bybit_funding filename stem for a canonical symbol.
func BybitFileName(symbol string) string {
	return CanonicalSymbol(symbol)
}

// RubikFileName is the rubik filename stem (base coin) for a canonical symbol.
func RubikFileName(symbol string) string {
	s := CanonicalSymbol(symbol)
	if strings.HasSuffix(s, "USDT") {
		return strings.TrimSuffix(s, "USDT")
	}
	return s
}

func loadPrices(path string, maxLines int) ([]priceBar, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var bars []priceBar
	for scanner.Scan() && len(bars) < maxLines {
		var row struct {
			Data []string `json:"data"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			return nil, err
		}
		if len(row.Data) < 9 {
			continue
		}
		if err := rejectFetchedAt(scanner.Bytes()); err != nil {
			return nil, err
		}
		ms, _ := strconv.ParseInt(row.Data[0], 10, 64)
		openPrice, _ := strconv.ParseFloat(row.Data[1], 64)
		closePrice, _ := strconv.ParseFloat(row.Data[4], 64)
		quoteVolume, _ := strconv.ParseFloat(row.Data[7], 64)
		confirmed := row.Data[8] == "1"
		if ms > 0 && openPrice > 0 && closePrice > 0 && confirmed {
			bars = append(bars, priceBar{
				OpenTime:    time.UnixMilli(ms).UTC(),
				Open:        openPrice,
				Close:       closePrice,
				QuoteVolume: quoteVolume,
				Confirmed:   true,
			})
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	sort.Slice(bars, func(i, j int) bool { return bars[i].OpenTime.Before(bars[j].OpenTime) })
	return bars, nil
}

func loadOKXFunding(path string, maxLines int) ([]fundingPoint, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var points []fundingPoint
	for scanner.Scan() && len(points) < maxLines {
		var row struct {
			Data struct {
				FundingRate string `json:"fundingRate"`
				FundingTime string `json:"fundingTime"`
			} `json:"data"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			return nil, err
		}
		if err := rejectFetchedAt(scanner.Bytes()); err != nil {
			return nil, err
		}
		ms, _ := strconv.ParseInt(row.Data.FundingTime, 10, 64)
		rate, _ := strconv.ParseFloat(row.Data.FundingRate, 64)
		if ms > 0 {
			points = append(points, fundingPoint{Time: time.UnixMilli(ms).UTC(), Rate: rate})
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	sort.Slice(points, func(i, j int) bool { return points[i].Time.Before(points[j].Time) })
	return points, nil
}

func loadBybitFunding(path string, maxLines int) ([]fundingPoint, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var points []fundingPoint
	for scanner.Scan() && len(points) < maxLines {
		var row struct {
			Data struct {
				FundingRate          string `json:"fundingRate"`
				FundingRateTimestamp string `json:"fundingRateTimestamp"`
			} `json:"data"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			return nil, err
		}
		if err := rejectFetchedAt(scanner.Bytes()); err != nil {
			return nil, err
		}
		ms, _ := strconv.ParseInt(row.Data.FundingRateTimestamp, 10, 64)
		rate, _ := strconv.ParseFloat(row.Data.FundingRate, 64)
		if ms > 0 {
			points = append(points, fundingPoint{Time: time.UnixMilli(ms).UTC(), Rate: rate})
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	sort.Slice(points, func(i, j int) bool { return points[i].Time.Before(points[j].Time) })
	return points, nil
}

func loadRubikTaker(path string, maxLines int) ([]rubikTakerPoint, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var points []rubikTakerPoint
	for scanner.Scan() && len(points) < maxLines {
		var row struct {
			Data []string `json:"data"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			return nil, err
		}
		if err := rejectFetchedAt(scanner.Bytes()); err != nil {
			return nil, err
		}
		if len(row.Data) < 3 {
			continue
		}
		ms, _ := strconv.ParseInt(row.Data[0], 10, 64)
		buy, _ := strconv.ParseFloat(row.Data[1], 64)
		sell, _ := strconv.ParseFloat(row.Data[2], 64)
		if ms > 0 && buy >= 0 && sell >= 0 && (buy > 0 || sell > 0) {
			points = append(points, rubikTakerPoint{Time: time.UnixMilli(ms).UTC(), Buy: buy, Sell: sell})
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	sort.Slice(points, func(i, j int) bool { return points[i].Time.Before(points[j].Time) })
	return points, nil
}

func loadRubikLSRatio(path string, maxLines int) ([]rubikRatioPoint, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var points []rubikRatioPoint
	for scanner.Scan() && len(points) < maxLines {
		var row struct {
			Data []string `json:"data"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			return nil, err
		}
		if err := rejectFetchedAt(scanner.Bytes()); err != nil {
			return nil, err
		}
		if len(row.Data) < 2 {
			continue
		}
		ms, _ := strconv.ParseInt(row.Data[0], 10, 64)
		ratio, _ := strconv.ParseFloat(row.Data[1], 64)
		if ms > 0 && ratio > 0 {
			points = append(points, rubikRatioPoint{Time: time.UnixMilli(ms).UTC(), Ratio: ratio})
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	sort.Slice(points, func(i, j int) bool { return points[i].Time.Before(points[j].Time) })
	return points, nil
}

// rejectFetchedAt ensures parsers never treat fetched_at as a decision timestamp.
// The field may exist on disk but must be ignored for as-of semantics.
func rejectFetchedAt(raw []byte) error {
	var probe struct {
		FetchedAt json.RawMessage `json:"fetched_at"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return err
	}
	return nil
}

func latestFundingBefore(points []fundingPoint, decision time.Time) (fundingPoint, bool) {
	var found fundingPoint
	ok := false
	for _, p := range points {
		if p.Time.After(decision) {
			break
		}
		found = p
		ok = true
	}
	if !ok || decision.Sub(found.Time) < 0 || decision.Sub(found.Time) > 24*time.Hour {
		return fundingPoint{}, false
	}
	return found, true
}

func latestRubikTakerBefore(points []rubikTakerPoint, decision time.Time) (rubikTakerPoint, bool) {
	var found rubikTakerPoint
	ok := false
	for _, p := range points {
		if p.Time.Add(time.Hour).After(decision) {
			break
		}
		found = p
		ok = true
	}
	if !ok {
		return rubikTakerPoint{}, false
	}
	closedAt := found.Time.Add(time.Hour)
	maxStaleness := time.Duration(protocol.Rubik1hMaxStalenessHours) * time.Hour
	if age := decision.Sub(closedAt); age < 0 || age > maxStaleness {
		return rubikTakerPoint{}, false
	}
	return found, true
}

func latestRubikRatioBefore(points []rubikRatioPoint, decision time.Time) (rubikRatioPoint, bool) {
	var found rubikRatioPoint
	ok := false
	for _, p := range points {
		if p.Time.Add(time.Hour).After(decision) {
			break
		}
		found = p
		ok = true
	}
	if !ok {
		return rubikRatioPoint{}, false
	}
	closedAt := found.Time.Add(time.Hour)
	maxStaleness := time.Duration(protocol.Rubik1hMaxStalenessHours) * time.Hour
	if age := decision.Sub(closedAt); age < 0 || age > maxStaleness {
		return rubikRatioPoint{}, false
	}
	return found, true
}

func familyLoader(family string) (func(string, int) (any, error), error) {
	switch family {
	case familyOKX1h, familyOKX5m:
		return func(path string, max int) (any, error) { return loadPrices(path, max) }, nil
	case familyOKXFunding:
		return func(path string, max int) (any, error) { return loadOKXFunding(path, max) }, nil
	case familyBybitFunding:
		return func(path string, max int) (any, error) { return loadBybitFunding(path, max) }, nil
	case familyRubikTaker1h:
		return func(path string, max int) (any, error) { return loadRubikTaker(path, max) }, nil
	case familyRubikTaker5m:
		return func(path string, max int) (any, error) { return loadRubikTaker(path, max) }, nil
	case familyRubikLSRatio1:
		return func(path string, max int) (any, error) { return loadRubikLSRatio(path, max) }, nil
	default:
		return nil, fmt.Errorf("g3reverse: unsupported family %q", family)
	}
}

func validateFamilySchemas() error {
	for _, family := range parsedFamilies {
		if _, err := familyLoader(family); err != nil {
			return err
		}
	}
	return nil
}

func init() {
	if err := validateFamilySchemas(); err != nil {
		panic(err)
	}
}

func loadFamilyFundingSpan(path string, maxLines int) ([]fundingPoint, error) {
	points, err := loadOKXFunding(path, maxLines)
	if err != nil {
		return nil, err
	}
	if len(points) == 0 {
		return nil, errors.New("g3reverse: empty okx funding span")
	}
	return points, nil
}
