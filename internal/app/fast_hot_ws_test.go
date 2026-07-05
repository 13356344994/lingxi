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
	subs, unsubs := fastHotWSDesired(hot, map[string]bool{}, now, 2, 600, nil)
	if len(subs) != 2 || subs[0] != "A-USDT" || subs[1] != "B-USDT" || len(unsubs) != 0 {
		t.Fatalf("补位错: subs=%v unsubs=%v", subs, unsubs)
	}
	// 已订阅 C(粘性内)与 D(超期):C 保留占位,D 退订;上限=2 → 只补 A
	subs2, unsubs2 := fastHotWSDesired(hot, map[string]bool{"C-USDT": true, "D-USDT": true}, now, 2, 600, nil)
	if len(unsubs2) != 1 || unsubs2[0] != "D-USDT" {
		t.Fatalf("超期退订错: %v", unsubs2)
	}
	if len(subs2) != 1 || subs2[0] != "A-USDT" {
		t.Fatalf("粘性占位后补位错: %v", subs2)
	}
	// 满员且全新鲜:无变化
	subs3, unsubs3 := fastHotWSDesired(hot, map[string]bool{"A-USDT": true, "B-USDT": true}, now, 2, 600, nil)
	if len(subs3) != 0 || len(unsubs3) != 0 {
		t.Fatalf("满员应无差分: subs=%v unsubs=%v", subs3, unsubs3)
	}
	// 热度表为空:全部退订
	_, unsubs4 := fastHotWSDesired(map[string]time.Time{}, map[string]bool{"A-USDT": true}, now, 2, 600, nil)
	if len(unsubs4) != 1 || unsubs4[0] != "A-USDT" {
		t.Fatalf("空热度表应全退订: %v", unsubs4)
	}
}

// TestFastHotWSMonsterPriority 钉死 v6.1 怪兽坑位优先:满员时 30min 暴动≥5 的候选驱逐
// 暴动少≥3 的在订者;迟滞(差<3)不动;暴动<5 永不驱逐;riotN=nil 走旧行为。
func TestFastHotWSMonsterPriority(t *testing.T) {
	now := time.Now().UTC()
	hot := map[string]time.Time{
		"OLD1-USDT": now.Add(-500 * time.Second), // 在订,不暴动
		"OLD2-USDT": now.Add(-400 * time.Second), // 在订,暴动 4 次
		"MON-USDT":  now.Add(-10 * time.Second),  // 候选,怪兽(暴动 9)
	}
	cur := map[string]bool{"OLD1-USDT": true, "OLD2-USDT": true}
	riot := map[string]int{"OLD1-USDT": 0, "OLD2-USDT": 4, "MON-USDT": 9}
	// 满员(maxN=2):MON 应驱逐最弱的 OLD1(9-0≥3)
	subs, unsubs := fastHotWSDesired(hot, cur, now, 2, 600, riot)
	if len(subs) != 1 || subs[0] != "MON-USDT" || len(unsubs) != 1 || unsubs[0] != "OLD1-USDT" {
		t.Fatalf("怪兽应驱逐最弱在订者: subs=%v unsubs=%v", subs, unsubs)
	}
	// 迟滞:候选暴动 6 vs 最弱在订 4,差 2<3 → 不动
	riot2 := map[string]int{"OLD1-USDT": 4, "OLD2-USDT": 5, "MON-USDT": 6}
	subs2, unsubs2 := fastHotWSDesired(hot, cur, now, 2, 600, riot2)
	if len(subs2) != 0 || len(unsubs2) != 0 {
		t.Fatalf("迟滞内应无差分: subs=%v unsubs=%v", subs2, unsubs2)
	}
	// 候选暴动 <5:即使在订者为 0 也不驱逐
	riot3 := map[string]int{"OLD1-USDT": 0, "OLD2-USDT": 0, "MON-USDT": 4}
	subs3, unsubs3 := fastHotWSDesired(hot, cur, now, 2, 600, riot3)
	if len(subs3) != 0 || len(unsubs3) != 0 {
		t.Fatalf("暴动<5 不够格驱逐: subs=%v unsubs=%v", subs3, unsubs3)
	}
	// riotN=nil(开关关):满员无差分=旧行为
	subs4, unsubs4 := fastHotWSDesired(hot, cur, now, 2, 600, nil)
	if len(subs4) != 0 || len(unsubs4) != 0 {
		t.Fatalf("nil riotN 应走旧行为: subs=%v unsubs=%v", subs4, unsubs4)
	}
	// 有空位时优先正常补位(不触发驱逐路径)
	subs5, unsubs5 := fastHotWSDesired(hot, cur, now, 3, 600, riot)
	if len(subs5) != 1 || subs5[0] != "MON-USDT" || len(unsubs5) != 0 {
		t.Fatalf("有空位应正常补位: subs=%v unsubs=%v", subs5, unsubs5)
	}
}
