// Package store 承载落盘的低层文件持久化原语：JSON / JSONL / CSV 的创建、读取、追加、原子重写，
// 以及按 recorded_at 的体积压缩与旧格式迁移。这些原语只依赖标准库与基础类型，不耦合任何业务领域
// 模型，因此可被实盘记录存储、回测落盘、运行时日志等复用，并独立做单元测试——是 Gate 3「落盘抽象
// 接口化（JSONL/SQLite 可换）」的基础设施层：未来要切到 SQLite，只需在本包内新增等价实现并让
// 上层存储门面注入不同后端，而无需触动业务代码。
package store

import (
	"bufio"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// lineScannerBuffer 是 JSONL 单行扫描缓冲上限（1MiB）：落盘记录可能较长，默认 64KiB 不够。
const lineScannerBuffer = 1024 * 1024

func newLineScanner(r io.Reader) *bufio.Scanner {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), lineScannerBuffer)
	return scanner
}

// EnsureJSONFile 确保 JSON 文件存在；不存在时写入空数组占位。
func EnsureJSONFile(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("检查 JSON 文件失败: %w", err)
	}
	return WriteJSONFile(path, []any{})
}

// EnsureJSONLFile 确保 JSONL 文件（及其目录）存在；不存在时创建空文件。
func EnsureJSONLFile(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("检查 JSONL 文件失败: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("创建 JSONL 目录失败: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE, 0o644)
	if err != nil {
		return fmt.Errorf("初始化 JSONL 文件失败: %w", err)
	}
	return file.Close()
}

// WriteJSONFile 以「临时文件 + 原子 rename」方式整体重写 JSON 文件，避免中途崩溃留下半截内容。
func WriteJSONFile(path string, payload any) error {
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化 JSON 失败: %w", err)
	}
	data = append(data, '\n')
	if mkdirErr := os.MkdirAll(filepath.Dir(path), 0o755); mkdirErr != nil {
		return fmt.Errorf("创建 JSON 目录失败: %w", mkdirErr)
	}
	tempFile, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("创建 JSON 临时文件失败: %w", err)
	}
	tempPath := tempFile.Name()
	success := false
	defer func() {
		_ = tempFile.Close()
		if !success {
			_ = os.Remove(tempPath)
		}
	}()
	if _, err := tempFile.Write(data); err != nil {
		return fmt.Errorf("写入 JSON 临时文件失败: %w", err)
	}
	if syncErr := tempFile.Sync(); syncErr != nil {
		return fmt.Errorf("刷新 JSON 临时文件失败: %w", syncErr)
	}
	if closeErr := tempFile.Close(); closeErr != nil {
		return fmt.Errorf("关闭 JSON 临时文件失败: %w", closeErr)
	}
	if renameErr := os.Rename(tempPath, path); renameErr != nil {
		return fmt.Errorf("提交 JSON 文件失败: %w", renameErr)
	}
	success = true
	return nil
}

// AppendJSONLRecord 向 JSONL 文件追加单条记录（每行一个 JSON 对象）。
func AppendJSONLRecord(path string, payload any) error {
	if err := EnsureJSONLFile(path); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("打开 JSONL 追加失败: %w", err)
	}
	defer file.Close()
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("序列化 JSONL 记录失败: %w", err)
	}
	if _, err := file.Write(append(encoded, '\n')); err != nil {
		return fmt.Errorf("写入 JSONL 记录失败: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("刷新 JSONL 文件失败: %w", err)
	}
	return nil
}

// AppendJSONLRecords 向 JSONL 文件批量追加记录；空切片为 no-op。
func AppendJSONLRecords[T any](path string, payloads []T) error {
	if len(payloads) == 0 {
		return nil
	}
	if err := EnsureJSONLFile(path); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("打开 JSONL 追加失败: %w", err)
	}
	defer file.Close()
	for _, payload := range payloads {
		encoded, marshalErr := json.Marshal(payload)
		if marshalErr != nil {
			return fmt.Errorf("序列化 JSONL 记录失败: %w", marshalErr)
		}
		if _, err := file.Write(append(encoded, '\n')); err != nil {
			return fmt.Errorf("写入 JSONL 记录失败: %w", err)
		}
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("刷新 JSONL 文件失败: %w", err)
	}
	return nil
}

// ReadJSONFile 读取整体 JSON 数组文件；文件缺失会被补建为空数组。
func ReadJSONFile[T any](path string) ([]T, error) {
	if err := EnsureJSONFile(path); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取 JSON 文件失败: %w", err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return []T{}, nil
	}
	var records []T
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, fmt.Errorf("解析 JSON 文件失败: %w", err)
	}
	if records == nil {
		return []T{}, nil
	}
	return records, nil
}

// ReadJSONLFile 逐行读取 JSONL 文件；空行跳过。
func ReadJSONLFile[T any](path string) ([]T, error) {
	if err := EnsureJSONLFile(path); err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("打开 JSONL 文件失败: %w", err)
	}
	defer file.Close()

	result := make([]T, 0, 128)
	scanner := newLineScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var record T
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			return nil, fmt.Errorf("解析 JSONL 记录失败: %w", err)
		}
		result = append(result, record)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("读取 JSONL 文件失败: %w", err)
	}
	return result, nil
}

// CompactJSONByRecordedAt 当 JSON 文件超过体积上限时，剔除早于 now-retention 的记录后整体重写。
func CompactJSONByRecordedAt(path string, now time.Time, retention time.Duration, maxFileSizeBytes int64) (bool, error) {
	if retention <= 0 || maxFileSizeBytes <= 0 {
		return false, nil
	}
	stat, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("检查 JSON 文件大小失败: %w", err)
	}
	if stat.Size() <= maxFileSizeBytes {
		return false, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("读取 JSON 文件失败: %w", err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(data, &rows); err != nil {
		return false, fmt.Errorf("解析 JSON 文件失败: %w", err)
	}
	cutoff := now.Add(-retention)
	kept := make([]map[string]any, 0, len(rows))
	compacted := false
	for _, row := range rows {
		recordedAtText, _ := row["recorded_at"].(string)
		recordedAt, ok := ParseCSVRecordedAt(recordedAtText)
		if ok && recordedAt.Before(cutoff) {
			compacted = true
			continue
		}
		kept = append(kept, row)
	}
	if !compacted {
		return false, nil
	}
	if err := WriteJSONFile(path, kept); err != nil {
		return false, err
	}
	return true, nil
}

// CompactJSONLByRecordedAt 当 JSONL 文件超过体积上限时，流式剔除早于 now-retention 的记录后原子重写。
func CompactJSONLByRecordedAt(path string, now time.Time, retention time.Duration, maxFileSizeBytes int64) (bool, error) {
	if retention <= 0 || maxFileSizeBytes <= 0 {
		return false, nil
	}
	stat, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("检查 JSONL 文件大小失败: %w", err)
	}
	if stat.Size() <= maxFileSizeBytes {
		return false, nil
	}

	source, err := os.Open(path)
	if err != nil {
		return false, fmt.Errorf("打开 JSONL 文件失败: %w", err)
	}
	defer source.Close()

	tempFile, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return false, fmt.Errorf("创建 JSONL 临时文件失败: %w", err)
	}
	tempPath := tempFile.Name()
	success := false
	defer func() {
		_ = tempFile.Close()
		if !success {
			_ = os.Remove(tempPath)
		}
	}()

	cutoff := now.Add(-retention)
	scanner := newLineScanner(source)
	compacted := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var row map[string]any
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			return false, fmt.Errorf("解析 JSONL 记录失败: %w", err)
		}
		recordedAtText, _ := row["recorded_at"].(string)
		recordedAt, ok := ParseCSVRecordedAt(recordedAtText)
		if ok && recordedAt.Before(cutoff) {
			compacted = true
			continue
		}
		if _, err := tempFile.WriteString(line + "\n"); err != nil {
			return false, fmt.Errorf("写入 JSONL 临时文件失败: %w", err)
		}
	}
	if err := scanner.Err(); err != nil {
		return false, fmt.Errorf("读取 JSONL 文件失败: %w", err)
	}
	if !compacted {
		return false, nil
	}
	if err := tempFile.Sync(); err != nil {
		return false, fmt.Errorf("刷新 JSONL 临时文件失败: %w", err)
	}
	if err := tempFile.Close(); err != nil {
		return false, fmt.Errorf("关闭 JSONL 临时文件失败: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return false, fmt.Errorf("提交 JSONL 文件失败: %w", err)
	}
	success = true
	return true, nil
}

// ClearJSONLFile 清空 JSONL 文件内容（保留文件本身）。
func ClearJSONLFile(path string) error {
	if err := EnsureJSONLFile(path); err != nil {
		return err
	}
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("清空 JSONL 文件失败: %w", err)
	}
	return file.Close()
}

// MigrateLegacyJSONFile 当目标 JSON 不存在但旧 CSV 存在时，用 parser 把旧 CSV 转写为 JSON。
func MigrateLegacyJSONFile(path string, legacyCSVPath string, parser func(rows [][]string) any) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("检查 JSON 文件失败: %w", err)
	}
	if _, err := os.Stat(legacyCSVPath); err != nil {
		if os.IsNotExist(err) {
			return EnsureJSONFile(path)
		}
		return fmt.Errorf("检查旧 CSV 文件失败: %w", err)
	}
	rows, err := ReadCSVRows(legacyCSVPath)
	if err != nil {
		return err
	}
	return WriteJSONFile(path, parser(rows))
}

// MigrateLegacyJSONLFile 按「旧 JSON → 旧 CSV → 空文件」优先级把历史数据迁移到 JSONL。
func MigrateLegacyJSONLFile[T any](path string, legacyJSONPath string, legacyCSVPath string, csvParser func(rows [][]string) []T) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("检查 JSONL 文件失败: %w", err)
	}
	if _, err := os.Stat(legacyJSONPath); err == nil {
		records, readErr := ReadJSONFile[T](legacyJSONPath)
		if readErr != nil {
			return readErr
		}
		return RewriteJSONLFile(path, records)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("检查旧 JSON 文件失败: %w", err)
	}
	if _, err := os.Stat(legacyCSVPath); err == nil {
		rows, readErr := ReadCSVRows(legacyCSVPath)
		if readErr != nil {
			return readErr
		}
		return RewriteJSONLFile(path, csvParser(rows))
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("检查旧 CSV 文件失败: %w", err)
	}
	return EnsureJSONLFile(path)
}

// RewriteJSONLFile 用给定记录整体重建 JSONL 文件。
func RewriteJSONLFile[T any](path string, records []T) error {
	if err := EnsureJSONLFile(path); err != nil {
		return err
	}
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("重建 JSONL 文件失败: %w", err)
	}
	defer file.Close()
	for _, record := range records {
		encoded, marshalErr := json.Marshal(record)
		if marshalErr != nil {
			return fmt.Errorf("序列化 JSONL 记录失败: %w", marshalErr)
		}
		if _, err := file.Write(append(encoded, '\n')); err != nil {
			return fmt.Errorf("写入 JSONL 记录失败: %w", err)
		}
	}
	return file.Sync()
}

// EnsureCSVHeader 确保 CSV 文件存在并写入表头；已存在则不动。
func EnsureCSVHeader(path string, header []string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("检查 CSV 文件失败: %w", err)
	}

	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("创建 CSV 文件失败: %w", err)
	}
	defer file.Close()

	writer := csv.NewWriter(file)
	if err := writer.Write(header); err != nil {
		return fmt.Errorf("写入 CSV 表头失败: %w", err)
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return fmt.Errorf("刷新 CSV 表头失败: %w", err)
	}
	return nil
}

// RewriteCSVWithHeader 用单行表头整体重建 CSV 文件。
func RewriteCSVWithHeader(path string, header []string) error {
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("重建 CSV 文件失败: %w", err)
	}
	defer file.Close()

	writer := csv.NewWriter(file)
	if err := writer.Write(header); err != nil {
		return fmt.Errorf("重写 CSV 表头失败: %w", err)
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return fmt.Errorf("刷新重写 CSV 表头失败: %w", err)
	}
	return nil
}

// RewriteCSV 用给定行整体重建 CSV 文件。
func RewriteCSV(path string, rows [][]string) error {
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("重建 CSV 文件失败: %w", err)
	}
	defer file.Close()

	writer := csv.NewWriter(file)
	for _, row := range rows {
		if err := writer.Write(row); err != nil {
			return fmt.Errorf("重写 CSV 记录失败: %w", err)
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return fmt.Errorf("刷新重写 CSV 记录失败: %w", err)
	}
	return nil
}

// AppendCSVRow 向 CSV 追加单行。
func AppendCSVRow(path string, row []string) error {
	return AppendCSVRows(path, [][]string{row})
}

// AppendCSVRows 向 CSV 批量追加行；空切片为 no-op。
func AppendCSVRows(path string, rows [][]string) error {
	if len(rows) == 0 {
		return nil
	}

	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("打开 CSV 追加失败: %w", err)
	}
	defer file.Close()

	writer := csv.NewWriter(file)
	for _, row := range rows {
		if err := writer.Write(row); err != nil {
			return fmt.Errorf("写入 CSV 记录失败: %w", err)
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return fmt.Errorf("刷新 CSV 记录失败: %w", err)
	}
	return nil
}

// CompactCSVByRecordedAt 当 CSV 超过体积上限时，剔除早于 now-retention 的行（保留表头）后重写。
func CompactCSVByRecordedAt(path string, header []string, now time.Time, retention time.Duration, maxFileSizeBytes int64) (bool, error) {
	if retention <= 0 || maxFileSizeBytes <= 0 {
		return false, nil
	}
	stat, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("检查 CSV 文件大小失败: %w", err)
	}
	if stat.Size() <= maxFileSizeBytes {
		return false, nil
	}
	recordedAtIndex := CSVColumnIndex(header, "recorded_at")
	if recordedAtIndex < 0 {
		return false, nil
	}

	file, err := os.Open(path)
	if err != nil {
		return false, fmt.Errorf("打开 CSV 失败: %w", err)
	}
	defer file.Close()

	rows, err := csv.NewReader(file).ReadAll()
	if err != nil {
		return false, fmt.Errorf("读取 CSV 失败: %w", err)
	}
	if len(rows) == 0 {
		return false, nil
	}
	cutoff := now.Add(-retention)
	kept := make([][]string, 0, len(rows))
	kept = append(kept, rows[0])
	compacted := false
	for _, row := range rows[1:] {
		if len(row) <= recordedAtIndex {
			kept = append(kept, row)
			continue
		}
		recordedAt, ok := ParseCSVRecordedAt(row[recordedAtIndex])
		if ok && recordedAt.Before(cutoff) {
			compacted = true
			continue
		}
		kept = append(kept, row)
	}
	if !compacted {
		return false, nil
	}
	if err := RewriteCSV(path, kept); err != nil {
		return false, err
	}
	return true, nil
}

// ReadCSVRows 读取 CSV 的数据行（跳过表头）；仅有表头或为空时返回空切片。
func ReadCSVRows(path string) ([][]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("打开 CSV 失败: %w", err)
	}
	defer file.Close()

	reader := csv.NewReader(file)
	allRows, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("读取 CSV 失败: %w", err)
	}
	if len(allRows) <= 1 {
		return [][]string{}, nil
	}
	return allRows[1:], nil
}

// ReadCSVTable 读取 CSV 的全部行（含表头）。
func ReadCSVTable(path string) ([][]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("打开 CSV 失败: %w", err)
	}
	defer file.Close()

	allRows, err := csv.NewReader(file).ReadAll()
	if err != nil {
		return nil, fmt.Errorf("读取 CSV 失败: %w", err)
	}
	return allRows, nil
}

// CSVColumnIndex 返回表头中字段所在列下标；不存在返回 -1。
func CSVColumnIndex(header []string, field string) int {
	field = strings.TrimSpace(field)
	for index, item := range header {
		if strings.TrimSpace(item) == field {
			return index
		}
	}
	return -1
}

// ParseCSVRecordedAt 解析 recorded_at 时间戳（先 RFC3339Nano 再 RFC3339），失败返回 ok=false。
func ParseCSVRecordedAt(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	if parsed, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return parsed, true
	}
	if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
		return parsed, true
	}
	return time.Time{}, false
}
