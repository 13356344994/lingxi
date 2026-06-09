package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBybitPositionStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store, err := newBybitPositionStore(dir)
	if err != nil {
		t.Fatalf("建仓位台账失败: %v", err)
	}
	if ps, err := store.load(); err != nil || len(ps) != 0 {
		t.Fatalf("空台账应返回空, got %v err=%v", ps, err)
	}

	p1 := BybitLivePosition{Symbol: "HOME-USDT", BaseQty: 10, NextFundingTime: time.Now().UTC(), IdemPrefix: "lx-1"}
	p2 := BybitLivePosition{Symbol: "EDEN-USDT", BaseQty: 5, NextFundingTime: time.Now().UTC(), IdemPrefix: "lx-2"}
	if err := store.upsert(p1); err != nil {
		t.Fatalf("upsert p1: %v", err)
	}
	if err := store.upsert(p2); err != nil {
		t.Fatalf("upsert p2: %v", err)
	}
	// 重复 upsert 同 symbol 应覆盖而非新增。
	p1.BaseQty = 20
	if err := store.upsert(p1); err != nil {
		t.Fatalf("upsert p1 again: %v", err)
	}
	ps, err := store.load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(ps) != 2 {
		t.Fatalf("应有 2 笔持仓, got %d", len(ps))
	}
	var homeQty float64
	for _, p := range ps {
		if p.Symbol == "HOME-USDT" {
			homeQty = p.BaseQty
		}
	}
	if homeQty != 20 {
		t.Fatalf("HOME 数量应被覆盖为 20, got %v", homeQty)
	}

	if err := store.remove("HOME-USDT"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	ps, _ = store.load()
	if len(ps) != 1 || ps[0].Symbol != "EDEN-USDT" {
		t.Fatalf("移除 HOME 后应只剩 EDEN, got %+v", ps)
	}

	// 持久化:新实例从同目录加载应见到 EDEN。
	store2, err := newBybitPositionStore(dir)
	if err != nil {
		t.Fatalf("重开台账: %v", err)
	}
	ps, _ = store2.load()
	if len(ps) != 1 || ps[0].Symbol != "EDEN-USDT" {
		t.Fatalf("跨实例加载应保留 EDEN, got %+v", ps)
	}
}

// TestBybitPositionStoreWritesEnvelope 钉死 R18:新格式落盘带 schema_version + checksum 信封(非裸数组)。
func TestBybitPositionStoreWritesEnvelope(t *testing.T) {
	dir := t.TempDir()
	store, _ := newBybitPositionStore(dir)
	if err := store.upsert(BybitLivePosition{Symbol: "HOME-USDT", BaseQty: 10, IdemPrefix: "lx-1"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, bybitPositionsFileName))
	if err != nil {
		t.Fatalf("读文件: %v", err)
	}
	content := string(raw)
	if !strings.Contains(content, "\"schema_version\"") || !strings.Contains(content, "\"checksum\"") || !strings.Contains(content, "\"positions\"") {
		t.Fatalf("落盘应为带信封头的对象,实际:\n%s", content)
	}
}

// TestBybitPositionStoreLegacyBareArrayPassthrough 钉死向后兼容:旧版本写的裸 JSON 数组仍能读出。
func TestBybitPositionStoreLegacyBareArrayPassthrough(t *testing.T) {
	dir := t.TempDir()
	legacy := `[{"symbol":"EDEN-USDT","base_qty":7,"idem_prefix":"lx-legacy"}]`
	if err := os.WriteFile(filepath.Join(dir, bybitPositionsFileName), []byte(legacy), 0o600); err != nil {
		t.Fatalf("写 legacy 文件: %v", err)
	}
	store, _ := newBybitPositionStore(dir)
	ps, err := store.load()
	if err != nil {
		t.Fatalf("读 legacy 裸数组应放行,却报错: %v", err)
	}
	if len(ps) != 1 || ps[0].Symbol != "EDEN-USDT" || ps[0].BaseQty != 7 {
		t.Fatalf("legacy 加载结果不对: %+v", ps)
	}
}

// TestBybitPositionStoreRejectsTampered 钉死完整性:信封内容被外部篡改后,load 拒绝放行(不在不可信状态上自动接管)。
func TestBybitPositionStoreRejectsTampered(t *testing.T) {
	dir := t.TempDir()
	store, _ := newBybitPositionStore(dir)
	if err := store.upsert(BybitLivePosition{Symbol: "HOME-USDT", BaseQty: 10, IdemPrefix: "lx-1"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	path := filepath.Join(dir, bybitPositionsFileName)
	raw, _ := os.ReadFile(path)
	// 篡改持仓数量(checksum 不再匹配),但保留信封结构。
	tampered := strings.Replace(string(raw), "\"base_qty\": 10", "\"base_qty\": 9999", 1)
	if tampered == string(raw) {
		t.Fatal("测试前置:未找到可篡改的字段")
	}
	if err := os.WriteFile(path, []byte(tampered), 0o600); err != nil {
		t.Fatalf("写篡改文件: %v", err)
	}
	if _, err := store.load(); err == nil {
		t.Fatal("篡改后的台账应被拒绝(checksum 不匹配),却放行了")
	}
}

func TestBybitIdemPrefixStableAcrossCalls(t *testing.T) {
	ft := time.Date(2026, 6, 7, 16, 0, 0, 0, time.UTC)
	a := bybitIdemPrefix("HOME-USDT", ft)
	b := bybitIdemPrefix("HOME-USDT", ft)
	if a != b {
		t.Fatalf("同 symbol+结算时刻幂等前缀应稳定: %s vs %s", a, b)
	}
	if c := bybitIdemPrefix("HOME-USDT", ft.Add(time.Hour)); c == a {
		t.Fatalf("不同结算时刻前缀应不同")
	}
}
