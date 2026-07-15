package binancereadonly

import (
	"encoding/json"
	"errors"
	"os"
	"syscall"
	"time"

	"lingxi/internal/historydownload"
)

const (
	pilotDirectory = "order-async"
	PilotRootCap   = int64(2 << 30)
)

var (
	PilotStart = time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC)
	PilotEnd   = time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC)
)

type Intent struct {
	SchemaVersion string    `json:"schema_version"`
	WindowStart   time.Time `json:"window_start"`
	WindowEnd     time.Time `json:"window_end"`
	QueryHash     string    `json:"query_hash"`
	State         string    `json:"state"`
	Hash          *string   `json:"hash"`
}

type Checkpoint struct {
	SchemaVersion string  `json:"schema_version"`
	State         string  `json:"state"`
	QueryHash     string  `json:"query_hash"`
	DownloadToken string  `json:"download_id_token,omitempty"`
	RawJobHash    string  `json:"raw_job_response_hash,omitempty"`
	StatusPolls   int     `json:"status_polls"`
	Hash          *string `json:"hash"`
}

type PilotReceipt struct {
	SchemaVersion   string           `json:"schema_version"`
	WindowStart     time.Time        `json:"window_start"`
	WindowEnd       time.Time        `json:"window_end"`
	DownloadToken   string           `json:"download_id_token,omitempty"`
	QuotaSideEffect string           `json:"quota_side_effect"`
	StopStatus      string           `json:"stop_status"`
	Summary         *ZeroFillSummary `json:"summary,omitempty"`
	Hash            *string          `json:"hash"`
}

type Current struct {
	SchemaVersion string  `json:"schema_version"`
	Generation    string  `json:"generation"`
	ReceiptHash   string  `json:"receipt_hash"`
	StopStatus    string  `json:"stop_status"`
	Hash          *string `json:"hash"`
}

func queryHash() string {
	return hashBytes([]byte(OrderAsyncEndpoint + "\n" + PilotStart.Format(time.RFC3339Nano) + "\n" + PilotEnd.Format(time.RFC3339Nano)))
}

func seal(value any, clear func()) ([]byte, string, error) {
	clear()
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, "", err
	}
	hash := hashBytes(raw)
	return raw, hash, nil
}

func writeIntent(base *os.File) (Intent, error) {
	intent := Intent{
		SchemaVersion: "binance-order-async.intent.v1", WindowStart: PilotStart, WindowEnd: PilotEnd,
		QueryHash: queryHash(), State: "INTENT_WRITTEN",
	}
	_, hash, err := seal(intent, func() { intent.Hash = nil })
	if err != nil {
		return Intent{}, err
	}
	intent.Hash = &hash
	raw, _ := json.Marshal(intent)
	fd, err := syscall.Openat(int(base.Fd()), "job_intent.json", syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return Intent{}, err
	}
	file := os.NewFile(uintptr(fd), "job_intent.json")
	if _, err := file.Write(append(raw, '\n')); err != nil {
		_ = file.Close()
		return Intent{}, err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return Intent{}, err
	}
	if err := file.Close(); err != nil {
		return Intent{}, err
	}
	if err := base.Sync(); err != nil {
		return Intent{}, err
	}
	return intent, nil
}

func writeCheckpoint(base *os.File, checkpoint Checkpoint) error {
	checkpoint.SchemaVersion = "binance-order-async.checkpoint.v1"
	checkpoint.QueryHash = queryHash()
	_, hash, err := seal(checkpoint, func() { checkpoint.Hash = nil })
	if err != nil {
		return err
	}
	checkpoint.Hash = &hash
	raw, err := json.Marshal(checkpoint)
	if err != nil {
		return err
	}
	return atomicAt(base, "checkpoint.json", append(raw, '\n'))
}

func loadCheckpoint(base *os.File) (Checkpoint, bool, error) {
	raw, err := readAt(base, "checkpoint.json", 64<<10)
	if errors.Is(err, syscall.ENOENT) {
		return Checkpoint{}, false, nil
	}
	if err != nil {
		return Checkpoint{}, false, err
	}
	var checkpoint Checkpoint
	if json.Unmarshal(raw, &checkpoint) != nil {
		return Checkpoint{}, false, errors.New("binancereadonly: invalid checkpoint")
	}
	actual := checkpoint.Hash
	checkpoint.Hash = nil
	expectedRaw, _ := json.Marshal(checkpoint)
	if actual == nil || *actual != hashBytes(expectedRaw) || checkpoint.QueryHash != queryHash() {
		return Checkpoint{}, false, errors.New("binancereadonly: checkpoint integrity mismatch")
	}
	checkpoint.Hash = actual
	return checkpoint, true, nil
}

func publishTerminal(base *os.File, receipt PilotReceipt) (Current, error) {
	if exists, err := existsAt(base, "current.json"); err != nil {
		return Current{}, err
	} else if exists {
		return Current{}, errors.New("binancereadonly: pilot already terminal")
	}
	if !validStop(receipt.StopStatus) || !receipt.WindowStart.Equal(PilotStart) || !receipt.WindowEnd.Equal(PilotEnd) {
		return Current{}, errors.New("binancereadonly: invalid terminal receipt")
	}
	receipt.SchemaVersion = "binance-order-async.receipt.v2"
	_, receiptHash, err := seal(receipt, func() { receipt.Hash = nil })
	if err != nil {
		return Current{}, err
	}
	receipt.Hash = &receiptHash
	receiptRaw, _ := json.Marshal(receipt)
	generations, err := mkdirAt(base, "generations")
	if err != nil {
		return Current{}, err
	}
	defer generations.Close()
	generationName := "generation-" + receiptHash[:24]
	generation, err := mkdirAt(generations, generationName)
	if err != nil {
		return Current{}, err
	}
	if err := atomicAt(generation, "receipt.json", append(receiptRaw, '\n')); err != nil {
		_ = generation.Close()
		return Current{}, err
	}
	if err := generation.Sync(); err != nil {
		_ = generation.Close()
		return Current{}, err
	}
	_ = generation.Close()
	current := Current{
		SchemaVersion: "binance-order-async.current.v2", Generation: generationName,
		ReceiptHash: receiptHash, StopStatus: receipt.StopStatus,
	}
	_, currentHash, err := seal(current, func() { current.Hash = nil })
	if err != nil {
		return Current{}, err
	}
	current.Hash = &currentHash
	currentRaw, _ := json.Marshal(current)
	if err := atomicAt(base, "current.json", append(currentRaw, '\n')); err != nil {
		return Current{}, err
	}
	return current, nil
}

func loadCurrent(base *os.File) (Current, bool, error) {
	raw, err := readAt(base, "current.json", 64<<10)
	if errors.Is(err, syscall.ENOENT) {
		return Current{}, false, nil
	}
	if err != nil {
		return Current{}, false, err
	}
	var current Current
	if json.Unmarshal(raw, &current) != nil {
		return Current{}, false, errors.New("binancereadonly: invalid current")
	}
	actual := current.Hash
	current.Hash = nil
	expectedRaw, _ := json.Marshal(current)
	if actual == nil || *actual != hashBytes(expectedRaw) || current.SchemaVersion != "binance-order-async.current.v2" {
		return Current{}, false, errors.New("binancereadonly: current integrity mismatch")
	}
	current.Hash = actual
	generations, err := mkdirAt(base, "generations")
	if err != nil {
		return Current{}, false, err
	}
	defer generations.Close()
	generation, err := mkdirAt(generations, current.Generation)
	if err != nil {
		return Current{}, false, err
	}
	defer generation.Close()
	receiptRaw, err := readAt(generation, "receipt.json", 1<<20)
	if err != nil {
		return Current{}, false, err
	}
	var receipt PilotReceipt
	if json.Unmarshal(receiptRaw, &receipt) != nil || receipt.Hash == nil || *receipt.Hash != current.ReceiptHash {
		return Current{}, false, errors.New("binancereadonly: terminal generation mismatch")
	}
	actualReceipt := receipt.Hash
	receipt.Hash = nil
	plain, _ := json.Marshal(receipt)
	if hashBytes(plain) != *actualReceipt || receipt.StopStatus != current.StopStatus {
		return Current{}, false, errors.New("binancereadonly: terminal receipt integrity mismatch")
	}
	return current, true, nil
}

func validStop(value string) bool {
	switch value {
	case "STOP_UNVERIFIED_DOWNLOAD_HOST", "STOP_AMBIGUOUS_JOB_INTENT", "STOP_ENDPOINT_REJECTED",
		"STOP_STATUS_FAILED", "STOP_STATUS_TIMEOUT", "STOP_NO_ACTIVITY", "STOP_NO_ZERO_FILL",
		"STOP_COMPLETED_ZERO_FILL_PRESENT":
		return true
	}
	return false
}

// PublishStop remains a narrow test helper and still derives all writes from
// RootLock.OpenRootDir; it never re-opens the root pathname.
func PublishStop(root string, _ int64, lock *historydownload.RootLock, receipt PilotReceipt) (Current, error) {
	if lock == nil || lock.Validate(root) != nil {
		return Current{}, errors.New("binancereadonly: held root lock required")
	}
	rootFile, err := lock.OpenRootDir()
	if err != nil {
		return Current{}, err
	}
	defer rootFile.Close()
	base, err := mkdirAt(rootFile, pilotDirectory)
	if err != nil {
		return Current{}, err
	}
	defer base.Close()
	if err := requireCapacity(rootFile, 1<<20); err != nil {
		return Current{}, err
	}
	return publishTerminal(base, receipt)
}

func VerifyCurrent(root string) (Current, error) {
	authority, err := acquireAuthority(root)
	if err != nil {
		return Current{}, err
	}
	defer authority.close()
	base, err := mkdirAt(authority.root, pilotDirectory)
	if err != nil {
		return Current{}, err
	}
	defer base.Close()
	current, found, err := loadCurrent(base)
	if err != nil {
		return Current{}, err
	}
	if !found {
		return Current{}, errors.New("binancereadonly: current absent")
	}
	return current, nil
}
