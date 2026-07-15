package historydownload

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

type InstrumentCurrent struct {
	SchemaVersion string  `json:"schema_version"`
	Generation    string  `json:"generation"`
	ManifestHash  string  `json:"manifest_hash"`
	Hash          *string `json:"hash"`
}

type InstrumentFault func(step string) error

func PublishInstrumentGeneration(
	root string,
	rootCap int64,
	lock *RootLock,
	raw []byte,
	receipt InstrumentSnapshotReceipt,
	manifest Manifest,
	fault InstrumentFault,
) (InstrumentCurrent, error) {
	if err := lock.Validate(root); err != nil {
		return InstrumentCurrent{}, err
	}
	instrumentsRoot := filepath.Join(root, "instruments")
	if err := os.MkdirAll(instrumentsRoot, 0o700); err != nil {
		return InstrumentCurrent{}, err
	}
	receiptBytes, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return InstrumentCurrent{}, err
	}
	receiptBytes = append(receiptBytes, '\n')
	rawHash := HashBytes(raw)
	receiptHash := HashBytes(receiptBytes)
	manifest.ArtifactHashes = map[string]string{
		"snapshot-raw.json":     rawHash,
		"snapshot-receipt.json": receiptHash,
	}
	sealed, err := SealManifest(manifest)
	if err != nil {
		return InstrumentCurrent{}, err
	}
	if sealed.ManifestHash == nil {
		return InstrumentCurrent{}, errors.New("historydownload: sealed instrument manifest lacks hash")
	}
	generation := "generation-" + (*sealed.ManifestHash)[:24]
	stage, err := os.MkdirTemp(instrumentsRoot, ".staging-")
	if err != nil {
		return InstrumentCurrent{}, err
	}
	published := false
	defer func() {
		if !published {
			_ = os.RemoveAll(stage)
		}
	}()
	if err := writeGenerationFile(root, rootCap, filepath.Join(stage, "snapshot-raw.json"), raw, 0o600); err != nil {
		return InstrumentCurrent{}, err
	}
	if err := injectInstrumentFault(fault, "after_raw"); err != nil {
		return InstrumentCurrent{}, err
	}
	if err := writeGenerationFile(root, rootCap, filepath.Join(stage, "snapshot-receipt.json"), receiptBytes, 0o600); err != nil {
		return InstrumentCurrent{}, err
	}
	if err := injectInstrumentFault(fault, "after_receipt"); err != nil {
		return InstrumentCurrent{}, err
	}
	manifestBytes, err := json.MarshalIndent(sealed, "", "  ")
	if err != nil {
		return InstrumentCurrent{}, err
	}
	manifestBytes = append(manifestBytes, '\n')
	if err := writeGenerationFile(root, rootCap, filepath.Join(stage, "manifest.json"), manifestBytes, 0o600); err != nil {
		return InstrumentCurrent{}, err
	}
	if err := injectInstrumentFault(fault, "after_manifest"); err != nil {
		return InstrumentCurrent{}, err
	}
	stageDir, err := os.Open(stage)
	if err != nil {
		return InstrumentCurrent{}, err
	}
	if err := stageDir.Sync(); err != nil {
		_ = stageDir.Close()
		return InstrumentCurrent{}, err
	}
	if err := stageDir.Close(); err != nil {
		return InstrumentCurrent{}, err
	}
	generationPath := filepath.Join(instrumentsRoot, generation)
	if err := os.Rename(stage, generationPath); err != nil {
		if _, statErr := os.Stat(generationPath); statErr != nil {
			return InstrumentCurrent{}, err
		}
		if _, verifyErr := verifyInstrumentGeneration(generationPath, *sealed.ManifestHash); verifyErr != nil {
			return InstrumentCurrent{}, verifyErr
		}
		_ = os.RemoveAll(stage)
	}
	published = true
	instrumentsDir, err := os.Open(instrumentsRoot)
	if err != nil {
		return InstrumentCurrent{}, err
	}
	if err := instrumentsDir.Sync(); err != nil {
		_ = instrumentsDir.Close()
		return InstrumentCurrent{}, err
	}
	if err := instrumentsDir.Close(); err != nil {
		return InstrumentCurrent{}, err
	}
	if err := injectInstrumentFault(fault, "after_generation"); err != nil {
		return InstrumentCurrent{}, err
	}
	current := InstrumentCurrent{
		SchemaVersion: "historydownload.instrument-current.v1",
		Generation:    generation, ManifestHash: *sealed.ManifestHash,
	}
	currentHash, err := sealInstrumentCurrent(current)
	if err != nil {
		return InstrumentCurrent{}, err
	}
	current = currentHash
	currentBytes, err := json.MarshalIndent(current, "", "  ")
	if err != nil {
		return InstrumentCurrent{}, err
	}
	if err := CheckRootCapacity(root, rootCap, int64(len(currentBytes)+1)); err != nil {
		return InstrumentCurrent{}, err
	}
	if err := injectInstrumentFault(fault, "before_current"); err != nil {
		return InstrumentCurrent{}, err
	}
	if err := WriteJSONAtomic(filepath.Join(instrumentsRoot, "current.json"), current, 0o600); err != nil {
		verified, _, verifyErr := VerifyInstrumentCurrent(root)
		if verifyErr == nil && verified.ManifestHash == current.ManifestHash && verified.Generation == current.Generation {
			return verified, nil
		}
		return InstrumentCurrent{}, err
	}
	return current, nil
}

func VerifyInstrumentCurrent(root string) (InstrumentCurrent, Manifest, error) {
	raw, err := readOwnedRegular(filepath.Join(root, "instruments", "current.json"), 0o600)
	if err != nil {
		return InstrumentCurrent{}, Manifest{}, err
	}
	var current InstrumentCurrent
	if err := json.Unmarshal(raw, &current); err != nil {
		return InstrumentCurrent{}, Manifest{}, err
	}
	actual := current.Hash
	current.Hash = nil
	expected, err := hash(current)
	if err != nil || actual == nil || *actual != expected ||
		current.SchemaVersion != "historydownload.instrument-current.v1" ||
		!strings.HasPrefix(current.Generation, "generation-") ||
		strings.ContainsAny(current.Generation, `/\`) {
		return InstrumentCurrent{}, Manifest{}, errors.New("historydownload: invalid instrument current pointer")
	}
	current.Hash = actual
	manifest, err := verifyInstrumentGeneration(filepath.Join(root, "instruments", current.Generation), current.ManifestHash)
	return current, manifest, err
}

func verifyInstrumentGeneration(path, manifestHash string) (Manifest, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return Manifest{}, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 ||
		stat.Uid != uint32(os.Geteuid()) {
		return Manifest{}, errors.New("historydownload: unsafe instrument generation directory")
	}
	raw, err := readOwnedRegular(filepath.Join(path, "manifest.json"), 0o600)
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return Manifest{}, err
	}
	if err := VerifyManifest(manifest); err != nil || manifest.ManifestHash == nil || *manifest.ManifestHash != manifestHash {
		return Manifest{}, errors.New("historydownload: instrument generation manifest mismatch")
	}
	for name, expected := range manifest.ArtifactHashes {
		if name != "snapshot-raw.json" && name != "snapshot-receipt.json" {
			return Manifest{}, errors.New("historydownload: unexpected instrument artifact")
		}
		artifact, err := readOwnedRegular(filepath.Join(path, name), 0o600)
		if err != nil || HashBytes(artifact) != expected {
			return Manifest{}, errors.New("historydownload: instrument artifact hash mismatch")
		}
	}
	if len(manifest.ArtifactHashes) != 2 {
		return Manifest{}, errors.New("historydownload: incomplete instrument artifact binding")
	}
	return manifest, nil
}

func sealInstrumentCurrent(current InstrumentCurrent) (InstrumentCurrent, error) {
	current.Hash = nil
	value, err := hash(current)
	if err != nil {
		return InstrumentCurrent{}, err
	}
	current.Hash = &value
	return current, nil
}

func writeGenerationFile(root string, capBytes int64, path string, raw []byte, mode os.FileMode) error {
	if err := CheckRootCapacity(root, capBytes, int64(len(raw))); err != nil {
		return err
	}
	return writeAtomicBytes(path, raw, mode)
}

func injectInstrumentFault(fault InstrumentFault, step string) error {
	if fault == nil {
		return nil
	}
	return fault(step)
}

func readOwnedRegular(path string, mode os.FileMode) ([]byte, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != mode || stat.Uid != uint32(os.Geteuid()) {
		return nil, errors.New("historydownload: unsafe instrument authority file")
	}
	return io.ReadAll(io.LimitReader(file, 64<<20))
}
