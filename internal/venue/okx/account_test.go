package okx

import (
	"context"
	"testing"
)

// TestVerifyAPIKeyPosturePermParsing 钉死 perm 字段的真实解析:带提现权限的 key 是高危配置,
// 安全自检必须如实上报 CanWithdraw,硬编码 false 会把真实风险盖住。
func TestVerifyAPIKeyPosturePermParsing(t *testing.T) {
	cases := []struct {
		name             string
		perm             string
		ip               string
		wantTrade        bool
		wantWithdraw     bool
		wantIPRestricted bool
	}{
		{"只读+交易(期望姿态)", "read_only,trade", "1.2.3.4", true, false, true},
		{"含提现(高危,必须暴露)", "read_only,trade,withdraw", "", true, true, false},
		{"纯只读", "read_only", "", false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := fakeOKX(t, map[string]string{
				pathAccountConfig: `{"code":"0","msg":"","data":[{"acctLv":"2","posMode":"net_mode","perm":"` + tc.perm + `","ip":"` + tc.ip + `"}]}`,
			})
			kp, err := a.VerifyAPIKeyPosture(context.Background())
			if err != nil {
				t.Fatalf("VerifyAPIKeyPosture: %v", err)
			}
			if kp.CanTrade != tc.wantTrade || kp.CanWithdraw != tc.wantWithdraw || kp.IPRestricted != tc.wantIPRestricted {
				t.Errorf("姿态解析错误: got trade=%t withdraw=%t ipRestricted=%t, want %t/%t/%t",
					kp.CanTrade, kp.CanWithdraw, kp.IPRestricted, tc.wantTrade, tc.wantWithdraw, tc.wantIPRestricted)
			}
			if kp.Notes == "" {
				t.Error("Notes 应含原始 perm 串")
			}
		})
	}
}
