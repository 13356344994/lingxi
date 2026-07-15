package reportcenter

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

const (
	DefaultRoot     = "/root/.lingxi-private/historydownload/report-center"
	DefaultInbox    = DefaultRoot + "/inbox"
	DefaultMappings = DefaultRoot + "/mappings"
	maxRootCap      = int64(4 << 30)
)

type Template string

const (
	OrderTemplate   Template = "order"
	TradingTemplate Template = "trading"
)

type Mapping struct {
	Template Template          `json:"template"`
	Columns  map[string]string `json:"columns"`
}

type Config struct {
	Root        string
	Inbox       string
	CSVPath     string
	MappingPath string
	RootCap     int64
	Fault       func(string) error
}

type TimeRange struct {
	Min string `json:"min"`
	Max string `json:"max"`
}

type Receipt struct {
	SchemaVersion            string               `json:"schema_version"`
	Accepted                 bool                 `json:"accepted"`
	Template                 Template             `json:"template,omitempty"`
	RawSource                string               `json:"raw_source,omitempty"`
	RawFileSHA256            string               `json:"raw_file_sha256,omitempty"`
	FileBytes                int64                `json:"file_bytes,omitempty"`
	HeaderRawSHA256          string               `json:"header_raw_sha256,omitempty"`
	HeaderNormalized         []string             `json:"header_normalized,omitempty"`
	HeaderNormalizedSHA256   string               `json:"header_normalized_sha256,omitempty"`
	MappingSHA256            string               `json:"mapping_sha256,omitempty"`
	ProjectionSHA256         string               `json:"projection_sha256,omitempty"`
	CheckpointSHA256         string               `json:"checkpoint_sha256,omitempty"`
	ArtifactHashes           map[string]string    `json:"artifact_hashes,omitempty"`
	Rows                     int                  `json:"rows"`
	TimeRanges               map[string]TimeRange `json:"time_ranges,omitempty"`
	IdentityPresence         map[string]int       `json:"identity_presence,omitempty"`
	OptionalPresence         map[string]int       `json:"optional_presence,omitempty"`
	StateCounts              map[string]int       `json:"state_counts,omitempty"`
	ForbiddenOutcomeColumns  []string             `json:"forbidden_outcome_columns,omitempty"`
	ForbiddenOutcomePresence map[string]int       `json:"forbidden_outcome_presence,omitempty"`
	Warnings                 []string             `json:"warnings"`
	Failure                  string               `json:"failure,omitempty"`
	ReceiptSHA256            string               `json:"receipt_sha256,omitempty"`
}

type Current struct {
	SchemaVersion string   `json:"schema_version"`
	Generation    string   `json:"generation"`
	ReceiptSHA256 string   `json:"receipt_sha256"`
	MappingSHA256 string   `json:"mapping_sha256"`
	Template      Template `json:"template"`
	Hash          string   `json:"hash"`
}

type checkpoint struct {
	SchemaVersion  string   `json:"schema_version"`
	RawSource      string   `json:"raw_source"`
	RawFileSHA256  string   `json:"raw_file_sha256"`
	MappingSHA256  string   `json:"mapping_sha256"`
	ProjectionHash string   `json:"projection_sha256"`
	Rows           int      `json:"rows"`
	Template       Template `json:"template"`
}

var required = map[Template][]string{
	OrderTemplate:   {"ordId", "instId", "instType", "state", "ordType", "side", "px", "sz", "cTime", "uTime"},
	TradingTemplate: {"ordId", "instId", "fillPx", "fillSz", "fillTime", "execType", "fee", "feeCcy"},
}

var allowedFields = map[Template]map[string]bool{
	OrderTemplate:   fieldSet("ordId", "instId", "instType", "state", "ordType", "side", "px", "sz", "cTime", "uTime", "clOrdId", "accFillSz", "cancelReason"),
	TradingTemplate: fieldSet("tradeId", "billId", "ordId", "instId", "fillPx", "fillSz", "fillTime", "execType", "fee", "feeCcy", "side", "posSide"),
}

// sourceAliases is the mapping authority. Unknown aliases fail closed even if
// they do not resemble an outcome field.
var sourceAliases = map[string]map[string]bool{
	"ordId":        aliases("Order", "ordId", "Order ID"),
	"tradeId":      aliases("Trade", "tradeId", "Trade ID"),
	"billId":       aliases("Bill", "billId", "Bill ID"),
	"instId":       aliases("Instrument", "instId", "Instrument ID"),
	"instType":     aliases("Type", "instType", "Instrument Type"),
	"state":        aliases("State", "Status", "Order State"),
	"ordType":      aliases("Order Type", "ordType"),
	"side":         aliases("Side"),
	"posSide":      aliases("Position Side", "posSide"),
	"px":           aliases("Price", "px", "Order Price"),
	"sz":           aliases("Size", "sz", "Order Size"),
	"cTime":        aliases("Created", "cTime", "Creation Time", "Created Time"),
	"uTime":        aliases("Updated", "uTime", "Update Time", "Updated Time"),
	"clOrdId":      aliases("Client", "clOrdId", "Client Order ID"),
	"accFillSz":    aliases("Fill", "accFillSz", "Accumulated Fill Size"),
	"cancelReason": aliases("Cancel", "Cancel Reason", "Cancellation Reason"),
	"fillPx":       aliases("Fill Price", "fillPx"),
	"fillSz":       aliases("Fill Size", "fillSz"),
	"fillTime":     aliases("Fill Time", "fillTime"),
	"execType":     aliases("Exec", "execType", "Execution Type"),
	"fee":          aliases("Fee"),
	"feeCcy":       aliases("Fee Ccy", "Fee Currency", "feeCcy"),
}

var sensitiveTokens = []string{
	"unrealizedpnl", "realizedpnl", "fillpnl", "closedpl", "netincome",
	"unrealized", "realized", "profit", "future", "return", "outcome",
	"pnl", "roi", "gain", "loss", "yield", "mfe", "mae",
}

func fieldSet(names ...string) map[string]bool {
	out := make(map[string]bool, len(names))
	for _, name := range names {
		out[name] = true
	}
	return out
}

func aliases(names ...string) map[string]bool {
	out := make(map[string]bool, len(names))
	for _, name := range names {
		out[canonicalHeader(name)] = true
	}
	return out
}

func canonicalHeader(value string) string {
	value = strings.TrimFunc(strings.TrimPrefix(value, "\ufeff"), func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	})
	var b strings.Builder
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

func isForbiddenHeader(value string) bool {
	canonical := canonicalHeader(value)
	if canonical == "pl" {
		return true
	}
	for _, token := range sensitiveTokens {
		if strings.Contains(canonical, token) {
			return true
		}
	}
	return false
}

func defaults(c Config) Config {
	if c.Root == "" {
		c.Root = DefaultRoot
	}
	if c.Inbox == "" {
		c.Inbox = filepath.Join(c.Root, "inbox")
	}
	if c.RootCap == 0 {
		c.RootCap = maxRootCap
	}
	return c
}

func validateConfig(c Config) (string, string, error) {
	if c.RootCap <= 0 || c.RootCap > maxRootCap {
		return "", "", errors.New("reportcenter: invalid root cap")
	}
	root, err := filepath.Abs(c.Root)
	if err != nil {
		return "", "", err
	}
	if root != filepath.Clean(c.Root) || filepath.Clean(c.Inbox) != filepath.Join(root, "inbox") {
		return "", "", errors.New("reportcenter: non-canonical root or inbox")
	}
	csvName, mappingName := filepath.Base(c.CSVPath), filepath.Base(c.MappingPath)
	if csvName == "." || mappingName == "." ||
		filepath.Clean(c.CSVPath) != filepath.Join(root, "inbox", csvName) ||
		filepath.Clean(c.MappingPath) != filepath.Join(root, "mappings", mappingName) {
		return "", "", errors.New("reportcenter: fixed private input directories required")
	}
	return csvName, mappingName, nil
}

func updateTimeRange(ranges map[string]TimeRange, field string, value time.Time) {
	text := value.Format(time.RFC3339Nano)
	current, ok := ranges[field]
	if !ok {
		ranges[field] = TimeRange{Min: text, Max: text}
		return
	}
	minTime, _ := time.Parse(time.RFC3339Nano, current.Min)
	maxTime, _ := time.Parse(time.RFC3339Nano, current.Max)
	if value.Before(minTime) {
		current.Min = text
	}
	if value.After(maxTime) {
		current.Max = text
	}
	ranges[field] = current
}

func marshalJSON(value any) ([]byte, error) {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

func hashBytes(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func hashStrings(values []string) string {
	raw, _ := json.Marshal(values)
	return hashBytes(raw)
}
func hashReceipt(receipt Receipt) string {
	receipt.ReceiptSHA256 = ""
	raw, _ := json.Marshal(receipt)
	return hashBytes(raw)
}
func hashCurrent(current Current) string {
	current.Hash = ""
	raw, _ := json.Marshal(current)
	return hashBytes(raw)
}
func fault(cfg Config, step string) error {
	if cfg.Fault != nil {
		return cfg.Fault(step)
	}
	return nil
}
func warnings() []string {
	return []string{"MANUAL_EXPORT_UNVERIFIED", "ORIGINAL_RECEIVE_TIME_ABSENT", "INCOMPLETE_ATTEMPTS_DENOMINATOR"}
}
