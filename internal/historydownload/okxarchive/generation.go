package okxarchive

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"lingxi/internal/historydownload"
)

type ArchiveCurrent struct {
	SchemaVersion string  `json:"schema_version"`
	Generation    string  `json:"generation"`
	ManifestHash  string  `json:"manifest_hash"`
	Module        string  `json:"module"`
	InstType      string  `json:"inst_type"`
	Instrument    string  `json:"instrument"`
	UTCDate       string  `json:"utc_date"`
	Hash          *string `json:"hash"`
}

type ArchiveFault func(step string) error

func PublishArchiveGeneration(
	root string,
	rootCap int64,
	lock *historydownload.RootLock,
	workspace string,
	receipt SmokeReceipt,
	manifest historydownload.Manifest,
	fault ArchiveFault,
) (ArchiveCurrent, error) {
	if err := lock.Validate(root); err != nil {
		return ArchiveCurrent{}, err
	}
	archiveRoot := filepath.Join(root, "archive-smoke")
	canonicalWorkspace, err := filepath.EvalSymlinks(workspace)
	if err != nil || filepath.Dir(canonicalWorkspace) != archiveRoot ||
		!strings.HasPrefix(filepath.Base(canonicalWorkspace), ".staging-") {
		return ArchiveCurrent{}, errors.New("okxarchive: invalid generation workspace")
	}
	for name, expected := range map[string]string{
		"archive.zip": receipt.ZIPHash,
		"data.csv":    receipt.CSVHash,
	} {
		actual, err := hashOwnedFile(filepath.Join(workspace, name), 0o600)
		if err != nil || actual != expected {
			return ArchiveCurrent{}, errors.New("okxarchive: workspace artifact hash mismatch")
		}
	}
	discoveryHash, err := hashOwnedFile(filepath.Join(workspace, "discovery.json"), 0o600)
	if err != nil {
		return ArchiveCurrent{}, err
	}
	discoveryRaw, err := readOwnedArchiveFile(filepath.Join(workspace, "discovery.json"), 0o600)
	if err != nil {
		return ArchiveCurrent{}, err
	}
	date, err := time.Parse("2006-01-02", receipt.UTCDate)
	if err != nil {
		return ArchiveCurrent{}, errors.New("okxarchive: receipt date invalid")
	}
	request := DiscoveryRequest{
		Module: receipt.Module, InstType: receipt.InstType, Instrument: receipt.Instrument, UTCDate: receipt.UTCDate,
		BeginMS: date.UnixMilli(), EndMS: date.Add(24*time.Hour).UnixMilli() - 1, ArchiveCap: receipt.ArchiveCapBytes,
	}
	selected, err := SelectArchive(discoveryRaw, request)
	if err != nil || VerifySmokeReceipt(receipt, request, selected) != nil {
		return ArchiveCurrent{}, errors.New("okxarchive: receipt is not bound to discovery")
	}
	receiptBytes, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return ArchiveCurrent{}, err
	}
	receiptBytes = append(receiptBytes, '\n')
	if err := historydownload.CheckRootCapacity(root, rootCap, int64(len(receiptBytes))); err != nil {
		return ArchiveCurrent{}, err
	}
	if err := historydownload.WriteBytesAtomic(filepath.Join(workspace, "smoke-receipt.json"), receiptBytes, 0o600); err != nil {
		return ArchiveCurrent{}, err
	}
	if err := archiveFault(fault, "after_receipt"); err != nil {
		return ArchiveCurrent{}, err
	}
	manifest.ArtifactHashes = map[string]string{
		"archive.zip":        receipt.ZIPHash,
		"data.csv":           receipt.CSVHash,
		"discovery.json":     discoveryHash,
		"smoke-receipt.json": historydownload.HashBytes(receiptBytes),
	}
	sealed, err := historydownload.SealManifest(manifest)
	if err != nil || sealed.ManifestHash == nil {
		return ArchiveCurrent{}, errors.New("okxarchive: manifest sealing failed")
	}
	manifestBytes, err := json.MarshalIndent(sealed, "", "  ")
	if err != nil {
		return ArchiveCurrent{}, err
	}
	manifestBytes = append(manifestBytes, '\n')
	if err := historydownload.CheckRootCapacity(root, rootCap, int64(len(manifestBytes))); err != nil {
		return ArchiveCurrent{}, err
	}
	if err := historydownload.WriteBytesAtomic(filepath.Join(workspace, "manifest.json"), manifestBytes, 0o600); err != nil {
		return ArchiveCurrent{}, err
	}
	if err := archiveFault(fault, "after_manifest"); err != nil {
		return ArchiveCurrent{}, err
	}
	if err := syncDirectory(workspace); err != nil {
		return ArchiveCurrent{}, err
	}
	generation := "generation-" + (*sealed.ManifestHash)[:24]
	generationPath := filepath.Join(archiveRoot, generation)
	if err := os.Rename(workspace, generationPath); err != nil {
		if _, statErr := os.Stat(generationPath); statErr != nil {
			return ArchiveCurrent{}, err
		}
		if _, verifyErr := verifyArchiveGeneration(generationPath, *sealed.ManifestHash); verifyErr != nil {
			return ArchiveCurrent{}, verifyErr
		}
		_ = os.RemoveAll(workspace)
	}
	if err := syncDirectory(archiveRoot); err != nil {
		return ArchiveCurrent{}, err
	}
	if err := archiveFault(fault, "after_generation"); err != nil {
		return ArchiveCurrent{}, err
	}
	current := ArchiveCurrent{
		SchemaVersion: "okxarchive.current.v1", Generation: generation, ManifestHash: *sealed.ManifestHash,
		Module: receipt.Module, InstType: receipt.InstType, Instrument: receipt.Instrument, UTCDate: receipt.UTCDate,
	}
	current.Hash = nil
	currentHash, err := historydownload.Fingerprint(current)
	if err != nil {
		return ArchiveCurrent{}, err
	}
	current.Hash = &currentHash
	currentBytes, err := json.MarshalIndent(current, "", "  ")
	if err != nil {
		return ArchiveCurrent{}, err
	}
	if err := historydownload.CheckRootCapacity(root, rootCap, int64(len(currentBytes)+1)); err != nil {
		return ArchiveCurrent{}, err
	}
	if err := archiveFault(fault, "before_current"); err != nil {
		return ArchiveCurrent{}, err
	}
	if err := historydownload.WriteJSONAtomic(filepath.Join(archiveRoot, "current.json"), current, 0o600); err != nil {
		verified, _, verifyErr := VerifyArchiveCurrent(root)
		if verifyErr == nil && verified.ManifestHash == current.ManifestHash {
			return verified, nil
		}
		return ArchiveCurrent{}, err
	}
	return current, nil
}

func VerifyArchiveCurrent(root string) (ArchiveCurrent, historydownload.Manifest, error) {
	raw, err := readOwnedArchiveFile(filepath.Join(root, "archive-smoke", "current.json"), 0o600)
	if err != nil {
		return ArchiveCurrent{}, historydownload.Manifest{}, err
	}
	var current ArchiveCurrent
	if err := json.Unmarshal(raw, &current); err != nil {
		return ArchiveCurrent{}, historydownload.Manifest{}, err
	}
	actual := current.Hash
	current.Hash = nil
	expected, err := historydownload.Fingerprint(current)
	if err != nil || actual == nil || *actual != expected || current.SchemaVersion != "okxarchive.current.v1" ||
		!strings.HasPrefix(current.Generation, "generation-") || strings.ContainsAny(current.Generation, `/\`) {
		return ArchiveCurrent{}, historydownload.Manifest{}, errors.New("okxarchive: invalid current pointer")
	}
	current.Hash = actual
	manifest, err := verifyArchiveGeneration(
		filepath.Join(root, "archive-smoke", current.Generation), current.ManifestHash,
	)
	if err != nil {
		return ArchiveCurrent{}, historydownload.Manifest{}, err
	}
	receiptRaw, err := readOwnedArchiveFile(
		filepath.Join(root, "archive-smoke", current.Generation, "smoke-receipt.json"), 0o600,
	)
	if err != nil {
		return ArchiveCurrent{}, historydownload.Manifest{}, err
	}
	var receipt SmokeReceipt
	if err := json.Unmarshal(receiptRaw, &receipt); err != nil ||
		receipt.Module != current.Module || receipt.InstType != current.InstType ||
		receipt.Instrument != current.Instrument || receipt.UTCDate != current.UTCDate {
		return ArchiveCurrent{}, historydownload.Manifest{}, errors.New("okxarchive: current provenance mismatch")
	}
	date, err := time.Parse("2006-01-02", receipt.UTCDate)
	if err != nil {
		return ArchiveCurrent{}, historydownload.Manifest{}, errors.New("okxarchive: current date invalid")
	}
	discoveryRaw, err := readOwnedArchiveFile(
		filepath.Join(root, "archive-smoke", current.Generation, "discovery.json"), 0o600,
	)
	if err != nil {
		return ArchiveCurrent{}, historydownload.Manifest{}, err
	}
	request := DiscoveryRequest{
		Module: receipt.Module, InstType: receipt.InstType, Instrument: receipt.Instrument, UTCDate: receipt.UTCDate,
		BeginMS: date.UnixMilli(), EndMS: date.Add(24*time.Hour).UnixMilli() - 1, ArchiveCap: receipt.ArchiveCapBytes,
	}
	selected, err := SelectArchive(discoveryRaw, request)
	if err != nil || VerifySmokeReceipt(receipt, request, selected) != nil {
		return ArchiveCurrent{}, historydownload.Manifest{}, errors.New("okxarchive: current discovery provenance invalid")
	}
	return current, manifest, nil
}

func verifyArchiveGeneration(path, manifestHash string) (historydownload.Manifest, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return historydownload.Manifest{}, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 ||
		stat.Uid != uint32(os.Geteuid()) {
		return historydownload.Manifest{}, errors.New("okxarchive: unsafe generation directory")
	}
	raw, err := readOwnedArchiveFile(filepath.Join(path, "manifest.json"), 0o600)
	if err != nil {
		return historydownload.Manifest{}, err
	}
	var manifest historydownload.Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return historydownload.Manifest{}, err
	}
	if err := historydownload.VerifyManifest(manifest); err != nil || manifest.ManifestHash == nil ||
		*manifest.ManifestHash != manifestHash || len(manifest.ArtifactHashes) != 4 {
		return historydownload.Manifest{}, errors.New("okxarchive: generation manifest mismatch")
	}
	for name, expected := range manifest.ArtifactHashes {
		if name != "archive.zip" && name != "data.csv" && name != "discovery.json" && name != "smoke-receipt.json" {
			return historydownload.Manifest{}, errors.New("okxarchive: unexpected generation artifact")
		}
		actual, err := hashOwnedFile(filepath.Join(path, name), 0o600)
		if err != nil || actual != expected {
			return historydownload.Manifest{}, errors.New("okxarchive: generation artifact hash mismatch")
		}
	}
	return manifest, nil
}

func hashOwnedFile(path string, mode os.FileMode) (string, error) {
	file, err := openOwnedArchiveFile(path, mode)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func readOwnedArchiveFile(path string, mode os.FileMode) ([]byte, error) {
	file, err := openOwnedArchiveFile(path, mode)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(file)
}

func openOwnedArchiveFile(path string, mode os.FileMode) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != mode || stat.Uid != uint32(os.Geteuid()) {
		_ = file.Close()
		return nil, errors.New("okxarchive: unsafe generation artifact")
	}
	return file, nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func archiveFault(fault ArchiveFault, step string) error {
	if fault == nil {
		return nil
	}
	return fault(step)
}
