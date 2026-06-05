package app

import (
	"testing"
	"time"
)

// TestShouldEscalatePendingFunding 钉死 R34 升级判定:重试达上限或挂起超时即升级;阈值=0 关闭该维度。
func TestShouldEscalatePendingFunding(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	recorded := now.Add(-2 * time.Hour)

	// 未达任何阈值 → 不升级。
	if esc, _ := shouldEscalatePendingFunding(5, recorded, now, 30, 12*time.Hour); esc {
		t.Fatal("未达阈值不应升级")
	}
	// 重试达上限 → 升级。
	if esc, reason := shouldEscalatePendingFunding(30, recorded, now, 30, 12*time.Hour); !esc || reason == "" {
		t.Fatalf("重试达上限应升级, esc=%v reason=%q", esc, reason)
	}
	// 挂起超时 → 升级。
	if esc, reason := shouldEscalatePendingFunding(1, now.Add(-13*time.Hour), now, 30, 12*time.Hour); !esc || reason == "" {
		t.Fatalf("挂起超时应升级, esc=%v reason=%q", esc, reason)
	}
	// 阈值=0 关闭:即便重试很多/年龄很大也不升级。
	if esc, _ := shouldEscalatePendingFunding(1000, now.Add(-100*time.Hour), now, 0, 0); esc {
		t.Fatal("阈值=0 应关闭升级维度")
	}
	// recordedAt 为零值时年龄维度不生效。
	if esc, _ := shouldEscalatePendingFunding(1, time.Time{}, now, 0, 12*time.Hour); esc {
		t.Fatal("recordedAt 零值时年龄维度不应触发")
	}
}
