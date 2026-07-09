package app

import (
	"testing"
	"time"
)

// TestPulseGroupsFromSamples 脉搏纯函数:按键分组 + 最近 window 笔滚动中位 + 热冷标记 + 排序。
func TestPulseGroupsFromSamples(t *testing.T) {
	base := time.Date(2026, 7, 9, 0, 0, 0, 0, time.UTC)
	var samples []pulseSample
	// hot 组:35 笔,前 5 笔大亏(应被滚动窗剔除),后 30 笔为 +1..+30 → 窗口中位 15.5
	for i := 0; i < 5; i++ {
		samples = append(samples, pulseSample{Key: "e|rest3", Time: base.Add(time.Duration(i) * time.Minute), Net: -100})
	}
	for i := 1; i <= 30; i++ {
		samples = append(samples, pulseSample{Key: "e|rest3", Time: base.Add(time.Duration(5+i) * time.Minute), Net: float64(i)})
	}
	// cold 组:3 笔(不足窗口),乱序输入,中位 -4
	samples = append(samples,
		pulseSample{Key: "fire|rest", Time: base.Add(9 * time.Minute), Net: -4},
		pulseSample{Key: "fire|rest", Time: base.Add(3 * time.Minute), Net: -10},
		pulseSample{Key: "fire|rest", Time: base.Add(6 * time.Minute), Net: +2},
	)

	groups := pulseGroupsFromSamples(samples, 30)
	if len(groups) != 2 {
		t.Fatalf("应 2 组,得 %d", len(groups))
	}
	// 排序:中位降序 → hot 组在前
	hot, cold := groups[0], groups[1]
	if hot.Key != "e|rest3" || cold.Key != "fire|rest" {
		t.Fatalf("排序错: %s / %s", hot.Key, cold.Key)
	}
	if hot.N != 30 || hot.TotalN != 35 {
		t.Fatalf("hot 窗口/全量笔数应 30/35,得 %d/%d", hot.N, hot.TotalN)
	}
	if hot.MedNet != 15.5 {
		t.Fatalf("hot 滚动中位应 15.5(前 5 笔大亏须被窗口剔除),得 %.2f", hot.MedNet)
	}
	if !hot.Hot {
		t.Fatal("中位>0 应标热")
	}
	if !hot.LastTime.Equal(base.Add(35 * time.Minute)) {
		t.Fatalf("hot 最后一笔时间错: %v", hot.LastTime)
	}
	if cold.N != 3 || cold.TotalN != 3 {
		t.Fatalf("cold 笔数应 3/3,得 %d/%d", cold.N, cold.TotalN)
	}
	if cold.MedNet != -4 {
		t.Fatalf("cold 中位应 -4(按时间序与窗口无关),得 %.2f", cold.MedNet)
	}
	if cold.Hot {
		t.Fatal("中位<0 不应标热")
	}
	// 乱序输入按时间排序后,最后一笔=9min 那笔
	if !cold.LastTime.Equal(base.Add(9 * time.Minute)) {
		t.Fatalf("cold 最后一笔时间错: %v", cold.LastTime)
	}

	if got := pulseGroupsFromSamples(nil, 30); len(got) != 0 {
		t.Fatalf("空输入应空输出,得 %d", len(got))
	}
}
