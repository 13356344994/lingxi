package profitmap

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"
)

func loadFundingJSONL(path string, maxLines int) ([]FundingPoint, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var points []FundingPoint
	line := 0
	for scanner.Scan() && (maxLines <= 0 || line < maxLines) {
		line++
		var row struct {
			Data struct {
				FundingRate string `json:"fundingRate"`
				FundingTime string `json:"fundingTime"`
			} `json:"data"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			return nil, err
		}
		ms, err := strconv.ParseInt(row.Data.FundingTime, 10, 64)
		if err != nil || ms <= 0 {
			return nil, fmt.Errorf("profitmap: funding line %d invalid fundingTime %q", line, row.Data.FundingTime)
		}
		rate, err := strconv.ParseFloat(row.Data.FundingRate, 64)
		if err != nil || math.IsNaN(rate) || math.IsInf(rate, 0) {
			return nil, fmt.Errorf("profitmap: funding line %d invalid fundingRate %q", line, row.Data.FundingRate)
		}
		points = append(points, FundingPoint{Time: time.UnixMilli(ms).UTC(), Rate: rate})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	sort.Slice(points, func(i, j int) bool { return points[i].Time.Before(points[j].Time) })
	return points, nil
}

// LoadSymbolFunding loads optional okx funding for one instrument.
func LoadSymbolFunding(historyRoot, instrument string, maxLines int) ([]FundingPoint, error) {
	path := filepath.Join(historyRoot, "okx_funding", instrument+".jsonl")
	return loadFundingJSONL(path, maxLines)
}
