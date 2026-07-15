package okxarchive

import (
	"encoding/json"
	"errors"
	"math"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"lingxi/internal/historydownload"
)

var safeArchiveInstrument = regexp.MustCompile(`^[A-Z0-9-]{3,40}$`)

type DiscoveryRequest struct {
	Module     string
	InstType   string
	Instrument string
	UTCDate    string
	BeginMS    int64
	EndMS      int64
	ArchiveCap int64
}

type SelectedArchive struct {
	Filename      string
	URL           string
	URLHash       string
	DeclaredBytes int64
	DateMS        int64
	Module        string
	InstType      string
	Instrument    string
	UTCDate       string
}

func SelectArchive(raw []byte, request DiscoveryRequest) (SelectedArchive, error) {
	if err := validateDiscoveryRequest(request); err != nil {
		return SelectedArchive{}, err
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil || !onlyKeys(document, "code", "msg", "data") ||
		stringField(document, "code", "") != "0" {
		return SelectedArchive{}, errors.New("okxarchive: invalid discovery envelope")
	}
	rawData, ok := document["data"].([]any)
	if !ok {
		return SelectedArchive{}, errors.New("okxarchive: invalid discovery data")
	}
	envelopeData := make([]map[string]any, 0, len(rawData))
	for _, value := range rawData {
		entry, ok := value.(map[string]any)
		if !ok || !onlyKeys(entry, "dateAggrType", "details", "totalSizeMB", "ts") ||
			stringField(entry, "dateAggrType", "") != "daily" {
			return SelectedArchive{}, errors.New("okxarchive: unknown discovery data structure")
		}
		envelopeData = append(envelopeData, entry)
	}
	if len(envelopeData) != 1 {
		return SelectedArchive{}, errors.New("okxarchive: discovery scope is not exactly one data group")
	}
	data := envelopeData[0]
	details, ok := data["details"].([]any)
	if !ok || len(details) != 1 {
		return SelectedArchive{}, errors.New("okxarchive: discovery scope is not exactly one detail")
	}
	detail, ok := details[0].(map[string]any)
	if !ok || !onlyKeys(detail, "ccy", "dateRangeEnd", "dateRangeStart", "groupDetails", "groupSizeMB", "instFamily", "instId", "instType") {
		return SelectedArchive{}, errors.New("okxarchive: invalid discovery detail")
	}
	rangeStart, startErr := decimalField(detail["dateRangeStart"])
	rangeEnd, endErr := decimalField(detail["dateRangeEnd"])
	if startErr != nil || endErr != nil || rangeStart != request.BeginMS || rangeEnd != request.EndMS+1 {
		return SelectedArchive{}, errors.New("okxarchive: discovery detail range outside exact request")
	}
	instID := stringField(detail, "instId", "")
	instType := stringField(detail, "instType", "")
	groups, ok := detail["groupDetails"].([]any)
	if !ok || len(groups) != 1 {
		return SelectedArchive{}, errors.New("okxarchive: discovery scope is not exactly one file")
	}
	group, ok := groups[0].(map[string]any)
	if !ok || !onlyKeys(group, "dateTs", "filename", "sizeMB", "url") {
		return SelectedArchive{}, errors.New("okxarchive: invalid discovery group")
	}
	filename := stringField(group, "filename", "")
	rawURL := stringField(group, "url", "")
	lowerEvidence := strings.ToLower(filename + " " + rawURL)
	if strings.Contains(lowerEvidence, "allswap") || strings.Contains(lowerEvidence, "allspot") {
		return SelectedArchive{}, errors.New("okxarchive: full-market archive rejected")
	}
	dateMS, err := decimalField(group["dateTs"])
	if err != nil {
		return SelectedArchive{}, errors.New("okxarchive: invalid discovery date")
	}
	if instID != request.Instrument || instType != request.InstType || dateMS != request.BeginMS {
		return SelectedArchive{}, errors.New("okxarchive: discovery entry is outside exact request scope")
	}
	declared, err := sizeMBBytes(group["sizeMB"])
	if err != nil || declared > request.ArchiveCap {
		return SelectedArchive{}, errors.New("okxarchive: declared archive size exceeds cap")
	}
	if err := validateArchiveBinding(filename, rawURL, request); err != nil {
		return SelectedArchive{}, err
	}
	return SelectedArchive{
		Filename: filename, URL: rawURL, URLHash: historydownload.HashBytes([]byte(rawURL)),
		DeclaredBytes: declared, DateMS: dateMS, Module: request.Module,
		InstType: request.InstType, Instrument: request.Instrument, UTCDate: request.UTCDate,
	}, nil
}

func onlyKeys(value map[string]any, allowed ...string) bool {
	set := make(map[string]bool, len(allowed))
	for _, key := range allowed {
		set[key] = true
	}
	for key := range value {
		if !set[key] {
			return false
		}
	}
	return true
}

func validateDiscoveryRequest(request DiscoveryRequest) error {
	if request.Module != "1" {
		return errors.New("okxarchive: unsupported smoke module")
	}
	if !safeArchiveInstrument.MatchString(request.Instrument) || strings.Contains(request.Instrument, ",") ||
		request.InstType == "" || request.ArchiveCap <= 0 || request.ArchiveCap > MaxArchiveBytes {
		return errors.New("okxarchive: invalid one-instrument smoke request")
	}
	date, err := time.Parse("2006-01-02", request.UTCDate)
	if err != nil || date.UnixMilli() != request.BeginMS ||
		request.EndMS != request.BeginMS+int64(24*time.Hour/time.Millisecond)-1 {
		return errors.New("okxarchive: request is not one exact UTC day")
	}
	return nil
}

func validateArchiveBinding(filename, rawURL string, request DiscoveryRequest) error {
	if filename == "" || path.Base(filename) != filename || !strings.HasSuffix(strings.ToLower(filename), ".zip") {
		return errors.New("okxarchive: discovery filename rejected")
	}
	parsed, err := ValidateURL(rawURL)
	if err != nil || !strings.EqualFold(parsed.Host, "static.okx.com") || parsed.Fragment != "" || parsed.User != nil {
		return errors.New("okxarchive: discovery URL rejected")
	}
	if parsed.RawQuery != "" {
		query := parsed.Query()
		versions := query["v"]
		if len(query) != 1 || len(versions) != 1 || !decimalString(versions[0]) || len(versions[0]) > 12 {
			return errors.New("okxarchive: discovery URL query rejected")
		}
	}
	unescaped, err := url.PathUnescape(parsed.Path)
	if err != nil || path.Base(unescaped) != filename {
		return errors.New("okxarchive: URL filename mismatch")
	}
	lower := strings.ToLower(filename)
	if !strings.HasPrefix(lower, strings.ToLower(request.Instrument)+"-") {
		return errors.New("okxarchive: filename instrument mismatch")
	}
	dateCompact := strings.ReplaceAll(request.UTCDate, "-", "")
	if !strings.Contains(filename, request.UTCDate) && !strings.Contains(filename, dateCompact) {
		return errors.New("okxarchive: filename date mismatch")
	}
	if !strings.Contains(lower, "-trades-") {
		return errors.New("okxarchive: filename module mismatch")
	}
	return nil
}

func decimalString(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func sizeMBBytes(value any) (int64, error) {
	var size float64
	switch typed := value.(type) {
	case string:
		parsed, err := strconv.ParseFloat(typed, 64)
		if err != nil {
			return 0, err
		}
		size = parsed
	case float64:
		size = typed
	default:
		return 0, errors.New("invalid size")
	}
	if size <= 0 || math.IsNaN(size) || math.IsInf(size, 0) {
		return 0, errors.New("invalid size")
	}
	return int64(math.Ceil(size * 1024 * 1024)), nil
}

func decimalField(value any) (int64, error) {
	switch typed := value.(type) {
	case string:
		return strconv.ParseInt(typed, 10, 64)
	case float64:
		if typed != math.Trunc(typed) {
			return 0, errors.New("not integer")
		}
		return int64(typed), nil
	default:
		return 0, errors.New("not decimal")
	}
}

func stringField(value map[string]any, key, fallback string) string {
	if text, ok := value[key].(string); ok {
		return text
	}
	return fallback
}
