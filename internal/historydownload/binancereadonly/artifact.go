package binancereadonly

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"syscall"
)

type fdCapacityWriter struct {
	root   *os.File
	writer io.Writer
}

func (w fdCapacityWriter) Write(value []byte) (int, error) {
	if err := requireCapacity(w.root, int64(len(value))); err != nil {
		return 0, err
	}
	return w.writer.Write(value)
}

// processArtifactFD performs direct no-redirect transport and extraction using
// only descriptors rooted in RootLock.OpenRootDir.
func processArtifactFD(ctx context.Context, client *http.Client, rawURL string, root, base *os.File) (ZeroFillSummary, error) {
	u, err := ValidateDownloadURL(rawURL)
	if err != nil {
		return ZeroFillSummary{}, err
	}
	if client == nil {
		client = http.DefaultClient
	}
	safeClient := *client
	safeClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return ZeroFillSummary{}, errors.New("binancereadonly: failed to construct download GET")
	}
	response, err := safeClient.Do(request)
	if err != nil {
		return ZeroFillSummary{}, errors.New("binancereadonly: archive transport failed")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return ZeroFillSummary{}, errors.New("binancereadonly: archive HTTP rejected")
	}
	artifacts, err := mkdirAt(base, "artifacts")
	if err != nil {
		return ZeroFillSummary{}, err
	}
	defer artifacts.Close()
	zipFile, err := createExclusive(artifacts, "order-history.zip")
	if err != nil {
		return ZeroFillSummary{}, err
	}
	zipHash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(fdCapacityWriter{root: root, writer: zipFile}, zipHash), io.LimitReader(response.Body, MaxArchiveBytes+1))
	if copyErr != nil || written > MaxArchiveBytes || zipFile.Sync() != nil {
		_ = zipFile.Close()
		return ZeroFillSummary{}, errors.New("binancereadonly: archive download cap or write failure")
	}
	info, err := zipFile.Stat()
	if err != nil {
		_ = zipFile.Close()
		return ZeroFillSummary{}, err
	}
	reader, err := zip.NewReader(zipFile, info.Size())
	if err != nil {
		_ = zipFile.Close()
		return ZeroFillSummary{}, err
	}
	if len(reader.File) != 1 || reader.File[0].FileInfo().IsDir() ||
		!strings.HasSuffix(strings.ToLower(reader.File[0].Name), ".csv") ||
		!safeName(reader.File[0].Name) || reader.File[0].UncompressedSize64 > uint64(MaxArchiveBytes) {
		_ = zipFile.Close()
		return ZeroFillSummary{}, errors.New("binancereadonly: archive content rejected")
	}
	input, err := reader.File[0].Open()
	if err != nil {
		_ = zipFile.Close()
		return ZeroFillSummary{}, err
	}
	csvFile, err := createExclusive(artifacts, "order-history.csv")
	if err != nil {
		_ = input.Close()
		_ = zipFile.Close()
		return ZeroFillSummary{}, err
	}
	csvHash := sha256.New()
	csvBytes, copyErr := io.Copy(io.MultiWriter(fdCapacityWriter{root: root, writer: csvFile}, csvHash), io.LimitReader(input, MaxArchiveBytes+1))
	closeInputErr := input.Close()
	if copyErr != nil || closeInputErr != nil || csvBytes > MaxArchiveBytes || csvFile.Sync() != nil {
		_ = csvFile.Close()
		_ = zipFile.Close()
		return ZeroFillSummary{}, errors.New("binancereadonly: CSV extraction failed")
	}
	if _, err := csvFile.Seek(0, io.SeekStart); err != nil {
		_ = csvFile.Close()
		_ = zipFile.Close()
		return ZeroFillSummary{}, err
	}
	summary, err := summarizeCSV(csvFile)
	_ = csvFile.Close()
	_ = zipFile.Close()
	if err != nil {
		return ZeroFillSummary{}, err
	}
	summary.ZIPHash = hex.EncodeToString(zipHash.Sum(nil))
	summary.CSVHash = hex.EncodeToString(csvHash.Sum(nil))
	if err := artifacts.Sync(); err != nil {
		return ZeroFillSummary{}, err
	}
	return summary, nil
}

func createExclusive(directory *os.File, name string) (*os.File, error) {
	if !safeName(name) {
		return nil, errors.New("binancereadonly: unsafe artifact name")
	}
	fd, err := syscall.Openat(int(directory.Fd()), name, syscall.O_RDWR|syscall.O_CREAT|syscall.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		_ = file.Close()
		return nil, errors.New("binancereadonly: unsafe artifact file")
	}
	return file, nil
}
