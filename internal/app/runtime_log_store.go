package app

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// 本文件从 runtime_controller.go 拆出：仅承载运行事件日志的落盘存储、按天轮转、过期清理
// 与内存裁剪（同包搬移，行为不变），让 runtime_controller.go 收敛到「控制器编排」职责并满足
// 单文件 ≤ 800 行门禁。

type RuntimeLogStore struct {
	path           string
	currentDay     string
	mu             sync.Mutex
	lastCleanupAt  time.Time
	cleanupRunning bool
	records        []RuntimeLogRecord
}

func NewRuntimeLogStore(path string) (*RuntimeLogStore, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("创建运行日志目录失败: %w", err)
	}
	now := time.Now().UTC()
	if err := ensureRuntimeLogBaseFile(path); err != nil {
		return nil, err
	}
	store := &RuntimeLogStore{path: path, currentDay: runtimeLogDay(now)}
	if existingDay, ok := detectRuntimeLogBaseFileDay(path); ok && existingDay != "" && existingDay != store.currentDay {
		if err := store.archiveBaseFileToDayLocked(existingDay); err != nil {
			return nil, err
		}
		if err := ensureRuntimeLogBaseFile(path); err != nil {
			return nil, err
		}
	}
	if err := store.reloadRecordsLocked(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *RuntimeLogStore) Path() string {
	return s.path
}

func (s *RuntimeLogStore) Append(record RuntimeLogRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	if err := s.rotateIfNeededLocked(now); err != nil {
		return err
	}

	file, err := os.OpenFile(s.path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		return fmt.Errorf("打开运行日志失败: %w", err)
	}
	defer file.Close()

	payload, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("序列化运行日志失败: %w", err)
	}
	if _, err := file.Write(append(payload, '\n')); err != nil {
		return fmt.Errorf("写入运行日志失败: %w", err)
	}
	s.records = append(s.records, record)
	s.trimRecordsLocked(now)
	s.maybeStartCleanupLocked(now)
	return nil
}

func (s *RuntimeLogStore) List(limit int) ([]RuntimeLogRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := time.Now().UTC().Add(-runtimeLogRetention)
	s.trimRecordsLocked(time.Now().UTC())
	result := append([]RuntimeLogRecord(nil), s.records...)
	if limit > 0 && len(result) > limit {
		result = result[len(result)-limit:]
	}
	for len(result) > 0 {
		recordedAt, ok := parseRuntimeLogRecordedAt(result[0].RecordedAt)
		if !ok || !recordedAt.Before(cutoff) {
			break
		}
		result = result[1:]
	}
	return result, nil
}

func (s *RuntimeLogStore) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	paths, err := s.listRuntimeLogFilesLocked()
	if err != nil {
		return err
	}
	for _, path := range paths {
		if path == s.path {
			continue
		}
		if removeErr := os.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) {
			return fmt.Errorf("删除轮转运行日志失败: %w", removeErr)
		}
	}
	file, err := os.Create(s.path)
	if err != nil {
		return fmt.Errorf("清空运行日志失败: %w", err)
	}
	s.records = nil
	s.currentDay = runtimeLogDay(time.Now().UTC())
	return file.Close()
}

func (s *RuntimeLogStore) maybeStartCleanupLocked(now time.Time) {
	if s.cleanupRunning {
		return
	}
	if !s.lastCleanupAt.IsZero() && now.Sub(s.lastCleanupAt) < runtimeLogCleanupInterval {
		return
	}
	s.cleanupRunning = true
	s.lastCleanupAt = now
	go s.cleanupExpired(now)
}

func (s *RuntimeLogStore) cleanupExpired(now time.Time) {
	cutoff := now.Add(-runtimeLogRetention)

	s.mu.Lock()
	defer func() {
		s.cleanupRunning = false
		s.mu.Unlock()
	}()
	_ = s.removeExpiredRotatedFilesLocked(cutoff)
	s.trimRecordsLocked(now)
}

func (s *RuntimeLogStore) reloadRecordsLocked() error {
	records := make([]RuntimeLogRecord, 0, 256)
	cutoff := time.Now().UTC().Add(-runtimeLogRetention)
	paths, err := s.listRuntimeLogFilesLocked()
	if err != nil {
		return err
	}
	for _, logPath := range paths {
		file, err := os.Open(logPath)
		if err != nil {
			if os.IsNotExist(err) && logPath == s.path {
				continue
			}
			return fmt.Errorf("打开运行日志失败: %w", err)
		}
		scanner := newRuntimeLogScanner(file)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			var record RuntimeLogRecord
			if err := json.Unmarshal([]byte(line), &record); err != nil {
				continue
			}
			if recordedAt, ok := parseRuntimeLogRecordedAt(record.RecordedAt); ok && recordedAt.Before(cutoff) {
				continue
			}
			records = append(records, record)
		}
		if scanErr := scanner.Err(); scanErr != nil {
			_ = file.Close()
			return fmt.Errorf("读取运行日志失败: %w", scanErr)
		}
		_ = file.Close()
	}
	s.records = records
	s.trimRecordsLocked(time.Now().UTC())
	return nil
}

func (s *RuntimeLogStore) trimRecordsLocked(now time.Time) {
	cutoff := now.Add(-runtimeLogRetention)
	if len(s.records) > 0 {
		filtered := s.records[:0]
		for _, record := range s.records {
			if recordedAt, ok := parseRuntimeLogRecordedAt(record.RecordedAt); ok && recordedAt.Before(cutoff) {
				continue
			}
			filtered = append(filtered, record)
		}
		s.records = filtered
	}
	if len(s.records) > runtimeLogMemoryLimit {
		s.records = append([]RuntimeLogRecord(nil), s.records[len(s.records)-runtimeLogMemoryLimit:]...)
	}
}

func newRuntimeLogScanner(r io.Reader) *bufio.Scanner {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), runtimeLogScannerBuffer)
	return scanner
}

func parseRuntimeLogRecordedAt(raw string) (time.Time, bool) {
	recordedAt, err := time.Parse(time.RFC3339, strings.TrimSpace(raw))
	if err != nil {
		return time.Time{}, false
	}
	return recordedAt.UTC(), true
}

func runtimeLogDay(now time.Time) string {
	return now.UTC().Format("20060102")
}

func detectRuntimeLogBaseFileDay(path string) (string, bool) {
	file, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer file.Close()

	scanner := newRuntimeLogScanner(file)
	lastDay := ""
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var record RuntimeLogRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			continue
		}
		recordedAt, ok := parseRuntimeLogRecordedAt(record.RecordedAt)
		if !ok {
			continue
		}
		lastDay = runtimeLogDay(recordedAt)
	}
	if lastDay != "" {
		return lastDay, true
	}
	info, err := file.Stat()
	if err != nil || info.Size() == 0 {
		return "", false
	}
	return runtimeLogDay(info.ModTime().UTC()), true
}

func (s *RuntimeLogStore) rotateIfNeededLocked(now time.Time) error {
	currentDay := runtimeLogDay(now)
	if s.currentDay == "" {
		s.currentDay = currentDay
	}
	if s.currentDay == currentDay {
		return nil
	}
	if err := s.archiveBaseFileToDayLocked(s.currentDay); err != nil {
		return err
	}
	if err := ensureRuntimeLogBaseFile(s.path); err != nil {
		return err
	}
	s.currentDay = currentDay
	return nil
}

func ensureRuntimeLogBaseFile(path string) error {
	file, err := os.OpenFile(path, os.O_CREATE, 0o644)
	if err != nil {
		return fmt.Errorf("初始化运行日志文件失败: %w", err)
	}
	return file.Close()
}

func runtimeLogRotatedPath(basePath string, day string) string {
	ext := filepath.Ext(basePath)
	name := strings.TrimSuffix(filepath.Base(basePath), ext)
	if ext == "" {
		ext = ".jsonl"
	}
	return filepath.Join(filepath.Dir(basePath), fmt.Sprintf("%s_%s%s", name, day, ext))
}

func (s *RuntimeLogStore) listRuntimeLogFilesLocked() ([]string, error) {
	dir := filepath.Dir(s.path)
	base := filepath.Base(s.path)
	ext := filepath.Ext(base)
	prefix := strings.TrimSuffix(base, ext) + "_"
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("读取运行日志目录失败: %w", err)
	}
	paths := make([]string, 0, len(entries)+1)
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ext) {
			continue
		}
		paths = append(paths, filepath.Join(dir, name))
	}
	paths = append(paths, s.path)
	sort.Strings(paths)
	return paths, nil
}

func (s *RuntimeLogStore) removeExpiredRotatedFilesLocked(cutoff time.Time) error {
	dir := filepath.Dir(s.path)
	base := filepath.Base(s.path)
	ext := filepath.Ext(base)
	prefix := strings.TrimSuffix(base, ext) + "_"
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	cutoffDay := runtimeLogDay(cutoff)
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ext) {
			continue
		}
		day := strings.TrimSuffix(strings.TrimPrefix(name, prefix), ext)
		if day >= cutoffDay {
			continue
		}
		_ = os.Remove(filepath.Join(dir, name))
	}
	return nil
}

func (s *RuntimeLogStore) archiveBaseFileToDayLocked(day string) error {
	day = strings.TrimSpace(day)
	if day == "" {
		return nil
	}
	info, err := os.Stat(s.path)
	if os.IsNotExist(err) || (err == nil && info.Size() == 0) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("读取当前运行日志失败: %w", err)
	}
	rotatedPath := runtimeLogRotatedPath(s.path, day)
	if _, err := os.Stat(rotatedPath); os.IsNotExist(err) {
		if renameErr := os.Rename(s.path, rotatedPath); renameErr != nil && !os.IsNotExist(renameErr) {
			return fmt.Errorf("轮转运行日志失败: %w", renameErr)
		}
		return nil
	} else if err != nil {
		return fmt.Errorf("检查轮转运行日志失败: %w", err)
	}

	src, err := os.Open(s.path)
	if err != nil {
		return fmt.Errorf("打开当前运行日志失败: %w", err)
	}
	defer src.Close()
	dst, err := os.OpenFile(rotatedPath, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		return fmt.Errorf("打开轮转运行日志失败: %w", err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		return fmt.Errorf("合并轮转运行日志失败: %w", err)
	}
	if err := dst.Close(); err != nil {
		return fmt.Errorf("关闭轮转运行日志失败: %w", err)
	}
	if err := os.Remove(s.path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("清理旧运行日志失败: %w", err)
	}
	return nil
}

// runtimeLogWriter 把任意写入按行切分并落盘成结构化运行日志记录（带 trace/run/cycle 元数据），
// 同时镜像到 mirror（通常是 stdout）。从 runtime_controller.go 同包搬移，行为不变。
type runtimeLogWriter struct {
	store            *RuntimeLogStore
	source           string
	mirror           io.Writer
	traceIDProvider  func() (string, string)
	metadataProvider func() TraceRecordMetadata
	mu               sync.Mutex
	pending          string
}

func newRuntimeLogWriter(store *RuntimeLogStore, source string, mirror io.Writer, traceIDProvider func() (string, string), metadataProvider func() TraceRecordMetadata) *runtimeLogWriter {
	return &runtimeLogWriter{
		store:            store,
		source:           source,
		mirror:           mirror,
		traceIDProvider:  traceIDProvider,
		metadataProvider: metadataProvider,
	}
}

func (w *runtimeLogWriter) Write(p []byte) (int, error) {
	if w.mirror != nil {
		if _, err := w.mirror.Write(p); err != nil {
			return 0, err
		}
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	data := w.pending + string(p)
	lines := strings.Split(data, "\n")
	if !strings.HasSuffix(data, "\n") {
		w.pending = lines[len(lines)-1]
		lines = lines[:len(lines)-1]
	} else {
		w.pending = ""
	}

	for _, line := range lines {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		runID, cycleID := "", ""
		if w.traceIDProvider != nil {
			runID, cycleID = w.traceIDProvider()
		}
		metadata := TraceRecordMetadata{}
		if w.metadataProvider != nil {
			metadata = w.metadataProvider().normalized()
		}
		if err := w.store.Append(RuntimeLogRecord{
			TraceRecordMetadata: metadata,
			RecordedAt:          time.Now().UTC().Format(time.RFC3339),
			RunID:               strings.TrimSpace(runID),
			CycleID:             strings.TrimSpace(cycleID),
			Source:              w.source,
			Message:             line,
		}); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}
