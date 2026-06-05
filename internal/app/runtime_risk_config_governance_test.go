package app

import (
	"context"
	"testing"
)

// TestDiffRuntimeRiskConfig 钉死字段级 diff:只返回变化字段,含 before/after。
func TestDiffRuntimeRiskConfig(t *testing.T) {
	before := RuntimeRiskConfig{PerSymbolQuoteUSDT: 200, MaxCycleLossUSDT: 50, MaxDailyOrderCount: 40}
	after := RuntimeRiskConfig{PerSymbolQuoteUSDT: 100, MaxCycleLossUSDT: 50, MaxDailyOrderCount: 30}
	diffs := diffRuntimeRiskConfig(before, after)
	if len(diffs) != 2 {
		t.Fatalf("应有 2 个变化字段, got %d: %+v", len(diffs), diffs)
	}
	byField := map[string]RuntimeConfigFieldDiff{}
	for _, d := range diffs {
		byField[d.Field] = d
	}
	if d, ok := byField["PerSymbolQuoteUSDT"]; !ok || d.Before != "200" || d.After != "100" {
		t.Fatalf("PerSymbolQuoteUSDT diff 不正确: %+v", byField)
	}
	if _, ok := byField["MaxCycleLossUSDT"]; ok {
		t.Fatal("未变化字段不应出现在 diff 中")
	}
	if len(diffRuntimeRiskConfig(before, before)) != 0 {
		t.Fatal("相同配置应无 diff")
	}
}

// newTestController 构造一个最小可用控制器(仅测改参治理路径)。
func newTestController(t *testing.T) *RuntimeController {
	t.Helper()
	store, err := NewReportStore(t.TempDir())
	if err != nil {
		t.Fatalf("建 store: %v", err)
	}
	c, err := NewRuntimeController(context.Background(), store, DefaultEngineConfig(), nil)
	if err != nil {
		t.Fatalf("建 controller: %v", err)
	}
	return c
}

// TestPreviewDoesNotApply 钉死预览只读:返回 diff 但不改变当前配置。
func TestPreviewDoesNotApply(t *testing.T) {
	c := newTestController(t)
	current := c.CurrentRiskConfig()
	proposed := current
	proposed.MaxCycleLossUSDT = current.MaxCycleLossUSDT / 2 // 收紧(合法)

	diffs, err := c.PreviewRiskConfigUpdate(proposed)
	if err != nil {
		t.Fatalf("预览应通过: %v", err)
	}
	if len(diffs) == 0 {
		t.Fatal("应有 diff")
	}
	if c.CurrentRiskConfig().MaxCycleLossUSDT != current.MaxCycleLossUSDT {
		t.Fatal("预览不得改变当前配置")
	}
}

// TestUpdateThenRollback 钉死改参后可一键回滚到改前;无历史时回滚报错。
func TestUpdateThenRollback(t *testing.T) {
	c := newTestController(t)
	original := c.CurrentRiskConfig()

	// 无历史 → 回滚报错。
	if _, err := c.RollbackRiskConfig(); err == nil {
		t.Fatal("无历史时回滚应报错")
	}

	// 收紧一次。
	proposed := original
	proposed.MaxCycleLossUSDT = original.MaxCycleLossUSDT / 2
	if _, err := c.UpdateRiskConfig(proposed); err != nil {
		t.Fatalf("收紧更新应通过: %v", err)
	}
	if c.CurrentRiskConfig().MaxCycleLossUSDT != proposed.MaxCycleLossUSDT {
		t.Fatal("更新未生效")
	}

	// 回滚 → 恢复原值(回滚可放松,不受 only-tighten 约束)。
	rolledBack, err := c.RollbackRiskConfig()
	if err != nil {
		t.Fatalf("回滚应通过: %v", err)
	}
	if rolledBack.MaxCycleLossUSDT != original.MaxCycleLossUSDT {
		t.Fatalf("回滚应恢复原值 %.4f, got %.4f", original.MaxCycleLossUSDT, rolledBack.MaxCycleLossUSDT)
	}
	if c.CurrentRiskConfig().MaxCycleLossUSDT != original.MaxCycleLossUSDT {
		t.Fatal("回滚未落地到当前配置")
	}
}
