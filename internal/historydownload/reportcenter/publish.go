package reportcenter

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"syscall"

	"lingxi/internal/historydownload"
)

func publish(cfg Config, root, generations *directory, stageName string, stage *directory, mappingRaw []byte, receipt Receipt) (Receipt, bool, error) {
	if err := validateHeld(root, generations, stage); err != nil {
		return rejectPublish(receipt, false, "DIRECTORY_CHANGED", err)
	}
	projectionHash, _, err := hashFileAt(stage, "projection.ndjson", 0o600)
	if err != nil || projectionHash != receipt.ProjectionSHA256 {
		return rejectPublish(receipt, false, "PROJECTION_FAILED", errors.New("reportcenter: projection hash mismatch"))
	}
	cp := checkpoint{
		SchemaVersion: "okx-report-center.checkpoint.v2", RawSource: receipt.RawSource,
		RawFileSHA256: receipt.RawFileSHA256, MappingSHA256: receipt.MappingSHA256,
		ProjectionHash: receipt.ProjectionSHA256, Rows: receipt.Rows, Template: receipt.Template,
	}
	cpRaw, err := marshalJSON(cp)
	if err != nil {
		return rejectPublish(receipt, false, "CHECKPOINT_FAILED", err)
	}
	receipt.CheckpointSHA256 = hashBytes(cpRaw)
	receipt.ArtifactHashes = map[string]string{
		"mapping.json": hashBytes(mappingRaw), "projection.ndjson": receipt.ProjectionSHA256,
		"checkpoint.json": receipt.CheckpointSHA256,
	}
	receipt.Accepted, receipt.Failure, receipt.ReceiptSHA256 = true, "", ""
	receipt.ReceiptSHA256 = hashReceipt(receipt)
	receiptRaw, err := marshalJSON(receipt)
	if err != nil {
		return rejectPublish(receipt, false, "RECEIPT_FAILED", err)
	}
	if usage, err := rootUsage(root); err != nil || usage+int64(len(cpRaw)+len(receiptRaw)) > cfg.RootCap {
		if err == nil {
			err = errors.New("reportcenter: root cap exceeded")
		}
		return rejectPublish(receipt, false, "ROOT_CAP", err)
	}
	if err := atomicWriteAt(stage, "checkpoint.json", cpRaw, 0o600); err != nil {
		return rejectPublish(receipt, false, "CHECKPOINT_FAILED", err)
	}
	if err := atomicWriteAt(stage, "receipt.json", receiptRaw, 0o600); err != nil {
		return rejectPublish(receipt, false, "RECEIPT_FAILED", err)
	}
	if usage, err := rootUsage(root); err != nil || usage > cfg.RootCap {
		if err == nil {
			err = errors.New("reportcenter: root cap exceeded")
		}
		return rejectPublish(receipt, false, "ROOT_CAP", err)
	}
	if err := stage.file.Sync(); err != nil {
		return rejectPublish(receipt, false, "SYNC_FAILED", err)
	}
	if err := fault(cfg, "after_receipt"); err != nil {
		return rejectPublish(receipt, false, "CRASH_INJECTED", err)
	}
	generation := "generation-" + receipt.ReceiptSHA256[:24]
	if err := syscall.Renameat(int(generations.file.Fd()), stageName, int(generations.file.Fd()), generation); err != nil {
		return rejectPublish(receipt, false, "GENERATION_FAILED", err)
	}
	renamed := true
	if err := generations.file.Sync(); err != nil {
		return receipt, renamed, err
	}
	if err := fault(cfg, "after_generation"); err != nil {
		return receipt, renamed, err
	}
	current := Current{
		SchemaVersion: "okx-report-center.current.v2", Generation: generation,
		ReceiptSHA256: receipt.ReceiptSHA256, MappingSHA256: receipt.MappingSHA256, Template: receipt.Template,
	}
	current.Hash = hashCurrent(current)
	currentRaw, err := marshalJSON(current)
	if err != nil {
		return receipt, renamed, err
	}
	if usage, err := rootUsage(root); err != nil || usage+int64(len(currentRaw)) > cfg.RootCap {
		if err == nil {
			err = errors.New("reportcenter: root cap exceeded")
		}
		return receipt, renamed, err
	}
	if err := fault(cfg, "before_current"); err != nil {
		return receipt, renamed, err
	}
	if err := atomicWriteAt(root, "current.json", currentRaw, 0o600); err != nil {
		return receipt, renamed, err
	}
	return receipt, renamed, nil
}

func rejectPublish(receipt Receipt, renamed bool, code string, err error) (Receipt, bool, error) {
	rejected, wrapped := reject(receipt, code, err)
	return rejected, renamed, wrapped
}

func VerifyCurrent(rootPath string) (Current, Receipt, error) {
	lock, err := acquireForVerify(rootPath)
	if err != nil {
		return Current{}, Receipt{}, err
	}
	defer lock.close()
	root := lock.root
	currentRaw, err := readFileAt(root, "current.json", 0o600, 1<<20)
	if err != nil {
		return Current{}, Receipt{}, err
	}
	var current Current
	if json.Unmarshal(currentRaw, &current) != nil || current.SchemaVersion != "okx-report-center.current.v2" ||
		current.Hash == "" || current.Hash != hashCurrent(current) ||
		!validGenerationName(current.Generation) {
		return Current{}, Receipt{}, errors.New("reportcenter: invalid current")
	}
	canonicalCurrent, _ := marshalJSON(current)
	if !bytes.Equal(currentRaw, canonicalCurrent) {
		return Current{}, Receipt{}, errors.New("reportcenter: non-canonical current")
	}
	generations, err := openDirAt(root, "generations", false)
	if err != nil {
		return Current{}, Receipt{}, err
	}
	defer generations.close()
	generation, err := openDirAt(generations, current.Generation, false)
	if err != nil {
		return Current{}, Receipt{}, err
	}
	defer generation.close()
	entries, err := listDir(generation)
	if err != nil {
		return Current{}, Receipt{}, err
	}
	allowlist := map[string]bool{"mapping.json": true, "projection.ndjson": true, "checkpoint.json": true, "receipt.json": true}
	if len(entries) != len(allowlist) {
		return Current{}, Receipt{}, errors.New("reportcenter: generation artifact count mismatch")
	}
	for _, entry := range entries {
		if !allowlist[entry.Name()] || !entry.Mode().IsRegular() {
			return Current{}, Receipt{}, errors.New("reportcenter: unexpected generation artifact")
		}
	}
	receiptRaw, err := readFileAt(generation, "receipt.json", 0o600, 1<<20)
	if err != nil {
		return Current{}, Receipt{}, err
	}
	var receipt Receipt
	if json.Unmarshal(receiptRaw, &receipt) != nil || receipt.SchemaVersion != "okx-report-center.receipt.v2" || !receipt.Accepted ||
		receipt.ReceiptSHA256 != hashReceipt(receipt) || receipt.ReceiptSHA256 != current.ReceiptSHA256 ||
		receipt.MappingSHA256 != current.MappingSHA256 || receipt.Template != current.Template {
		return Current{}, Receipt{}, errors.New("reportcenter: receipt integrity mismatch")
	}
	canonicalReceipt, _ := marshalJSON(receipt)
	if !bytes.Equal(receiptRaw, canonicalReceipt) || len(receipt.ArtifactHashes) != 3 {
		return Current{}, Receipt{}, errors.New("reportcenter: receipt artifact manifest mismatch")
	}
	for name, expected := range receipt.ArtifactHashes {
		if name == "receipt.json" || !allowlist[name] {
			return Current{}, Receipt{}, errors.New("reportcenter: artifact not allowlisted")
		}
		actual, _, err := hashFileAt(generation, name, 0o600)
		if err != nil || actual != expected {
			return Current{}, Receipt{}, errors.New("reportcenter: generation artifact hash mismatch")
		}
	}
	if receipt.ArtifactHashes["mapping.json"] != receipt.MappingSHA256 ||
		receipt.ArtifactHashes["projection.ndjson"] != receipt.ProjectionSHA256 ||
		receipt.ArtifactHashes["checkpoint.json"] != receipt.CheckpointSHA256 {
		return Current{}, Receipt{}, errors.New("reportcenter: receipt artifact binding mismatch")
	}
	mappingRaw, err := readFileAt(generation, "mapping.json", 0o600, 1<<20)
	if err != nil {
		return Current{}, Receipt{}, err
	}
	mapping, err := parseMapping(mappingRaw)
	if err != nil || mapping.Template != receipt.Template {
		return Current{}, Receipt{}, errors.New("reportcenter: mapping snapshot invalid")
	}
	cpRaw, err := readFileAt(generation, "checkpoint.json", 0o600, 1<<20)
	if err != nil {
		return Current{}, Receipt{}, err
	}
	var cp checkpoint
	if json.Unmarshal(cpRaw, &cp) != nil || cp.SchemaVersion != "okx-report-center.checkpoint.v2" ||
		cp.RawSource != receipt.RawSource ||
		cp.RawFileSHA256 != receipt.RawFileSHA256 || cp.MappingSHA256 != receipt.MappingSHA256 ||
		cp.ProjectionHash != receipt.ProjectionSHA256 || cp.Rows != receipt.Rows || cp.Template != receipt.Template {
		return Current{}, Receipt{}, errors.New("reportcenter: checkpoint binding mismatch")
	}
	canonicalCheckpoint, _ := marshalJSON(cp)
	if !bytes.Equal(cpRaw, canonicalCheckpoint) {
		return Current{}, Receipt{}, errors.New("reportcenter: non-canonical checkpoint")
	}
	inbox, err := openDirAt(root, "inbox", false)
	if err != nil {
		return Current{}, Receipt{}, err
	}
	defer inbox.close()
	rawHash, _, err := hashFileAt(inbox, receipt.RawSource, 0o600)
	if err != nil || rawHash != receipt.RawFileSHA256 {
		return Current{}, Receipt{}, errors.New("reportcenter: external raw reference mismatch")
	}
	return current, receipt, nil
}

func validGenerationName(name string) bool {
	const prefix = "generation-"
	if !strings.HasPrefix(name, prefix) || len(name) != len(prefix)+24 || containsSlash(name) {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(name, prefix))
	return err == nil
}

type verifyLock struct {
	lock interface{ Close() error }
	root *directory
}

func (v *verifyLock) close() {
	v.root.close()
	_ = v.lock.Close()
}

func acquireForVerify(path string) (*verifyLock, error) {
	lock, err := historydownload.AcquireRootLock(path)
	if err != nil {
		return nil, err
	}
	rootFile, err := lock.OpenRootDir()
	if err != nil {
		_ = lock.Close()
		return nil, err
	}
	root, err := heldDirectory(rootFile)
	if err != nil {
		_ = rootFile.Close()
		_ = lock.Close()
		return nil, err
	}
	return &verifyLock{lock: lock, root: root}, nil
}
