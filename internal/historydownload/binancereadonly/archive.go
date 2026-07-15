package binancereadonly

import (
	"encoding/csv"
	"errors"
	"io"
	"math/big"
	"os"
	"strings"
)

const (
	MaxArchiveBytes = int64(2 << 30)
	MaxArchiveFiles = 8
)

type ZeroFillSummary struct {
	SchemaFields []string       `json:"schema_fields"`
	Counts       map[string]int `json:"status_zero_executed_qty_counts"`
	Rows         int            `json:"rows"`
	IdentityRows int            `json:"identity_rows"`
	TimeRows     int            `json:"time_rows"`
	ZIPHash      string         `json:"zip_sha256,omitempty"`
	CSVHash      string         `json:"csv_sha256,omitempty"`
}

// ValidateCSVSchema exposes headers only and rejects any outcome-oriented
// column. The exact upstream field set is intentionally not accepted until
// official schema verification has been recorded.
func ValidateCSVSchema(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	reader := csv.NewReader(io.LimitReader(file, MaxArchiveBytes))
	header, err := reader.Read()
	if err != nil || len(header) == 0 {
		return errors.New("binancereadonly: CSV header absent")
	}
	required := map[string]bool{"orderid": false, "status": false, "executedqty": false, "updatetime": false}
	for _, raw := range header {
		name := canonicalField(raw)
		if name == "" || isOutcomeField(name) {
			return errors.New("binancereadonly: CSV outcome or invalid field rejected")
		}
		if _, ok := required[name]; ok {
			required[name] = true
		}
	}
	for _, present := range required {
		if !present {
			return errors.New("binancereadonly: CSV required schema absent")
		}
	}
	return nil
}

// SummarizeZeroFill reads only identity presence, time presence, and the
// status-by-zero-executedQty denominator. It never retains price, PnL, or any
// outcome value.
func SummarizeZeroFill(path string) (ZeroFillSummary, error) {
	file, err := os.Open(path)
	if err != nil {
		return ZeroFillSummary{}, err
	}
	defer file.Close()
	return summarizeCSV(file)
}

func summarizeCSV(input io.Reader) (ZeroFillSummary, error) {
	reader := csv.NewReader(io.LimitReader(input, MaxArchiveBytes))
	header, err := reader.Read()
	if err != nil {
		return ZeroFillSummary{}, errors.New("binancereadonly: CSV header absent")
	}
	index := map[string]int{}
	fields := make([]string, len(header))
	required := map[string]bool{"orderid": false, "status": false, "executedqty": false, "updatetime": false}
	for i, value := range header {
		fields[i] = strings.TrimSpace(value)
		field := canonicalField(value)
		if field == "" || isOutcomeField(field) {
			return ZeroFillSummary{}, errors.New("binancereadonly: CSV outcome or invalid field rejected")
		}
		index[field] = i
		if _, ok := required[field]; ok {
			required[field] = true
		}
	}
	for _, present := range required {
		if !present {
			return ZeroFillSummary{}, errors.New("binancereadonly: CSV required schema absent")
		}
	}
	summary := ZeroFillSummary{SchemaFields: fields, Counts: map[string]int{}}
	for {
		row, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil || len(row) != len(header) {
			return ZeroFillSummary{}, errors.New("binancereadonly: malformed CSV row")
		}
		summary.Rows++
		if strings.TrimSpace(row[index["orderid"]]) != "" {
			summary.IdentityRows++
		}
		if strings.TrimSpace(row[index["updatetime"]]) != "" {
			summary.TimeRows++
		}
		zero, err := isDecimalZero(row[index["executedqty"]])
		if err != nil {
			return ZeroFillSummary{}, err
		}
		if zero {
			status := strings.ToUpper(strings.TrimSpace(row[index["status"]]))
			if status == "CANCELED" || status == "EXPIRED" {
				summary.Counts[status]++
			}
		}
	}
	if summary.IdentityRows != summary.Rows || summary.TimeRows != summary.Rows {
		return ZeroFillSummary{}, errors.New("binancereadonly: identity or time coverage incomplete")
	}
	return summary, nil
}

func isDecimalZero(raw string) (bool, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return false, errors.New("binancereadonly: executedQty invalid decimal")
	}
	start := 0
	if value[0] == '-' {
		start = 1
		if len(value) == 1 {
			return false, errors.New("binancereadonly: executedQty invalid decimal")
		}
	}
	dot := false
	digits := 0
	for _, r := range value[start:] {
		if r == '.' && !dot {
			dot = true
			continue
		}
		if r < '0' || r > '9' {
			return false, errors.New("binancereadonly: executedQty invalid decimal")
		}
		digits++
	}
	if digits == 0 || strings.HasSuffix(value, ".") {
		return false, errors.New("binancereadonly: executedQty invalid decimal")
	}
	rat, ok := new(big.Rat).SetString(value)
	if !ok {
		return false, errors.New("binancereadonly: executedQty invalid decimal")
	}
	if rat.Sign() < 0 {
		return false, errors.New("binancereadonly: executedQty negative")
	}
	return rat.Sign() == 0, nil
}

func canonicalField(value string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(value)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func isOutcomeField(field string) bool {
	for _, forbidden := range []string{
		"pnl", "profit", "return", "roe", "roi", "income", "realized", "unrealized",
		"gain", "loss", "yield", "closedpl", "netpl", "futurevalue", "markout",
	} {
		if strings.Contains(field, forbidden) {
			return true
		}
	}
	return false
}
