package app

import (
	"encoding/json"
	"strings"
	"testing"
)

// Test_fastUpdateExcursion_SeenAware 钉死 seen-aware 采样:首次采样某窗口时 MFE=MAE=ubps(不被 0 污染);
// 之后 max/min;窗口外不采样(seen 保持 false)。
func Test_fastUpdateExcursion_SeenAware(t *testing.T) {
	// 首采样为负(-7):所有窗口 seen 且 MFE=MAE=-7(不再记成假 0)。
	p := &fastLivePosition{}
	fastUpdateExcursion(p, 2, -7)
	type w struct {
		mfe, mae float64
		seen     bool
		name     string
	}
	for _, x := range []w{
		{p.Mfe3, p.Mae3, p.Seen3, "3"}, {p.Mfe5, p.Mae5, p.Seen5, "5"}, {p.Mfe10, p.Mae10, p.Seen10, "10"},
		{p.Mfe15, p.Mae15, p.Seen15, "15"}, {p.Mfe30, p.Mae30, p.Seen30, "30"}, {p.Mfe60, p.Mae60, p.Seen60, "60"},
	} {
		if !x.seen || x.mfe != -7 || x.mae != -7 {
			t.Fatalf("首采样-7: 窗口%s 期望 seen且MFE=MAE=-7, got mfe=%v mae=%v seen=%v", x.name, x.mfe, x.mae, x.seen)
		}
	}

	// 首采样为正(+9):MFE=MAE=+9。
	p2 := &fastLivePosition{}
	fastUpdateExcursion(p2, 2, 9)
	if !p2.Seen3 || p2.Mfe3 != 9 || p2.Mae3 != 9 {
		t.Fatalf("首采样+9: 期望 MFE=MAE=9, got mfe=%v mae=%v seen=%v", p2.Mfe3, p2.Mae3, p2.Seen3)
	}

	// 窗口外不采样:hold=4s → 3s 窗口不采样(seen3=false),5s 窗口采样。
	p3 := &fastLivePosition{}
	fastUpdateExcursion(p3, 4, 5)
	if p3.Seen3 {
		t.Fatalf("hold=4s 不应采样 3s 窗口(seen3 应 false)")
	}
	if !p3.Seen5 || p3.Mfe5 != 5 || p3.Mae5 != 5 {
		t.Fatalf("hold=4s 应采样 5s 窗口首值=5, got seen5=%v mfe5=%v mae5=%v", p3.Seen5, p3.Mfe5, p3.Mae5)
	}

	// 后续 max/min:先 +3 再 -2(同 3s 窗口)→ MFE=3, MAE=-2。
	p4 := &fastLivePosition{}
	fastUpdateExcursion(p4, 1, 3)
	fastUpdateExcursion(p4, 2, -2)
	if p4.Mfe3 != 3 || p4.Mae3 != -2 {
		t.Fatalf("max/min 更新错误 mfe3=%v mae3=%v want 3/-2", p4.Mfe3, p4.Mae3)
	}
}

// Test_settlement_secslice_json_pointer 钉死落盘规则:采样窗口字段出现(含采样到0);未采样窗口不出现。
func Test_settlement_secslice_json_pointer(t *testing.T) {
	// 仅采样到 3s(含 mae 负值),5s/10s 未采样(nil)。
	mfe3, mae3 := 4.0, -1.0
	s := okxDirectionalSettlement{PostFillMfe3sBps: &mfe3, PostFillMae3sBps: &mae3}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	js := string(b)
	if !strings.Contains(js, "post_fill_mfe_3s_bps") || !strings.Contains(js, "post_fill_mae_3s_bps") {
		t.Fatalf("已采样 3s 字段应出现: %s", js)
	}
	for _, k := range []string{"post_fill_mfe_5s_bps", "post_fill_mfe_10s_bps", "post_fill_mae_5s_bps", "post_fill_mae_10s_bps"} {
		if strings.Contains(js, k) {
			t.Fatalf("未采样窗口字段不应出现: %s in %s", k, js)
		}
	}

	// 采样到 0 的窗口:字段仍应出现(指针非 nil,区分"未采样")。
	zero := 0.0
	s2 := okxDirectionalSettlement{PostFillMfe5sBps: &zero}
	b2, _ := json.Marshal(s2)
	if !strings.Contains(string(b2), "post_fill_mfe_5s_bps") {
		t.Fatalf("采样到 0 的窗口字段仍应出现(指针非 nil): %s", string(b2))
	}
}
