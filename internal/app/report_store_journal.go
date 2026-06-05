package app

import (
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
