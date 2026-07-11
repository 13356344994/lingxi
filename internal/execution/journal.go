package execution

// journal.go 实现 append-only WAL(write-ahead log):每条记录追加即 fsync,重放时按到达顺序
// 逐行还原;末尾一行如因崩溃写到一半而不是合法 JSON,视为「崩溃截断」直接丢弃而不报错——这是
// append-only WAL 的标准语义(最后一条未确认的写入允许丢失,之前的必须全部可信);中间任意一行
// 损坏则视为磁盘级别的数据损坏,必须报错而不是静默跳过。
import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// RecordKind 枚举 WAL 记录类型,覆盖 intent/claim/order/fill/quarantine 全生命周期,
// 保证「decision → intent → order → fill → position」全链路都能从 WAL 重放还原。
type RecordKind string

const (
	RecordDecisionOpened    RecordKind = "decision_opened"
	RecordIntentCreated     RecordKind = "intent_created"
	RecordIntentTransition  RecordKind = "intent_transition"
	RecordClaimAcquired     RecordKind = "claim_acquired"
	RecordClaimReleased     RecordKind = "claim_released"
	RecordOrderSubmitted    RecordKind = "order_submitted"
	RecordOrderTransition   RecordKind = "order_transition"
	RecordFillApplied       RecordKind = "fill_applied"
	RecordQuarantined       RecordKind = "quarantined"
	RecordQuarantineCleared RecordKind = "quarantine_cleared"
	RecordRecoverySnapshot  RecordKind = "recovery_snapshot"
)

// Record 是 WAL 的单条日志行(一行一个 JSON 对象)。Seq 由 WAL 在追加时单调赋值,
// 不由调用方指定,保证重放顺序=写入顺序=seq 递增顺序三者一致。
type Record struct {
	Seq             uint64          `json:"seq"`
	WrittenAt       time.Time       `json:"written_at"`
	Kind            RecordKind      `json:"kind"`
	DecisionID      string          `json:"decision_id,omitempty"`
	IntentID        string          `json:"intent_id,omitempty"`
	Symbol          string          `json:"symbol,omitempty"`
	ClientOrderID   string          `json:"client_order_id,omitempty"`
	ExchangeOrderID string          `json:"exchange_order_id,omitempty"`
	TradeID         string          `json:"trade_id,omitempty"`
	Payload         json.RawMessage `json:"payload,omitempty"`
}

// WAL 是单文件 append-only 日志的并发安全句柄。
type WAL struct {
	mu   sync.Mutex
	file *os.File
	path string
	seq  uint64
}

// OpenWAL 打开(或创建)path 处的 WAL 文件:先容错重放已有内容以恢复 seq 计数器,
// 再以 O_APPEND 打开供后续写入,保证「重启后继续追加」不会重复 seq 也不会跳号。
func OpenWAL(path string) (*WAL, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("execution: 创建 WAL 目录失败: %w", err)
		}
	}
	if err := repairWALTail(path); err != nil {
		return nil, err
	}
	existing, err := ReplayWAL(path)
	if err != nil {
		return nil, err
	}
	var lastSeq uint64
	if len(existing) > 0 {
		lastSeq = existing[len(existing)-1].Seq
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("execution: 打开 WAL 失败: %w", err)
	}
	return &WAL{file: f, path: path, seq: lastSeq}, nil
}

// repairWALTail 在重新打开 WAL 前修掉崩溃留下的半截尾行；否则后续 O_APPEND 会把新 JSON
// 直接拼到半截 JSON 后面，导致新记录也永远无法重放。合法但缺少换行的最后一条记录只补换行。
func repairWALTail(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("execution: 检查 WAL 尾部失败: %w", err)
	}
	if len(data) == 0 {
		return nil
	}
	end := len(data)
	for end > 0 && (data[end-1] == '\n' || data[end-1] == '\r') {
		end--
	}
	if end == 0 {
		return nil
	}
	lineStart := 0
	if idx := strings.LastIndexByte(string(data[:end]), '\n'); idx >= 0 {
		lineStart = idx + 1
	}
	var rec Record
	if json.Unmarshal(data[lineStart:end], &rec) == nil {
		if data[len(data)-1] == '\n' {
			return nil
		}
		f, openErr := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
		if openErr != nil {
			return fmt.Errorf("execution: 打开 WAL 补换行失败: %w", openErr)
		}
		if _, writeErr := f.Write([]byte{'\n'}); writeErr != nil {
			_ = f.Close()
			return fmt.Errorf("execution: WAL 补换行失败: %w", writeErr)
		}
		if syncErr := f.Sync(); syncErr != nil {
			_ = f.Close()
			return fmt.Errorf("execution: WAL 补换行刷盘失败: %w", syncErr)
		}
		return f.Close()
	}
	if err := os.Truncate(path, int64(lineStart)); err != nil {
		return fmt.Errorf("execution: 截断 WAL 崩溃尾行失败: %w", err)
	}
	return nil
}

// Append 追加一条记录:赋值单调 Seq、补齐 WrittenAt(若未指定)、写入一行 JSON 并立即 fsync。
// fsync 失败会返回错误,调用方必须视为「本条未落盘」处理(不能假定它已生效)。
func (w *WAL) Append(rec Record) (Record, error) {
	if w == nil || w.file == nil {
		return Record{}, fmt.Errorf("execution: WAL 未打开")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.seq++
	rec.Seq = w.seq
	if rec.WrittenAt.IsZero() {
		rec.WrittenAt = time.Now().UTC()
	}
	encoded, err := json.Marshal(rec)
	if err != nil {
		w.seq-- // 序列化失败不消费序号,保持「seq 只在成功落盘时前进」
		return Record{}, fmt.Errorf("execution: 序列化 WAL 记录失败: %w", err)
	}
	encoded = append(encoded, '\n')
	if _, err := w.file.Write(encoded); err != nil {
		return Record{}, fmt.Errorf("execution: 写入 WAL 失败: %w", err)
	}
	if err := w.file.Sync(); err != nil {
		return Record{}, fmt.Errorf("execution: 刷盘 WAL 失败: %w", err)
	}
	return rec, nil
}

// Close 关闭底层文件句柄。
func (w *WAL) Close() error {
	if w == nil || w.file == nil {
		return nil
	}
	return w.file.Close()
}

// Path 返回 WAL 落盘路径,便于日志/诊断引用。
func (w *WAL) Path() string {
	if w == nil {
		return ""
	}
	return w.path
}

// walScanBufferBytes 是单行 WAL 记录扫描缓冲上限(1MiB),对齐 internal/store 的 JSONL 读取惯例。
const walScanBufferBytes = 1024 * 1024

// ReplayWAL 按写入顺序重放 path 处的全部合法记录。文件不存在返回空切片、nil error(全新 WAL
// 的正常起点)。末尾一行若不是合法 JSON,视为「崩溃写到一半」直接丢弃、不报错;中间任意一行
// 损坏则视为磁盘损坏,返回错误而不是静默跳过——这条边界正是「crash 只丢最后一条未确认写入,
// 之前全部可信」与「静默数据损坏」之间唯一安全的区分线。
func ReplayWAL(path string) ([]Record, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("execution: 打开 WAL 重放失败: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), walScanBufferBytes)
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("execution: 读取 WAL 失败: %w", err)
	}

	records := make([]Record, 0, len(lines))
	for i, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		var rec Record
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			if i == len(lines)-1 {
				break // 末尾行崩溃截断,丢弃不报错
			}
			return nil, fmt.Errorf("execution: WAL 第 %d 行损坏(非末尾行,视为磁盘损坏): %w", i+1, err)
		}
		wantSeq := uint64(len(records) + 1)
		if rec.Seq != wantSeq {
			return nil, fmt.Errorf("execution: WAL sequence 不连续: got=%d want=%d", rec.Seq, wantSeq)
		}
		records = append(records, rec)
	}
	return records, nil
}
