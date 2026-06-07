package app

import (
	"reflect"
	"strings"
	"time"
)

// report_store_journal.go 实现 S8-C「结算 / pending funding 存储崩溃安全化」(R19)的 append-only 事实源。
//
// 背景:settlements.json / pending_funding_settlements.json 走「整文件原子重写」(temp+fsync+rename,已防撕裂写),
// 但「当前状态视图」与「审计事实」混在同一文件——若内存切片因 bug/部分加载被截断,原子写会忠实地把截断后的
// 状态落盘,历史随之丢失。本模块为这两类崩溃关键记录额外维护一条 append-only JSONL 日志(每次变更只追加、永不
// 重写、逐条 fsync),作为不可变事实源;原 JSON 文件退化为「物化视图」(快读当前态)。事实源可在需要时回放重建
// 视图(materializeSettlementsFromJournal / materializePendingFromJournal,纯函数、可单测)。

const (
	settlementJournalName     = "settlements_journal.jsonl"
	pendingFundingJournalName = "pending_funding_journal.jsonl"
)

// settlementJournalEntry 是结算事实源的一条变更:op=append(新建) | update(资金费实收后定稿)。
type settlementJournalEntry struct {
	Op     string              `json:"op"`
	At     string              `json:"at"`
	Record SettlementCSVRecord `json:"record"`
}

// pendingFundingJournalEntry 是 pending funding 事实源的一条变更:op=upsert | delete。
type pendingFundingJournalEntry struct {
	Op     string                         `json:"op"`
	At     string                         `json:"at"`
	Record PendingFundingSettlementRecord `json:"record"`
}

func (s *ReportStore) appendSettlementJournalLocked(op string, record SettlementCSVRecord) error {
	if strings.TrimSpace(s.settlementJournalPath) == "" {
		return nil
	}
	return appendJSONLRecord(s.settlementJournalPath, settlementJournalEntry{
		Op:     op,
		At:     time.Now().UTC().Format(time.RFC3339Nano),
		Record: record,
	})
}

func (s *ReportStore) appendPendingFundingJournalLocked(op string, record PendingFundingSettlementRecord) error {
	if strings.TrimSpace(s.pendingFundingJournalPath) == "" {
		return nil
	}
	return appendJSONLRecord(s.pendingFundingJournalPath, pendingFundingJournalEntry{
		Op:     op,
		At:     time.Now().UTC().Format(time.RFC3339Nano),
		Record: record,
	})
}

// materializeSettlementsFromJournal 把结算事实源回放成当前状态视图(纯函数):按 (RunID,CycleID) 折叠,
// append 建立首次出现并保序,update 原地替换。用于崩溃后从事实源重建视图。
func materializeSettlementsFromJournal(entries []settlementJournalEntry) []SettlementCSVRecord {
	order := make([]string, 0, len(entries))
	idx := make(map[string]int, len(entries))
	out := make([]SettlementCSVRecord, 0, len(entries))
	for _, e := range entries {
		key := strings.TrimSpace(e.Record.RunID) + "/" + strings.TrimSpace(e.Record.CycleID)
		if pos, ok := idx[key]; ok {
			out[pos] = e.Record
			continue
		}
		idx[key] = len(out)
		order = append(order, key)
		out = append(out, e.Record)
	}
	_ = order
	return out
}

// materializePendingFromJournal 把 pending funding 事实源回放成当前状态视图(纯函数):upsert 设置、delete 移除,
// 按 (RunID,CycleID) 折叠并保序。
func materializePendingFromJournal(entries []pendingFundingJournalEntry) []PendingFundingSettlementRecord {
	idx := make(map[string]int, len(entries))
	alive := make(map[string]bool, len(entries))
	out := make([]PendingFundingSettlementRecord, 0, len(entries))
	for _, e := range entries {
		key := strings.TrimSpace(e.Record.RunID) + "/" + strings.TrimSpace(e.Record.CycleID)
		switch e.Op {
		case "delete":
			if pos, ok := idx[key]; ok {
				alive[key] = false
				_ = pos
			}
		default: // upsert
			if pos, ok := idx[key]; ok {
				out[pos] = e.Record
			} else {
				idx[key] = len(out)
				out = append(out, e.Record)
			}
			alive[key] = true
		}
	}
	// 过滤掉已删除的键,保持首次出现顺序。
	filtered := make([]PendingFundingSettlementRecord, 0, len(out))
	for _, rec := range out {
		key := strings.TrimSpace(rec.RunID) + "/" + strings.TrimSpace(rec.CycleID)
		if alive[key] {
			filtered = append(filtered, rec)
		}
	}
	return filtered
}

func settlementJournalKey(rec SettlementCSVRecord) string {
	return strings.TrimSpace(rec.RunID) + "/" + strings.TrimSpace(rec.CycleID)
}

func pendingFundingJournalKey(rec PendingFundingSettlementRecord) string {
	return strings.TrimSpace(rec.RunID) + "/" + strings.TrimSpace(rec.CycleID)
}

// reconcileSettlementsWithJournal 用 append-only 事实源(journal)校正物化视图(materialized JSON)。
// journal 在每次变更时**先于** JSON 写,故若发生「journal 已追加、JSON 原子重写未落盘即崩溃」,journal 是更
// 完整、更权威的来源。合并规则(绝不丢数据):
//   - 以物化视图顺序为基准,对每个键用 journal 的最新内容覆盖(修正崩溃丢失的 update 定稿);
//   - journal 有而物化缺失的键(丢失的 append 写)按 journal 顺序补到末尾;
//   - 物化有而 journal 完全不知道的键(journal 被轮转/截断)原样保留。
//
// 返回合并结果与是否发生变化(changed=true 时需回写物化 JSON 并告警)。
func reconcileSettlementsWithJournal(materialized []SettlementCSVRecord, entries []settlementJournalEntry) ([]SettlementCSVRecord, bool) {
	if len(entries) == 0 {
		return materialized, false
	}
	journalView := materializeSettlementsFromJournal(entries)
	authoritative := make(map[string]SettlementCSVRecord, len(journalView))
	journalOrder := make([]string, 0, len(journalView))
	for _, rec := range journalView {
		key := settlementJournalKey(rec)
		authoritative[key] = rec
		journalOrder = append(journalOrder, key)
	}
	merged := make([]SettlementCSVRecord, 0, len(materialized)+len(journalView))
	seen := make(map[string]bool, len(materialized))
	changed := false
	for _, rec := range materialized {
		key := settlementJournalKey(rec)
		seen[key] = true
		if auth, ok := authoritative[key]; ok {
			if !reflect.DeepEqual(rec, auth) {
				changed = true
			}
			merged = append(merged, auth)
		} else {
			merged = append(merged, rec)
		}
	}
	for _, key := range journalOrder {
		if seen[key] {
			continue
		}
		merged = append(merged, authoritative[key])
		changed = true
	}
	return merged, changed
}

// reconcilePendingFundingWithJournal 同 reconcileSettlementsWithJournal,但 pending funding 事实源含 delete:
// 若 journal 已记录某键删除而物化 JSON 因崩溃仍残留该键,以 journal 为准移除之(避免对已结算 pending 重复结算)。
func reconcilePendingFundingWithJournal(materialized []PendingFundingSettlementRecord, entries []pendingFundingJournalEntry) ([]PendingFundingSettlementRecord, bool) {
	if len(entries) == 0 {
		return materialized, false
	}
	journalView := materializePendingFromJournal(entries)
	known := make(map[string]bool, len(entries))
	for _, e := range entries {
		known[pendingFundingJournalKey(e.Record)] = true
	}
	authoritative := make(map[string]PendingFundingSettlementRecord, len(journalView))
	journalOrder := make([]string, 0, len(journalView))
	for _, rec := range journalView {
		key := pendingFundingJournalKey(rec)
		authoritative[key] = rec
		journalOrder = append(journalOrder, key)
	}
	merged := make([]PendingFundingSettlementRecord, 0, len(materialized)+len(journalView))
	seen := make(map[string]bool, len(materialized))
	changed := false
	for _, rec := range materialized {
		key := pendingFundingJournalKey(rec)
		seen[key] = true
		if known[key] {
			auth, alive := authoritative[key]
			if !alive {
				// journal 最终态为已删除:丢弃物化残留(其删除写在崩溃中丢失)。
				changed = true
				continue
			}
			if !reflect.DeepEqual(rec, auth) {
				changed = true
			}
			merged = append(merged, auth)
		} else {
			merged = append(merged, rec)
		}
	}
	for _, key := range journalOrder {
		if seen[key] {
			continue
		}
		merged = append(merged, authoritative[key])
		changed = true
	}
	return merged, changed
}
