package app

import (
	"testing"
)

func TestMaterializeSettlementsFromJournal(t *testing.T) {
	entries := []settlementJournalEntry{
		{Op: "append", Record: SettlementCSVRecord{RunID: "r", CycleID: "c1", NetPnL: "1.0", FundingIncomePending: true}},
		{Op: "append", Record: SettlementCSVRecord{RunID: "r", CycleID: "c2", NetPnL: "2.0"}},
		{Op: "update", Record: SettlementCSVRecord{RunID: "r", CycleID: "c1", NetPnL: "1.5", FundingIncomePending: false}},
	}
	got := materializeSettlementsFromJournal(entries)
	if len(got) != 2 {
		t.Fatalf("应折叠成 2 条, got %d", len(got))
	}
	// c1 应反映 update 后的值,且保持首次出现顺序(c1 在 c2 前)。
	if got[0].CycleID != "c1" || got[0].NetPnL != "1.5" || got[0].FundingIncomePending {
		t.Fatalf("c1 应回放为 update 后的定稿态, got %+v", got[0])
	}
	if got[1].CycleID != "c2" {
		t.Fatalf("顺序应保持, got %+v", got[1])
	}
}

func TestMaterializePendingFromJournal(t *testing.T) {
	entries := []pendingFundingJournalEntry{
		{Op: "upsert", Record: PendingFundingSettlementRecord{RunID: "r", CycleID: "c1", AttemptCount: 1}},
		{Op: "upsert", Record: PendingFundingSettlementRecord{RunID: "r", CycleID: "c2", AttemptCount: 1}},
		{Op: "upsert", Record: PendingFundingSettlementRecord{RunID: "r", CycleID: "c1", AttemptCount: 3}},
		{Op: "delete", Record: PendingFundingSettlementRecord{RunID: "r", CycleID: "c2"}},
	}
	got := materializePendingFromJournal(entries)
	if len(got) != 1 {
		t.Fatalf("c2 已删,应剩 1 条, got %d", len(got))
	}
	if got[0].CycleID != "c1" || got[0].AttemptCount != 3 {
		t.Fatalf("c1 应反映最后一次 upsert(AttemptCount=3), got %+v", got[0])
	}
}

// TestSettlementJournalReconstructsView 端到端:对 ReportStore 做若干变更后,从 append-only 事实源回放出的
// 视图应与当前物化视图一致(崩溃后可据事实源重建)。
func TestSettlementJournalReconstructsView(t *testing.T) {
	store, err := NewReportStore(t.TempDir())
	if err != nil {
		t.Fatalf("建 store: %v", err)
	}
	if err := store.AppendSettlement(CycleSettlement{RunID: "r", CycleID: "c1", NetPnL: 1.0, FundingIncomePending: true}); err != nil {
		t.Fatalf("append c1: %v", err)
	}
	if err := store.AppendSettlement(CycleSettlement{RunID: "r", CycleID: "c2", NetPnL: 2.0}); err != nil {
		t.Fatalf("append c2: %v", err)
	}
	if err := store.UpdateSettlement(CycleSettlement{RunID: "r", CycleID: "c1", NetPnL: 1.5, FundingIncomePending: false}); err != nil {
		t.Fatalf("update c1: %v", err)
	}

	entries, err := readJSONLFile[settlementJournalEntry](store.settlementJournalPath)
	if err != nil {
		t.Fatalf("读事实源: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("事实源应有 3 条(append/append/update,append-only), got %d", len(entries))
	}
	rebuilt := materializeSettlementsFromJournal(entries)
	if len(rebuilt) != 2 {
		t.Fatalf("回放视图应为 2 条, got %d", len(rebuilt))
	}
	// 回放出的 c1 应为定稿态(NetPnL=1.5)。
	var c1 *SettlementCSVRecord
	for i := range rebuilt {
		if rebuilt[i].CycleID == "c1" {
			c1 = &rebuilt[i]
		}
	}
	if c1 == nil || c1.NetPnL != "1.5" || c1.FundingIncomePending {
		t.Fatalf("回放 c1 应为定稿态, got %+v", c1)
	}
}

// TestPendingFundingJournalAppendOnly 端到端:upsert + delete 后事实源条目数累加(永不重写),回放视图正确。
func TestPendingFundingJournalAppendOnly(t *testing.T) {
	store, err := NewReportStore(t.TempDir())
	if err != nil {
		t.Fatalf("建 store: %v", err)
	}
	_ = store.UpsertPendingFundingSettlement(PendingFundingSettlementRecord{RunID: "r", CycleID: "c1", AttemptCount: 1})
	_ = store.UpsertPendingFundingSettlement(PendingFundingSettlementRecord{RunID: "r", CycleID: "c1", AttemptCount: 2})
	_ = store.DeletePendingFundingSettlement("r", "c1")

	entries, err := readJSONLFile[pendingFundingJournalEntry](store.pendingFundingJournalPath)
	if err != nil {
		t.Fatalf("读事实源: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("事实源应 append-only 累加 3 条, got %d", len(entries))
	}
	if rebuilt := materializePendingFromJournal(entries); len(rebuilt) != 0 {
		t.Fatalf("c1 已删,回放视图应为空, got %d", len(rebuilt))
	}
}
