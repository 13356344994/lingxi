package historydownload

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	maxCheckpointPages   = 10000
	maxCheckpointRecords = 100000
	stateActive          = "active"
	statePendingManifest = "pending_manifest"
	statePublished       = "published"
)

type ManifestPublisher func(string, Manifest) error

type Page struct {
	Raw          []byte
	Records      []map[string]any
	NextCursor   string
	ReceiveTime  time.Time
	EventTimeMin time.Time
	EventTimeMax time.Time
}

type PageFetcher func(context.Context, string) (Page, error)

type DownloadConfig struct {
	Endpoint            string
	RawDirectory        string
	ProjectionDirectory string
	CheckpointPath      string
	ManifestPath        string
	QueryHash           string
	SchemaHash          string
	DiskCapBytes        int64
	RootDirectory       string
	RootCapBytes        int64
	RootLock            *RootLock
	MaxRetries          int
	RetryDelay          time.Duration
	Private             bool
	PITStatus           string
	Warnings            []string
	ClockFitness        string
	DenominatorFitness  string
	TokenStore          TokenStore
	PublishManifest     ManifestPublisher
	OnProgress          func(page, records int, bytes int64)
}

type DownloadResult struct {
	Pages       int
	Records     int
	Bytes       int64
	Retries     int
	Manifest    Manifest
	CompletedAt time.Time
}

// Download resumes only from a matching sealed checkpoint, writes each raw
// page atomically outside the repository, and stops before exceeding disk cap.
func Download(ctx context.Context, cfg DownloadConfig, fetch PageFetcher) (DownloadResult, error) {
	if cfg.Endpoint == "" || cfg.RawDirectory == "" || cfg.ProjectionDirectory == "" || cfg.CheckpointPath == "" || cfg.ManifestPath == "" ||
		cfg.QueryHash == "" || cfg.SchemaHash == "" || cfg.DiskCapBytes <= 0 || cfg.RootDirectory == "" || cfg.RootCapBytes <= 0 ||
		cfg.TokenStore.SaltPath == "" || fetch == nil {
		return DownloadResult{}, errors.New("historydownload: endpoint, raw directory, checkpoint, disk cap, and fetcher required")
	}
	if err := cfg.RootLock.Validate(cfg.RootDirectory); err != nil {
		return DownloadResult{}, err
	}
	if err := validateManagedUnderRoot(cfg); err != nil {
		return DownloadResult{}, err
	}
	if err := os.MkdirAll(cfg.RawDirectory, 0o700); err != nil {
		return DownloadResult{}, err
	}
	if err := os.MkdirAll(cfg.ProjectionDirectory, 0o755); err != nil {
		return DownloadResult{}, err
	}
	if err := cleanupAtomicTemps(cfg); err != nil {
		return DownloadResult{}, err
	}
	saltAddition := int64(0)
	if _, err := os.Stat(cfg.TokenStore.SaltPath); os.IsNotExist(err) {
		saltAddition = 32
	} else if err != nil {
		return DownloadResult{}, err
	}
	if saltAddition > cfg.DiskCapBytes {
		return DownloadResult{}, errors.New("historydownload: managed disk cap would be exceeded")
	}
	if err := CheckRootCapacity(cfg.RootDirectory, cfg.RootCapBytes, saltAddition); err != nil {
		return DownloadResult{}, err
	}
	tokenSaltID, err := cfg.TokenStore.Identity()
	if err != nil {
		return DownloadResult{}, err
	}
	semanticsHash, err := hash(struct {
		PITStatus          string
		Warnings           []string
		ClockFitness       string
		DenominatorFitness string
	}{cfg.PITStatus, cfg.Warnings, cfg.ClockFitness, cfg.DenominatorFitness})
	if err != nil {
		return DownloadResult{}, err
	}
	cp, exists, err := LoadCheckpoint(cfg.CheckpointPath)
	if err != nil {
		return DownloadResult{}, err
	}
	if exists && (cp.Endpoint != cfg.Endpoint || cp.QueryHash != cfg.QueryHash || cp.SchemaHash != cfg.SchemaHash ||
		cp.RawDirectory != cfg.RawDirectory || cp.OutputDir != cfg.ProjectionDirectory || cp.ManifestPath != cfg.ManifestPath ||
		cp.Private != cfg.Private || cp.DiskCapBytes != cfg.DiskCapBytes || cp.TokenSaltID != tokenSaltID ||
		cp.RootDirectory != cfg.RootDirectory || cp.RootCapBytes != cfg.RootCapBytes || cp.SemanticsHash != semanticsHash) {
		return DownloadResult{}, errors.New("historydownload: checkpoint critical configuration mismatch")
	}
	if !exists {
		cp = Checkpoint{
			Endpoint: cfg.Endpoint, QueryHash: cfg.QueryHash, SchemaHash: cfg.SchemaHash,
			RawDirectory: cfg.RawDirectory, OutputDir: cfg.ProjectionDirectory, ManifestPath: cfg.ManifestPath,
			Private: cfg.Private, DiskCapBytes: cfg.DiskCapBytes, TokenSaltID: tokenSaltID,
			RootDirectory: cfg.RootDirectory, RootCapBytes: cfg.RootCapBytes,
			SemanticsHash: semanticsHash, PublishState: stateActive,
			Metadata:       map[string]string{"started_at": time.Now().UTC().Format(time.RFC3339Nano)},
			PresenceCounts: map[string]int{}, OmittedCounts: map[string]int{},
		}
	}
	if err := verifyRawPages(cfg.RawDirectory, cp.PageRawHashes); err != nil {
		return DownloadResult{}, err
	}
	if cp.PublishState == statePublished {
		return loadPublishedResult(cfg, cp)
	}
	if cp.PublishState == statePendingManifest {
		return finishDownload(cfg, cp, DownloadResult{Pages: cp.Page, Records: cp.RecordCount, Bytes: cp.BytesWritten, Retries: cp.Retries})
	}
	if cp.PublishState != stateActive {
		return DownloadResult{}, errors.New("historydownload: invalid checkpoint publish state")
	}
	rawCursor, err := recoverRawCursor(cfg.Endpoint, cfg.RawDirectory, cp.PageRawHashes)
	if err != nil {
		return DownloadResult{}, err
	}
	result := DownloadResult{Pages: cp.Page, Records: cp.RecordCount, Bytes: cp.BytesWritten, Retries: cp.Retries}
	for {
		var page Page
		for attempt := 0; ; attempt++ {
			page, err = fetch(ctx, rawCursor)
			if err == nil {
				break
			}
			if attempt >= cfg.MaxRetries {
				var safe safeFetchError
				if errors.As(err, &safe) {
					return result, fmt.Errorf("historydownload: page %d fetch failed: %s", cp.Page+1, safe.Error())
				}
				return result, fmt.Errorf("historydownload: page %d fetch failed", cp.Page+1)
			}
			delay := cfg.RetryDelay
			if delay <= 0 {
				delay = time.Second
			}
			select {
			case <-ctx.Done():
				return result, ctx.Err()
			case <-time.After(delay * time.Duration(attempt+1)):
			}
			cp.Retries++
			result.Retries = cp.Retries
		}
		if len(page.Raw) == 0 {
			return result, errors.New("historydownload: empty raw envelope")
		}
		if len(page.Records) == 0 {
			updateClockRanges(&cp, page)
			return finishDownload(cfg, cp, result)
		}
		cursorToken, err := tokenOptional(cfg.TokenStore, page.NextCursor)
		if err != nil {
			return result, err
		}
		if cursorToken != "" && (cursorToken == cp.CursorToken || contains(cp.CursorTokens, cursorToken)) {
			return result, errors.New("historydownload: cursor did not advance or looped")
		}
		records, recordKeys, duplicates, err := deduplicateRecords(cfg.Endpoint, page.Records, cp.RecordKeys, cfg.TokenStore)
		if err != nil {
			var missing MissingIdentityError
			if errors.As(err, &missing) {
				cp.MissingIDCount = missing.Count
				if saveErr := SaveCheckpoint(cfg.CheckpointPath, cp); saveErr != nil {
					return result, errors.Join(err, saveErr)
				}
			}
			return result, err
		}
		if err := validatePageOrder(cfg.Endpoint, rawCursor, page.Records, page.NextCursor); err != nil {
			return result, err
		}
		if cp.Page >= maxCheckpointPages || len(cp.RecordKeys)+len(recordKeys) > maxCheckpointRecords {
			return result, errors.New("historydownload: checkpoint collection limit exceeded")
		}
		projection, err := Project(cfg.Endpoint, records, cfg.TokenStore)
		if err != nil {
			return result, err
		}
		if projection.SchemaHash != cfg.SchemaHash {
			return result, errors.New("historydownload: projection schema drift")
		}
		rawHash := HashBytes(page.Raw)
		if contains(cp.PageRawHashes, rawHash) {
			return result, errors.New("historydownload: duplicate raw page")
		}
		name := fmt.Sprintf("page-%06d.json", cp.Page+1)
		projectionBytes, err := json.MarshalIndent(projection.Records, "", "  ")
		if err != nil {
			return result, err
		}
		projectionBytes = append(projectionBytes, '\n')
		rawPath := filepath.Join(cfg.RawDirectory, name)
		projectionPath := filepath.Join(cfg.ProjectionDirectory, name)
		rawExists, err := existingBytesMatch(rawPath, page.Raw)
		if err != nil {
			return result, err
		}
		projectionExists, err := existingBytesMatch(projectionPath, projectionBytes)
		if err != nil {
			return result, err
		}
		var adding int64
		if !rawExists {
			adding += int64(len(page.Raw))
		}
		if !projectionExists {
			adding += int64(len(projectionBytes))
		}
		if err := checkManagedCap(cfg, adding); err != nil {
			return result, err
		}
		if !rawExists {
			if err := WriteBytesAtomic(rawPath, page.Raw, 0o600); err != nil {
				return result, err
			}
		}
		if !projectionExists {
			if err := writeAtomicBytes(projectionPath, projectionBytes, 0o644); err != nil {
				return result, err
			}
		}
		cp.Page++
		cp.CursorToken = cursorToken
		cp.CursorTokens = append(cp.CursorTokens, cursorToken)
		cp.PageRawHashes = append(cp.PageRawHashes, rawHash)
		cp.RecordKeys = append(cp.RecordKeys, recordKeys...)
		cp.BytesWritten += int64(len(page.Raw))
		cp.RecordCount += len(records)
		cp.DuplicateCount += duplicates
		for field, count := range projection.PresenceCounts {
			cp.PresenceCounts[field] += count
		}
		for field, count := range projection.OmittedCounts {
			cp.OmittedCounts[field] += count
		}
		updateClockRanges(&cp, page)
		result.Pages, result.Bytes, result.Records = cp.Page, cp.BytesWritten, cp.RecordCount
		checkpointSize, err := checkpointEncodedSize(cp)
		if err != nil {
			return result, err
		}
		if err := checkManagedCap(cfg, checkpointSize); err != nil {
			return result, err
		}
		if err := SaveCheckpoint(cfg.CheckpointPath, cp); err != nil {
			return result, err
		}
		if cfg.OnProgress != nil {
			cfg.OnProgress(cp.Page, cp.RecordCount, cp.BytesWritten)
		}
		if page.NextCursor == "" {
			return finishDownload(cfg, cp, result)
		}
		rawCursor = page.NextCursor
	}
}

func validateManagedUnderRoot(cfg DownloadConfig) error {
	root, err := canonicalPotentialPath(cfg.RootDirectory)
	if err != nil {
		return err
	}
	for _, candidate := range []string{
		cfg.RawDirectory, cfg.ProjectionDirectory, cfg.CheckpointPath, cfg.ManifestPath, cfg.TokenStore.SaltPath,
	} {
		canonical, err := canonicalPotentialPath(candidate)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, canonical)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
			return errors.New("historydownload: managed path escapes root")
		}
	}
	return nil
}

func deduplicateRecords(endpoint string, records []map[string]any, known []string, tokens TokenStore) ([]map[string]any, []string, int, error) {
	seen := make(map[string]bool, len(known)+len(records))
	for _, key := range known {
		seen[key] = true
	}
	out := make([]map[string]any, 0, len(records))
	added := make([]string, 0, len(records))
	duplicates := 0
	missing := 0
	for _, record := range records {
		rawKey := recordIdentity(endpoint, record)
		if rawKey == "" {
			missing++
			continue
		}
		key, err := tokens.Token(endpoint + ":" + rawKey)
		if err != nil {
			return nil, nil, duplicates, err
		}
		if seen[key] {
			duplicates++
			continue
		}
		seen[key] = true
		added = append(added, key)
		out = append(out, record)
	}
	if missing > 0 {
		return nil, nil, duplicates, MissingIdentityError{Field: identityField(endpoint), Count: missing}
	}
	return out, added, duplicates, nil
}

type MissingIdentityError struct {
	Field string
	Count int
}

func (e MissingIdentityError) Error() string {
	return fmt.Sprintf("historydownload: required %s identity missing (count=%d)", e.Field, e.Count)
}

func recordIdentity(endpoint string, record map[string]any) string {
	field := identityField(endpoint)
	value, ok := record[field].(string)
	if !ok || value == "" {
		return ""
	}
	return value
}

func identityField(endpoint string) string {
	if endpoint == FillsEndpoint {
		return "tradeId"
	}
	return "ordId"
}

func cursorField(endpoint string) string {
	if endpoint == FillsEndpoint {
		return "billId"
	}
	return "ordId"
}

func tokenOptional(tokens TokenStore, raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	return tokens.Token(raw)
}

func recoverRawCursor(endpoint, rawDirectory string, pageHashes []string) (string, error) {
	if len(pageHashes) == 0 {
		return "", nil
	}
	path := filepath.Join(rawDirectory, fmt.Sprintf("page-%06d.json", len(pageHashes)))
	raw, err := os.ReadFile(path)
	if err != nil || HashBytes(raw) != pageHashes[len(pageHashes)-1] {
		return "", errors.New("historydownload: cannot recover cursor from verified raw page")
	}
	var envelope struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil || len(envelope.Data) == 0 {
		return "", errors.New("historydownload: verified raw page lacks resumable data")
	}
	value, ok := envelope.Data[len(envelope.Data)-1][cursorField(endpoint)].(string)
	if !ok || !decimalID(value) {
		return "", errors.New("historydownload: verified raw page lacks valid resume cursor")
	}
	return value, nil
}

func validatePageOrder(endpoint, previousRaw string, records []map[string]any, nextRaw string) error {
	field := cursorField(endpoint)
	values := make([]*big.Int, 0, len(records))
	for _, record := range records {
		raw, ok := record[field].(string)
		if !ok || !decimalID(raw) {
			return fmt.Errorf("historydownload: invalid %s pagination identity", field)
		}
		value, _ := new(big.Int).SetString(raw, 10)
		values = append(values, value)
	}
	for index := 1; index < len(values); index++ {
		if values[index-1].Cmp(values[index]) <= 0 {
			return fmt.Errorf("historydownload: %s page is not strictly descending", field)
		}
	}
	if previousRaw != "" && len(values) > 0 {
		previous, ok := new(big.Int).SetString(previousRaw, 10)
		if !ok || previous.Cmp(values[0]) <= 0 {
			return fmt.Errorf("historydownload: %s cross-page boundary reversed", field)
		}
	}
	if len(records) > 0 {
		last, _ := records[len(records)-1][field].(string)
		if nextRaw != last {
			return fmt.Errorf("historydownload: %s next cursor mismatch", field)
		}
	}
	return nil
}

func decimalID(value string) bool {
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

func finishDownload(cfg DownloadConfig, cp Checkpoint, result DownloadResult) (DownloadResult, error) {
	if cp.PublishState == stateActive {
		cp.PublishState = statePendingManifest
		cp.Metadata["completed_at"] = time.Now().UTC().Format(time.RFC3339Nano)
		size, err := checkpointEncodedSize(cp)
		if err != nil {
			return result, err
		}
		if err := checkManagedCap(cfg, size); err != nil {
			return result, err
		}
		if err := SaveCheckpoint(cfg.CheckpointPath, cp); err != nil {
			return result, err
		}
	}
	started, _ := time.Parse(time.RFC3339Nano, cp.Metadata["started_at"])
	completed, _ := time.Parse(time.RFC3339Nano, cp.Metadata["completed_at"])
	warnings := append([]string(nil), cfg.Warnings...)
	clockFitness := cfg.ClockFitness
	if clockFitness == "" {
		clockFitness = "COMPLETE"
	}
	if cp.EventTimeRange.Min.IsZero() || cp.EventTimeRange.Max.IsZero() {
		warnings = append(warnings, "event clock range missing")
		clockFitness = "INCOMPLETE"
	}
	if cp.ReceiveRange.Min.IsZero() || cp.ReceiveRange.Max.IsZero() {
		warnings = append(warnings, "receive clock range missing")
		clockFitness = "INCOMPLETE"
	}
	manifest := Manifest{
		Endpoint: cfg.Endpoint, Private: cfg.Private, QueryHash: cfg.QueryHash,
		PageRawHashes: cp.PageRawHashes, CursorTokens: cp.CursorTokens, RecordCount: cp.RecordCount,
		DuplicateCount: cp.DuplicateCount, MissingIDCount: cp.MissingIDCount,
		BytesWritten: cp.BytesWritten, ProjectionFields: projectionFields(cfg.Endpoint),
		ProjectionSchemaHash: cfg.SchemaHash, PresenceCounts: cp.PresenceCounts,
		OmittedCounts:  cp.OmittedCounts,
		Clocks:         Clock{EventTime: cp.EventTimeRange.Max, ReceiveTime: cp.ReceiveRange.Max, ProcessTime: completed},
		EventTimeRange: cp.EventTimeRange, ReceiveTimeRange: cp.ReceiveRange, ClockFitness: clockFitness,
		DenominatorFitness: cfg.DenominatorFitness,
		Retries:            cp.Retries, Warnings: warnings, StartedAt: started, CompletedAt: completed, PITStatus: cfg.PITStatus,
	}
	sealed, err := SealManifest(manifest)
	if err != nil {
		return result, err
	}
	manifestBytes, err := json.MarshalIndent(sealed, "", "  ")
	if err != nil {
		return result, err
	}
	existing, existingErr := loadVerifiedManifest(cfg.ManifestPath)
	if existingErr == nil && existing.ManifestHash != nil && sealed.ManifestHash != nil && *existing.ManifestHash == *sealed.ManifestHash {
		manifest = existing
	} else {
		if err := checkManagedCap(cfg, int64(len(manifestBytes)+1)); err != nil {
			return result, err
		}
		publish := cfg.PublishManifest
		if publish == nil {
			publish = PublishManifest
		}
		if err := publish(cfg.ManifestPath, manifest); err != nil {
			return result, err
		}
		manifest, err = loadVerifiedManifest(cfg.ManifestPath)
		if err != nil {
			return result, err
		}
		if manifest.ManifestHash == nil || sealed.ManifestHash == nil || *manifest.ManifestHash != *sealed.ManifestHash {
			return result, errors.New("historydownload: published manifest hash mismatch")
		}
	}
	cp.PublishState = statePublished
	cp.ManifestHash = *manifest.ManifestHash
	size, err := checkpointEncodedSize(cp)
	if err != nil {
		return result, err
	}
	if err := checkManagedCap(cfg, size); err != nil {
		return result, err
	}
	if err := SaveCheckpoint(cfg.CheckpointPath, cp); err != nil {
		return result, err
	}
	result.CompletedAt, result.Manifest = completed, manifest
	return result, nil
}

func loadPublishedResult(cfg DownloadConfig, cp Checkpoint) (DownloadResult, error) {
	manifest, err := loadVerifiedManifest(cfg.ManifestPath)
	if err != nil {
		return DownloadResult{}, fmt.Errorf("historydownload: published checkpoint lacks valid manifest: %w", err)
	}
	if manifest.ManifestHash == nil || cp.ManifestHash == "" || *manifest.ManifestHash != cp.ManifestHash {
		return DownloadResult{}, errors.New("historydownload: checkpoint/manifest hash mismatch")
	}
	return DownloadResult{
		Pages: cp.Page, Records: cp.RecordCount, Bytes: cp.BytesWritten, Retries: cp.Retries,
		Manifest: manifest, CompletedAt: manifest.CompletedAt,
	}, nil
}

func loadVerifiedManifest(path string) (Manifest, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return Manifest{}, err
	}
	if err := VerifyManifest(manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func updateClockRanges(cp *Checkpoint, page Page) {
	cp.ReceiveRange = extendRange(cp.ReceiveRange, page.ReceiveTime, page.ReceiveTime)
	cp.EventTimeRange = extendRange(cp.EventTimeRange, page.EventTimeMin, page.EventTimeMax)
}

func extendRange(current ClockRange, minTime, maxTime time.Time) ClockRange {
	if !minTime.IsZero() && (current.Min.IsZero() || minTime.Before(current.Min)) {
		current.Min = minTime.UTC()
	}
	if !maxTime.IsZero() && (current.Max.IsZero() || maxTime.After(current.Max)) {
		current.Max = maxTime.UTC()
	}
	return current
}

func checkManagedCap(cfg DownloadConfig, adding int64) error {
	total, err := managedSize(cfg)
	if err != nil {
		return err
	}
	if total+adding > cfg.DiskCapBytes {
		return errors.New("historydownload: managed disk cap would be exceeded")
	}
	rootTotal, err := directorySize(cfg.RootDirectory)
	if err != nil {
		return err
	}
	if rootTotal+adding > cfg.RootCapBytes {
		return errors.New("historydownload: root disk cap would be exceeded")
	}
	return nil
}

func directorySize(root string) (int64, error) {
	var total int64
	err := filepath.Walk(root, func(_ string, entry os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() {
			total += entry.Size()
		}
		return nil
	})
	if os.IsNotExist(err) {
		return 0, nil
	}
	return total, err
}

func CheckRootCapacity(root string, capBytes, adding int64) error {
	if root == "" || capBytes <= 0 || adding < 0 {
		return errors.New("historydownload: invalid root capacity request")
	}
	total, err := directorySize(root)
	if err != nil {
		return err
	}
	if total+adding > capBytes {
		return errors.New("historydownload: root disk cap would be exceeded")
	}
	return nil
}

func managedSize(cfg DownloadConfig) (int64, error) {
	seen := map[string]bool{}
	var total int64
	for _, path := range []string{
		cfg.RawDirectory, cfg.ProjectionDirectory,
		cfg.CheckpointPath, cfg.CheckpointPath + ".tmp",
		cfg.ManifestPath, cfg.ManifestPath + ".tmp",
		cfg.TokenStore.SaltPath,
	} {
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		info, err := os.Stat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return 0, err
		}
		if !info.IsDir() {
			total += info.Size()
			continue
		}
		err = filepath.Walk(path, func(_ string, entry os.FileInfo, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if !entry.IsDir() {
				total += entry.Size()
			}
			return nil
		})
		if err != nil {
			return 0, err
		}
	}
	return total, nil
}

func cleanupAtomicTemps(cfg DownloadConfig) error {
	for _, path := range []string{cfg.CheckpointPath + ".tmp", cfg.ManifestPath + ".tmp"} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	for _, root := range []string{cfg.RawDirectory, cfg.ProjectionDirectory} {
		entries, err := os.ReadDir(root)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".tmp") {
				if err := os.Remove(filepath.Join(root, entry.Name())); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func existingBytesMatch(path string, expected []byte) (bool, error) {
	actual, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if HashBytes(actual) != HashBytes(expected) {
		return false, errors.New("historydownload: uncheckpointed staging artifact mismatch")
	}
	return true, nil
}

func checkpointEncodedSize(cp Checkpoint) (int64, error) {
	cp.SchemaVersion = "historydownload.checkpoint.v1"
	cp.Downloader = DownloaderVersion
	cp.ContractHash = ContractHash
	cp.UpdatedAt = time.Now().UTC()
	cp.Hash = nil
	value, err := hash(cp)
	if err != nil {
		return 0, err
	}
	cp.Hash = &value
	raw, err := json.MarshalIndent(cp, "", "  ")
	return int64(len(raw) + 1), err
}

func projectionFields(endpoint string) []string {
	schema := projectionSchemas[endpoint]
	fields := make([]string, 0, len(schema))
	for field := range schema {
		if tokenFields[field] {
			field += "Token"
		}
		fields = append(fields, field)
	}
	sort.Strings(fields)
	return fields
}

func contains(values []string, value string) bool {
	if value == "" {
		return false
	}
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}

func HashBytes(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func verifyRawPages(dir string, hashes []string) error {
	for index, expected := range hashes {
		path := filepath.Join(dir, "page-"+fmt.Sprintf("%06d", index+1)+".json")
		raw, err := os.ReadFile(path)
		if err != nil || HashBytes(raw) != expected {
			return errors.New("historydownload: raw page missing or mutated")
		}
	}
	return nil
}

func WriteBytesAtomic(path string, raw []byte, mode os.FileMode) error {
	return writeAtomicBytes(path, raw, mode)
}
