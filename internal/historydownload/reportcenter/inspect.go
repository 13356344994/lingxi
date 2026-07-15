package reportcenter

import (
	"bufio"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"lingxi/internal/historydownload"
)

func Inspect(cfg Config) (Receipt, error) {
	cfg = defaults(cfg)
	csvName, mappingName, err := validateConfig(cfg)
	if err != nil {
		return fail(err)
	}
	lock, err := historydownload.AcquireRootLock(cfg.Root)
	if err != nil {
		return fail(err)
	}
	defer lock.Close()
	rootFile, err := lock.OpenRootDir()
	if err != nil {
		return fail(err)
	}
	root, err := heldDirectory(rootFile)
	if err != nil {
		_ = rootFile.Close()
		return fail(err)
	}
	defer root.close()
	inbox, err := openDirAt(root, "inbox", false)
	if err != nil {
		return fail(err)
	}
	defer inbox.close()
	mappings, err := openDirAt(root, "mappings", false)
	if err != nil {
		return fail(err)
	}
	defer mappings.close()
	rawFile, initialStat, err := openRegularAt(inbox, csvName, syscall.O_RDONLY, 0o600)
	if err != nil {
		return fail(err)
	}
	defer rawFile.Close()
	if filepath.Ext(csvName) != ".csv" || int64(initialStat.size) > cfg.RootCap {
		return fail(errors.New("reportcenter: invalid or over-cap CSV"))
	}
	firstHash, err := hashOpenFile(rawFile)
	if err != nil {
		return fail(err)
	}
	if err := fault(cfg, "after_initial_hash"); err != nil {
		return reject(Receipt{Warnings: warnings()}, "CRASH_INJECTED", err)
	}
	if _, err := rawFile.Seek(0, io.SeekStart); err != nil {
		return fail(err)
	}
	mappingRaw, err := readFileAt(mappings, mappingName, 0o600, 1<<20)
	if err != nil {
		return fail(err)
	}
	mapping, err := parseMapping(mappingRaw)
	if err != nil {
		return fail(err)
	}
	if usage, err := rootUsage(root); err != nil || usage > cfg.RootCap {
		if err == nil {
			err = errors.New("reportcenter: root cap exceeded")
		}
		return fail(err)
	}
	receipt := Receipt{
		SchemaVersion: "okx-report-center.receipt.v2", Template: mapping.Template,
		RawSource: csvName, RawFileSHA256: firstHash, FileBytes: int64(initialStat.size),
		MappingSHA256: hashBytes(mappingRaw), ArtifactHashes: map[string]string{},
		TimeRanges: map[string]TimeRange{}, IdentityPresence: map[string]int{},
		OptionalPresence: map[string]int{}, StateCounts: map[string]int{},
		ForbiddenOutcomePresence: map[string]int{}, Warnings: warnings(),
	}
	reader := csv.NewReader(bufio.NewReader(rawFile))
	reader.FieldsPerRecord = -1
	header, err := reader.Read()
	if err != nil {
		return reject(receipt, "CSV_MALFORMED", err)
	}
	if err := validateUTF8(header); err != nil {
		return reject(receipt, "ENCODING_AMBIGUITY", err)
	}
	headerHash := sha256.New()
	if _, err := io.Copy(headerHash, io.NewSectionReader(rawFile, 0, reader.InputOffset())); err != nil {
		return reject(receipt, "CSV_MALFORMED", err)
	}
	receipt.HeaderRawSHA256 = hex.EncodeToString(headerHash.Sum(nil))
	normalized, indexes, forbiddenIndexes, err := inspectHeaders(header, &receipt)
	if err != nil {
		return reject(receipt, "DUPLICATE_COLUMNS", err)
	}
	receipt.HeaderNormalized = normalized
	receipt.HeaderNormalizedSHA256 = hashStrings(normalized)
	bindings, err := bindMapping(mapping, header, indexes)
	if err != nil {
		return reject(receipt, "SCHEMA_MAPPING_INVALID", err)
	}
	generations, err := openDirAt(root, "generations", true)
	if err != nil {
		return reject(receipt, "STAGING_FAILED", err)
	}
	defer generations.close()
	stageName, stage, err := createStage(generations)
	if err != nil {
		return reject(receipt, "STAGING_FAILED", err)
	}
	renamed := false
	defer func() {
		if renamed {
			stage.close()
		} else {
			removeStage(generations, stageName, stage)
		}
	}()
	if err := atomicWriteAt(stage, "mapping.json", mappingRaw, 0o600); err != nil {
		return reject(receipt, "STAGING_FAILED", err)
	}
	projected, _, err := openRegularAt(stage, "projection.ndjson", syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL, 0o600)
	if err != nil {
		return reject(receipt, "STAGING_FAILED", err)
	}
	writer := bufio.NewWriter(projected)
	projectionHash := sha256.New()
	tokens, err := loadTokenizer(root)
	if err != nil {
		_ = projected.Close()
		return reject(receipt, "TOKEN_FAILED", err)
	}
	seen := map[string]struct{}{}
	var projectionBytes int64
	for {
		row, readErr := reader.Read()
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil || len(row) != len(header) {
			_ = projected.Close()
			return reject(receipt, "CSV_MALFORMED", errors.New("reportcenter: malformed CSV row"))
		}
		if err := validateRow(row, forbiddenIndexes); err != nil {
			_ = projected.Close()
			return reject(receipt, "FORMULA_OR_ENCODING_AMBIGUITY", err)
		}
		record, err := inspectRow(mapping, header, bindings, row, tokens, seen, &receipt)
		if err != nil {
			_ = projected.Close()
			return reject(receipt, "IDENTITY_OR_TIME_INVALID", err)
		}
		encoded, err := json.Marshal(record)
		if err != nil {
			_ = projected.Close()
			return reject(receipt, "PROJECTION_FAILED", err)
		}
		encoded = append(encoded, '\n')
		projectionBytes += int64(len(encoded))
		if projectionBytes > cfg.RootCap {
			_ = projected.Close()
			return reject(receipt, "ROOT_CAP", errors.New("reportcenter: projection exceeds cap"))
		}
		if _, err := writer.Write(encoded); err != nil {
			_ = projected.Close()
			return reject(receipt, "PROJECTION_FAILED", err)
		}
		_, _ = projectionHash.Write(encoded)
		receipt.Rows++
		if receipt.Rows%1024 == 0 {
			if err := writer.Flush(); err != nil {
				_ = projected.Close()
				return reject(receipt, "PROJECTION_FAILED", err)
			}
			if err := validateHeld(root, inbox, mappings, generations, stage); err != nil {
				_ = projected.Close()
				return reject(receipt, "DIRECTORY_CHANGED", err)
			}
			if usage, err := rootUsage(root); err != nil || usage > cfg.RootCap {
				_ = projected.Close()
				if err == nil {
					err = errors.New("reportcenter: root cap exceeded")
				}
				return reject(receipt, "ROOT_CAP", err)
			}
		}
	}
	if err := writer.Flush(); err != nil {
		_ = projected.Close()
		return reject(receipt, "PROJECTION_FAILED", err)
	}
	if err := projected.Sync(); err != nil {
		_ = projected.Close()
		return reject(receipt, "PROJECTION_FAILED", err)
	}
	if err := projected.Close(); err != nil {
		return reject(receipt, "PROJECTION_FAILED", err)
	}
	if _, err := rawFile.Seek(0, io.SeekStart); err != nil {
		return reject(receipt, "SOURCE_CHANGED", err)
	}
	secondHash, err := hashOpenFile(rawFile)
	finalStat, statErr := snapshotFile(rawFile, 0o600)
	if err != nil || statErr != nil || secondHash != firstHash || !sameSnapshot(initialStat, finalStat) {
		return reject(receipt, "SOURCE_CHANGED", errors.New("reportcenter: raw source changed during inspection"))
	}
	receipt.ProjectionSHA256 = hex.EncodeToString(projectionHash.Sum(nil))
	if err := fault(cfg, "after_stream"); err != nil {
		return reject(receipt, "CRASH_INJECTED", err)
	}
	published, wasRenamed, err := publish(cfg, root, generations, stageName, stage, mappingRaw, receipt)
	renamed = wasRenamed
	return published, err
}

func parseMapping(raw []byte) (Mapping, error) {
	var mapping Mapping
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&mapping); err != nil {
		return Mapping{}, errors.New("reportcenter: invalid mapping JSON")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Mapping{}, errors.New("reportcenter: trailing mapping JSON")
	}
	if _, ok := allowedFields[mapping.Template]; !ok || mapping.Columns == nil {
		return Mapping{}, errors.New("reportcenter: explicit order or trading mapping required")
	}
	for canonical, source := range mapping.Columns {
		aliasesForField := sourceAliases[canonical]
		if !allowedFields[mapping.Template][canonical] || source == "" ||
			isForbiddenHeader(source) || !aliasesForField[canonicalHeader(source)] {
			return Mapping{}, errors.New("reportcenter: source header is not an audited alias")
		}
	}
	for _, name := range required[mapping.Template] {
		if mapping.Columns[name] == "" {
			return Mapping{}, fmt.Errorf("reportcenter: required mapping %s missing", name)
		}
	}
	if mapping.Template == TradingTemplate && mapping.Columns["tradeId"] == "" && mapping.Columns["billId"] == "" {
		return Mapping{}, errors.New("reportcenter: trading identity mapping missing")
	}
	return mapping, nil
}

func inspectHeaders(header []string, receipt *Receipt) ([]string, map[string]int, map[int]bool, error) {
	normalized := make([]string, len(header))
	indexes := map[string]int{}
	forbiddenIndexes := map[int]bool{}
	for index, source := range header {
		name := canonicalHeader(source)
		if name == "" {
			return nil, nil, nil, errors.New("reportcenter: empty header")
		}
		if _, duplicate := indexes[name]; duplicate {
			return nil, nil, nil, errors.New("reportcenter: duplicate canonical header")
		}
		normalized[index], indexes[name] = name, index
		if isForbiddenHeader(source) {
			forbiddenIndexes[index] = true
			receipt.ForbiddenOutcomeColumns = append(receipt.ForbiddenOutcomeColumns, name)
			receipt.ForbiddenOutcomePresence[name] = 1
		}
	}
	sort.Strings(receipt.ForbiddenOutcomeColumns)
	return normalized, indexes, forbiddenIndexes, nil
}

func bindMapping(mapping Mapping, headers []string, indexes map[string]int) (map[string]int, error) {
	out := map[string]int{}
	for canonical, source := range mapping.Columns {
		sourceCanonical := canonicalHeader(source)
		if isForbiddenHeader(source) || !sourceAliases[canonical][sourceCanonical] {
			return nil, errors.New("reportcenter: source header is not an audited alias")
		}
		index, ok := indexes[sourceCanonical]
		if !ok || headers[index] != source {
			return nil, errors.New("reportcenter: exact mapped source header absent")
		}
		for _, prior := range out {
			if prior == index {
				return nil, errors.New("reportcenter: source header mapped more than once")
			}
		}
		out[canonical] = index
	}
	return out, nil
}

func inspectRow(mapping Mapping, headers []string, bindings map[string]int, row []string, tokens tokenizer, seen map[string]struct{}, receipt *Receipt) (map[string]string, error) {
	idField := "ordId"
	if mapping.Template == TradingTemplate {
		if mapping.Columns["tradeId"] != "" {
			idField = "tradeId"
		} else {
			idField = "billId"
		}
	}
	identity := strings.TrimSpace(row[bindings[idField]])
	if identity == "" {
		return nil, errors.New("reportcenter: missing identity")
	}
	if _, duplicate := seen[identity]; duplicate {
		return nil, errors.New("reportcenter: duplicate identity")
	}
	seen[identity] = struct{}{}
	receipt.IdentityPresence[idField]++
	timeFields := []string{"fillTime"}
	if mapping.Template == OrderTemplate {
		timeFields = []string{"cTime", "uTime"}
		state := strings.ToLower(strings.TrimSpace(row[bindings["state"]]))
		if state == "" {
			return nil, errors.New("reportcenter: missing physical state")
		}
		receipt.StateCounts[state]++
	}
	for _, field := range timeFields {
		parsed, err := parseTime(strings.TrimSpace(row[bindings[field]]))
		if err != nil {
			return nil, errors.New("reportcenter: invalid event time")
		}
		updateTimeRange(receipt.TimeRanges, field, parsed)
	}
	for _, field := range []string{"clOrdId", "accFillSz", "cancelReason"} {
		if index, ok := bindings[field]; ok && strings.TrimSpace(row[index]) != "" {
			receipt.OptionalPresence[field]++
		}
	}
	out := map[string]string{}
	for canonical, index := range bindings {
		sourceCanonical := canonicalHeader(headers[index])
		if isForbiddenHeader(headers[index]) || !sourceAliases[canonical][sourceCanonical] {
			return nil, errors.New("reportcenter: unaudited source reached projection")
		}
		value := row[index]
		if canonical == "ordId" || canonical == "tradeId" || canonical == "billId" || canonical == "clOrdId" {
			if value == "" {
				continue
			}
			token, err := tokens.token(value)
			if err != nil {
				return nil, err
			}
			out[canonical+"Token"] = token
		} else {
			out[canonical] = value
		}
	}
	return out, nil
}

func parseTime(value string) (time.Time, error) {
	if len(value) == 10 || len(value) == 13 {
		for _, r := range value {
			if r < '0' || r > '9' {
				return time.Time{}, errors.New("reportcenter: mixed numeric time")
			}
		}
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return time.Time{}, err
		}
		if len(value) == 13 {
			return time.UnixMilli(n).UTC(), nil
		}
		return time.Unix(n, 0).UTC(), nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, errors.New("reportcenter: timezone-qualified RFC3339 required")
	}
	return parsed, nil
}

func validateUTF8(values []string) error {
	for _, value := range values {
		if !utf8.ValidString(value) {
			return errors.New("reportcenter: invalid UTF-8")
		}
	}
	return nil
}

func validateRow(row []string, forbiddenIndexes map[int]bool) error {
	for index, value := range row {
		if forbiddenIndexes[index] {
			continue
		}
		if !utf8.ValidString(value) {
			return errors.New("reportcenter: invalid UTF-8")
		}
		trimmed := strings.TrimLeftFunc(value, func(r rune) bool {
			return unicode.IsSpace(r) || unicode.IsControl(r)
		})
		if trimmed != "" && strings.ContainsRune("=+-@", rune(trimmed[0])) {
			return errors.New("reportcenter: formula-like cell")
		}
	}
	return nil
}

func hashOpenFile(file *os.File) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func validateHeld(dirs ...*directory) error {
	for _, dir := range dirs {
		if err := dir.validate(); err != nil {
			return err
		}
	}
	return nil
}

func fail(err error) (Receipt, error) {
	return Receipt{SchemaVersion: "okx-report-center.receipt.v2", Failure: "CONFIG_OR_INPUT_INVALID", Warnings: warnings()}, err
}

func reject(receipt Receipt, code string, err error) (Receipt, error) {
	receipt.Accepted, receipt.Failure = false, code
	return receipt, fmt.Errorf("reportcenter: %s: %w", code, err)
}
