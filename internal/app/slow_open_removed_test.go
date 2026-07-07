package app

import (
	"os"
	"strings"
	"testing"
)

// TestSlowOpenPathRemoved 护栏(2026-07-07 用户指令"慢线全部停掉并删除逻辑"):
// 慢线开仓调用点(runOnce → openFromShortlist)必须保持摘除状态——历史累计≈-52U、
// 从未验证出正 edge。恢复该调用=撤销用户决策,须用户明示。
// manage/universe 扫描/fast 层不在此约束内(fast_ws 订阅依赖 shortlist)。
func TestSlowOpenPathRemoved(t *testing.T) {
	src, err := os.ReadFile("okx_directional_engine.go")
	if err != nil {
		t.Fatalf("读引擎源码失败: %v", err)
	}
	if n := strings.Count(string(src), "e.openFromShortlist("); n != 0 {
		t.Fatalf("慢线开仓调用点被恢复(出现 %d 次)——2026-07-07 用户指令摘除,回退须用户明示", n)
	}
}
