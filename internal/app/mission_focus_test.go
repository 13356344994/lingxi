package app

import (
	"strings"
	"testing"
)

// TestMissionTodayFocus 今日焦点纯函数:需要拍板>判负善后>最近裁决点>默认。
func TestMissionTodayFocus(t *testing.T) {
	// 门票过检 → 等用户拍板(最高优先)
	if s := missionTodayFocus(true, true, 35, 5.0, 1.5, 3, 120, 30); !strings.Contains(s, "等你拍板") {
		t.Fatalf("门票过检应提示拍板,got %q", s)
	}
	// 门票判负 → 回炉说明 + 最近裁决点
	s := missionTodayFocus(true, false, 38, -9.1, 0.44, 0.5, 40, 10)
	if !strings.Contains(s, "判负") || !strings.Contains(s, "回炉") {
		t.Fatalf("判负应如实说明,got %q", s)
	}
	if !strings.Contains(s, "maker") {
		t.Fatalf("应给出最近裁决点,got %q", s)
	}
	// 全部完成且无待办 → 无需操作
	if s := missionTodayFocus(false, false, 30, 0, 0, 5, 200, 48); !strings.Contains(s, "没有需要你做的事") {
		t.Fatalf("无待办应明确说无需操作,got %q", s)
	}
}
