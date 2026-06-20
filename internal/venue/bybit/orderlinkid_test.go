package bybit

import "testing"

func TestBybitOrderLinkIDWithinLimitUnchanged(t *testing.T) {
	id := "svsp-flat-MNT-USDT-123"
	if got := bybitOrderLinkID(id); got != id {
		t.Fatalf("≤36 应原样返回, got %q want %q", got, id)
	}
	// 恰好 36 字符边界。
	exact := "abcdefghij-abcdefghij-abcdefghij-123"
	if len(exact) != 36 {
		t.Fatalf("测试常量长度应为 36, got %d", len(exact))
	}
	if got := bybitOrderLinkID(exact); got != exact {
		t.Fatalf("36 字符边界应原样返回, got %q", got)
	}
}

func TestBybitOrderLinkIDTruncatesDeterministically(t *testing.T) {
	long := "svsp-reconcile-flat-MNT-USDT-1781950201488292781" // 48 字符,实测触发 170003
	if len(long) <= bybitMaxOrderLinkIDLen {
		t.Fatalf("用例需 >36, got %d", len(long))
	}
	got := bybitOrderLinkID(long)
	if len(got) != bybitMaxOrderLinkIDLen {
		t.Fatalf("收敛后应为 36 字符, got %d (%q)", len(got), got)
	}
	// 确定性:同输入同输出(place/query/cancel 三处必须一致)。
	if again := bybitOrderLinkID(long); again != got {
		t.Fatalf("必须确定性: %q != %q", got, again)
	}
	// 唯一性:不同输入不同输出。
	other := "svsp-reconcile-flat-MNT-USDT-1781950201488292782"
	if bybitOrderLinkID(other) == got {
		t.Fatalf("不同 clientID 收敛后不应相同")
	}
}
