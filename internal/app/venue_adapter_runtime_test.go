package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lingxi/internal/venue/gate"
	"lingxi/internal/venue/okx"
)

// TestWireVenueAdapterMissingCredentials 凭证缺失时 wire 失败,启动隔离可跳过该所(不 panic)。
func TestWireVenueAdapterMissingCredentials(t *testing.T) {
	rt := &venueRuntime{name: okx.VenueName}
	err := wireVenueAdapter(rt, filepath.Join(t.TempDir(), "no-secret.yaml"), nil)
	if err == nil {
		t.Fatal("缺凭证应返回 error")
	}
	if !strings.Contains(err.Error(), "凭证/适配器") {
		t.Fatalf("错误应说明凭证/适配器原因: %v", err)
	}
	if rt.venueAdapter != nil {
		t.Fatal("失败时不应注入 venueAdapter")
	}
}

// TestWireVenueAdapterOkxGateSuccess 凭证齐全时 okx/gate 适配器成功挂到 venueRuntime。
func TestWireVenueAdapterOkxGateSuccess(t *testing.T) {
	dir := t.TempDir()
	secretPath := filepath.Join(dir, "secret.yaml")
	content := `okx:
  api: OKX_KEY
  secret: OKX_SEC
  passphrase: OKX_PASS
gate:
  api: GATE_KEY
  secret: GATE_SEC
`
	if err := os.WriteFile(secretPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{okx.VenueName, gate.VenueName} {
		rt := &venueRuntime{name: name}
		if err := wireVenueAdapter(rt, secretPath, nil); err != nil {
			t.Fatalf("venue=%s wireVenueAdapter() error = %v", name, err)
		}
		if rt.venueAdapter == nil {
			t.Fatalf("venue=%s venueAdapter 应为非 nil", name)
		}
		if rt.venueAdapter.Name() != name {
			t.Fatalf("venue=%s adapter.Name() = %q", name, rt.venueAdapter.Name())
		}
	}
}

// TestWireVenueAdapterBinanceBybitNoOp 币安/bybit 不注入 venueAdapter(行为与改造前一致)。
func TestWireVenueAdapterBinanceBybitNoOp(t *testing.T) {
	for _, name := range []string{"binance", "bybit"} {
		rt := &venueRuntime{name: name}
		if err := wireVenueAdapter(rt, "secret.yaml", nil); err != nil {
			t.Fatalf("venue=%s 不应报错: %v", name, err)
		}
		if rt.venueAdapter != nil {
			t.Fatalf("venue=%s 不应注入 venueAdapter", name)
		}
	}
}

// TestRuntimeControllerStartRejectsOkxFundingVenueAfterDeWire 单所 funding 引擎已退役(de-wire 2026-06-17):
// okx Start(live) 应被拒绝(实盘统一经跨所执行器),不再经 per-venue 控制器启动单所引擎。
func TestRuntimeControllerStartRejectsOkxFundingVenueAfterDeWire(t *testing.T) {
	dir := t.TempDir()
	store, err := NewReportStore(dir)
	if err != nil {
		t.Fatalf("NewReportStore: %v", err)
	}
	c, err := NewRuntimeController(context.Background(), store, DefaultEngineConfig(), nil)
	if err != nil {
		t.Fatalf("NewRuntimeController: %v", err)
	}
	c.venueName = okx.VenueName
	err = c.Start(RuntimeStartRequest{Mode: string(RuntimeModeLive), SecretPath: "secret.yaml"})
	if err == nil {
		t.Fatal("单所引擎已退役,okx Start(live) 应被拒绝")
	}
	if !strings.Contains(err.Error(), "已退役") {
		t.Fatalf("拒绝原因应说明单所引擎已退役: %v", err)
	}
	if c.Status().Running {
		t.Fatal("拒绝后不应有后台任务在运行")
	}
}

// TestRuntimeControllerStartRejectsGateFundingVenueAfterDeWire gate 与 okx 同路径(单所引擎已退役)。
func TestRuntimeControllerStartRejectsGateFundingVenueAfterDeWire(t *testing.T) {
	dir := t.TempDir()
	store, err := NewReportStore(dir)
	if err != nil {
		t.Fatalf("NewReportStore: %v", err)
	}
	c, err := NewRuntimeController(context.Background(), store, DefaultEngineConfig(), nil)
	if err != nil {
		t.Fatalf("NewRuntimeController: %v", err)
	}
	c.venueName = gate.VenueName
	err = c.Start(RuntimeStartRequest{Mode: string(RuntimeModeLive), SecretPath: "secret.yaml"})
	if err == nil {
		t.Fatal("单所引擎已退役,gate Start(live) 应被拒绝")
	}
	if !strings.Contains(err.Error(), "已退役") {
		t.Fatalf("拒绝原因应说明单所引擎已退役: %v", err)
	}
	if c.Status().Running {
		t.Fatal("拒绝后不应有后台任务在运行")
	}
}
