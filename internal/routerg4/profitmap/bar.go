package profitmap

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"time"
)

// Bar is one confirmed OHLCV candle.
type Bar struct {
	OpenTime    time.Time
	Open        float64
	High        float64
	Low         float64
	Close       float64
	QuoteVolume float64
	Confirmed   bool
}

func (b Bar) Mid() float64 {
	return (b.High + b.Low) / 2
}

func (b Bar) CloseTime(duration time.Duration) time.Time {
	return b.OpenTime.Add(duration)
}

// LoadBars streams confirmed bars from one jsonl file up to maxLines.
func LoadBars(path string, maxLines int) ([]Bar, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var bars []Bar
	for scanner.Scan() && (maxLines <= 0 || len(bars) < maxLines) {
		var row struct {
			Data []string `json:"data"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			return nil, err
		}
		if len(row.Data) < 9 {
			continue
		}
		ms, _ := strconv.ParseInt(row.Data[0], 10, 64)
		openPrice, _ := strconv.ParseFloat(row.Data[1], 64)
		highPrice, _ := strconv.ParseFloat(row.Data[2], 64)
		lowPrice, _ := strconv.ParseFloat(row.Data[3], 64)
		closePrice, _ := strconv.ParseFloat(row.Data[4], 64)
		quoteVolume, _ := strconv.ParseFloat(row.Data[7], 64)
		confirmed := row.Data[8] == "1"
		if ms > 0 && openPrice > 0 && closePrice > 0 && confirmed {
			bars = append(bars, Bar{
				OpenTime:    time.UnixMilli(ms).UTC(),
				Open:        openPrice,
				High:        highPrice,
				Low:         lowPrice,
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

// RollingQuoteVolumeUSDT sums quote volume over the trailing window ending at barIndex.
func RollingQuoteVolumeUSDT(bars []Bar, barIndex int, windowBars int) float64 {
	if barIndex < 0 || len(bars) == 0 {
		return 0
	}
	start := barIndex - windowBars + 1
	if start < 0 {
		start = 0
	}
	var sum float64
	for i := start; i <= barIndex; i++ {
		sum += bars[i].QuoteVolume
	}
	return sum
}

// AvgQuoteVolumeUSDT returns average quote volume over windowBars ending at barIndex.
func AvgQuoteVolumeUSDT(bars []Bar, barIndex int, windowBars int) float64 {
	if windowBars <= 0 {
		return 0
	}
	return RollingQuoteVolumeUSDT(bars, barIndex, windowBars) / float64(windowBars)
}

// DecisionTime is the close timestamp of bar at index for the given bar duration.
func DecisionTime(bars []Bar, index int, barDuration time.Duration) time.Time {
	return bars[index].OpenTime.Add(barDuration)
}

// IsClosedAt reports whether bar index is fully closed at the given as-of time.
func IsClosedAt(bars []Bar, index int, barDuration time.Duration, asOf time.Time) bool {
	if index < 0 || index >= len(bars) {
		return false
	}
	return !bars[index].OpenTime.Add(barDuration).After(asOf)
}

// RejectCurrentBarOpen rejects decision points that would use an unclosed bar.
func RejectCurrentBarOpen(bars []Bar, index int, barDuration time.Duration) bool {
	if index < 0 || index >= len(bars) {
		return true
	}
	if !bars[index].Confirmed {
		return true
	}
	// Decision uses bar close; the bar must be confirmed and we never read
	// the forming next bar for features — only its open for entry.
	return false
}

// BarIndexAt returns the bar index whose close equals decisionTime, or -1.
func BarIndexAt(bars []Bar, decisionTime time.Time, barDuration time.Duration) int {
	for i, bar := range bars {
		if bar.OpenTime.Add(barDuration).Equal(decisionTime) {
			return i
		}
	}
	return -1
}

// LatestClosedBarBefore returns the last bar fully closed at or before asOf.
func LatestClosedBarBefore(bars []Bar, asOf time.Time, barDuration time.Duration) (Bar, bool) {
	var found Bar
	ok := false
	for _, bar := range bars {
		closeTime := bar.OpenTime.Add(barDuration)
		if closeTime.After(asOf) {
			break
		}
		found = bar
		ok = true
	}
	return found, ok
}

// FormatCandleLine builds one okx jsonl line for tests.
func FormatCandleLine(openTime time.Time, open, high, low, closePrice, quoteVol float64) string {
	row := map[string]any{
		"data": []string{
			fmt.Sprint(openTime.UnixMilli()),
			fmt.Sprint(open),
			fmt.Sprint(high),
			fmt.Sprint(low),
			fmt.Sprint(closePrice),
			"1", "1",
			fmt.Sprint(quoteVol),
			"1",
		},
		"fetched_at": openTime.Add(48 * time.Hour).UnixMilli(),
	}
	raw, _ := json.Marshal(row)
	return string(raw)
}
