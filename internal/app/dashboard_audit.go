package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const dashboardAuditLogJSONLName = "dashboard_control_audit.jsonl"

type DashboardAuditRecord struct {
	TraceRecordMetadata
	RecordedAt string `json:"recorded_at"`
	RunID      string `json:"run_id"`
	CycleID    string `json:"cycle_id"`
	Action     string `json:"action"`
	Success    bool   `json:"success"`
	Detail     string `json:"detail"`
	Source     string `json:"source"`
	Operator   string `json:"operator"`
	TokenHash  string `json:"token_hash"`
	// Venue 记录本次控制操作作用的所(§13.2:审计保持单文件 + 每条带 venue 字段,聚合查询方便)。
	Venue string `json:"venue,omitempty"`
}

type DashboardAuditStore struct {
	path string
	mu   sync.Mutex
}

func NewDashboardAuditStore(path string) (*DashboardAuditStore, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("创建审计日志目录失败: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("初始化审计日志文件失败: %w", err)
	}
	_ = file.Close()
	return &DashboardAuditStore{path: path}, nil
}

func (s *DashboardAuditStore) Append(record DashboardAuditRecord) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	file, err := os.OpenFile(s.path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		return fmt.Errorf("打开审计日志失败: %w", err)
	}
	defer file.Close()

	payload, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("序列化审计日志失败: %w", err)
	}
	if _, err := file.Write(append(payload, '\n')); err != nil {
		return fmt.Errorf("写入审计日志失败: %w", err)
	}
	// fsync:审计记录是控制操作的唯一留痕,进程 crash 不应丢最后一条(与 store.AppendJSONLRecord 同口径)。
	if err := file.Sync(); err != nil {
		return fmt.Errorf("落盘审计日志失败: %w", err)
	}
	return nil
}

func normalizeAuditDetail(detail string) string {
	return strings.TrimSpace(detail)
}

func newDashboardAuditRecord(action string, success bool, detail string, source string, operator string, tokenHash string, runID string, cycleID string, venue string, metadata TraceRecordMetadata) DashboardAuditRecord {
	return DashboardAuditRecord{
		TraceRecordMetadata: metadata.normalized(),
		RecordedAt:          time.Now().UTC().Format(time.RFC3339),
		RunID:               strings.TrimSpace(runID),
		CycleID:             strings.TrimSpace(cycleID),
		Action:              strings.TrimSpace(action),
		Success:             success,
		Detail:              normalizeAuditDetail(detail),
		Source:              strings.TrimSpace(source),
		Operator:            strings.TrimSpace(operator),
		TokenHash:           strings.TrimSpace(tokenHash),
		Venue:               strings.TrimSpace(venue),
	}
}
