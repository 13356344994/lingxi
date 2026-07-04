package app

import (
	"testing"
	"time"
)

// TestFastHotWSDesired 钉死动态订阅差分:最近热优先补位、粘性保留、超期退订、上限封顶。
func TestFastHotWSDesired(t *testing.T) {
	now := time.Now().UTC()
	hot := map[string]time.Time{
		"A-USDT": now.Add(-5 * time.Second),   // 最热
		"B-USDT": now.Add(-60 * time.Second),  // 次热
		"C-USDT": now.Add(-300 * time.Second), // 仍在粘性期
		"D-USDT": now.Add(-700 * time.Second), // 超期(>600s)
	}
	// 空订阅集:按最近热补位,超期不进,上限=2 → 只取 A、B
	subs, unsubs := fastHotWSDesired(hot, map[string]bool{}, now, 2, 600)
	if len(subs) != 2 || subs[0] != "A-USDT" || subs[1] != "B-USDT" || len(unsubs) != 0 {
		t.Fatalf("补位错: subs=%v unsubs=%v", subs, unsubs)
	}
	// 已订阅 C(粘性内)与 D(超期):C 保留占位,D 退订;上限=2 → 只补 A
	subs2, unsubs2 := fastHotWSDesired(hot, map[string]bool{"C-USDT": true, "D-USDT": true}, now, 2, 600)
	if len(unsubs2) != 1 || unsubs2[0] != "D-USDT" {
		t.Fatalf("超期退订错: %v", unsubs2)
	}
	if len(subs2) != 1 || subs2[0] != "A-USDT" {
		t.Fatalf("粘性占位后补位错: %v", subs2)
	}
	// 满员且全新鲜:无变化
	subs3, unsubs3 := fastHotWSDesired(hot, map[string]bool{"A-USDT": true, "B-USDT": true}, now, 2, 600)
	if len(subs3) != 0 || len(unsubs3) != 0 {
		t.Fatalf("满员应无差分: subs=%v unsubs=%v", subs3, unsubs3)
	}
	// 热度表为空:全部退订
	_, unsubs4 := fastHotWSDesired(map[string]time.Time{}, map[string]bool{"A-USDT": true}, now, 2, 600)
	if len(unsubs4) != 1 || unsubs4[0] != "A-USDT" {
		t.Fatalf("空热度表应全退订: %v", unsubs4)
	}
}
