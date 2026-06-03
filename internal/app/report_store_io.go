package app

import (
	"encoding/csv"
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"lingxi/internal/store"
)

// 本文件是落盘低层 IO 的「同包薄适配层」：真正的 JSON / JSONL / CSV 持久化原语已抽到
// internal/store（Gate 3 落盘抽象接口化），这里保留原有小写函数名按原签名委托，避免触动 app 内
// 既有的大量调用点。仅与业务领域强耦合的迁移（执行事件 CSV schema、月度模拟 CSV→JSON）和
// MonitoredTarget 校验保留在本包，因为它们引用了领域类型/解析器。

func ensureJSONFile(path string) error { return store.EnsureJSONFile(path) }

func ensureJSONLFile(path string) error { return store.EnsureJSONLFile(path) }

func writeJSONFile(path string, payload any) error { return store.WriteJSONFile(path, payload) }

func appendJSONLRecord(path string, payload any) error { return store.AppendJSONLRecord(path, payload) }

func appendJSONLRecords[T any](path string, payloads []T) error {
	return store.AppendJSONLRecords(path, payloads)
}

func readJSONFile[T any](path string) ([]T, error) { return store.ReadJSONFile[T](path) }

func readJSONLFile[T any](path string) ([]T, error) { return store.ReadJSONLFile[T](path) }

func compactJSONByRecordedAt(path string, now time.Time, retention time.Duration, maxFileSizeBytes int64) (bool, error) {
	return store.CompactJSONByRecordedAt(path, now, retention, maxFileSizeBytes)
}

func compactJSONLByRecordedAt(path string, now time.Time, retention time.Duration, maxFileSizeBytes int64) (bool, error) {
	return store.CompactJSONLByRecordedAt(path, now, retention, maxFileSizeBytes)
}

func clearJSONLFile(path string) error { return store.ClearJSONLFile(path) }

func migrateLegacyJSONFile(path string, legacyCSVPath string, parser func(rows [][]string) any) error {
	return store.MigrateLegacyJSONFile(path, legacyCSVPath, parser)
}

func migrateLegacyJSONLFile[T any](path string, legacyJSONPath string, legacyCSVPath string, csvParser func(rows [][]string) []T) error {
	return store.MigrateLegacyJSONLFile(path, legacyJSONPath, legacyCSVPath, csvParser)
}

func rewriteJSONLFile[T any](path string, records []T) error {
	return store.RewriteJSONLFile(path, records)
}

func ensureCSVHeader(path string, header []string) error {
	return store.EnsureCSVHeader(path, header)
}

func rewriteCSVWithHeader(path string, header []string) error {
	return store.RewriteCSVWithHeader(path, header)
}

func rewriteCSV(path string, rows [][]string) error { return store.RewriteCSV(path, rows) }

func appendCSVRow(path string, row []string) error { return store.AppendCSVRow(path, row) }

func appendCSVRows(path string, rows [][]string) error { return store.AppendCSVRows(path, rows) }

func compactCSVByRecordedAt(path string, header []string, now time.Time, retention time.Duration, maxFileSizeBytes int64) (bool, error) {
	return store.CompactCSVByRecordedAt(path, header, now, retention, maxFileSizeBytes)
}

func readCSVRows(path string) ([][]string, error) { return store.ReadCSVRows(path) }

func readCSVTable(path string) ([][]string, error) { return store.ReadCSVTable(path) }

func csvColumnIndex(header []string, field string) int { return store.CSVColumnIndex(header, field) }

func parseCSVRecordedAt(raw string) (time.Time, bool) { return store.ParseCSVRecordedAt(raw) }

// migrateExecutionCSVSchema 是执行事件 CSV 的领域专属 schema 迁移：当旧表头列数与新 schema 不一致
// 时，按旧解析器重读并以新表头重写。它依赖 executionCSVHeader/parseExecutionRows/executionIncidentRow
// 等领域定义，故保留在 app 包，仅复用 store 的通用读写原语。
func migrateExecutionCSVSchema(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("打开执行事件 CSV 失败: %w", err)
	}
	defer file.Close()

	rows, err := csv.NewReader(file).ReadAll()
	if err != nil {
		return fmt.Errorf("读取执行事件 CSV 失败: %w", err)
	}
	if len(rows) == 0 {
		return rewriteCSVWithHeader(path, executionCSVHeader)
	}
	header := rows[0]
	if len(header) == len(executionCSVHeader) {
		return nil
	}
	if len(header) != 9 {
		return nil
	}

	records := parseExecutionRows(rows[1:])
	if err := rewriteCSVWithHeader(path, executionCSVHeader); err != nil {
		return err
	}
	payload := make([][]string, 0, len(records))
	for _, record := range records {
		payload = append(payload, executionIncidentRow(record))
	}
	return appendCSVRows(path, payload)
}

// migrateLegacyMonthlyJSONFile 是月度模拟日志的领域专属迁移：把旧 CSV 表逐行转为 map[string]string
// 后写成 JSON。它依赖 app 内的 maxIntValue 等工具，故保留在本包，仅复用 store 的通用读写原语。
func migrateLegacyMonthlyJSONFile(path string, legacyCSVPath string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("检查月度模拟 JSON 文件失败: %w", err)
	}
	if _, err := os.Stat(legacyCSVPath); err != nil {
		if os.IsNotExist(err) {
			return ensureJSONFile(path)
		}
		return fmt.Errorf("检查旧月度模拟 CSV 文件失败: %w", err)
	}
	table, err := readCSVTable(legacyCSVPath)
	if err != nil {
		return err
	}
	if len(table) == 0 {
		return ensureJSONFile(path)
	}
	header := table[0]
	payload := make([]map[string]string, 0, maxIntValue(0, len(table)-1))
	for _, row := range table[1:] {
		item := make(map[string]string, len(header))
		for index, key := range header {
			if index < len(row) {
				item[key] = row[index]
			} else {
				item[key] = ""
			}
		}
		payload = append(payload, item)
	}
	return writeJSONFile(path, payload)
}

func atomicLoadTargetValid(target *MonitoredTarget) bool {
	return target != nil && atomic.LoadInt32(&target.IsValid) == 1
}
