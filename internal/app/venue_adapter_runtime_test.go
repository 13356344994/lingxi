package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lingxi/internal/venue"
	"lingxi/internal/venue/gate"
	"lingxi/internal/venue/okx"
)

// waitForRuntimeControllerStopped 在 Stop 之后轮询直至后台 runTask goroutine 完全退出,
// 避免其仍向 t.TempDir 写运行日志/领域事件时触发 RemoveAll「directory not empty」。
func waitForRuntimeControllerStopped(t *testing.T, c *RuntimeController) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for c.Status().Running && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if c.Status().Running {
		t.Fatal("后台任务停止超时,仍在 running")
	}
}

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

// TestRuntimeControllerStartAcceptsOkxFundingVenue okx Start 应启动 funding 执行器(不再「尚未接入」拒绝)。
func TestRuntimeControllerStartAcceptsOkxFundingVenue(t *testing.T) {
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
	orig := venueFundingVenueFactory
	venueFundingVenueFactory = func(_, _ string) (venue.Venue, error) {
		return &fakeVenueFundingTrader{}, nil
	}
	defer func() { venueFundingVenueFactory = orig }()
	err = c.Start(RuntimeStartRequest{Mode: string(RuntimeModeLive), SecretPath: "secret.yaml"})
	if err != nil {
		t.Fatalf("okx Start 应接受 funding 引擎: %v", err)
	}
	if err := c.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	waitForRuntimeControllerStopped(t, c)
}

// TestRuntimeControllerStartAcceptsGateFundingVenue gate 与 okx 同路径。
func TestRuntimeControllerStartAcceptsGateFundingVenue(t *testing.T) {
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
	orig := venueFundingVenueFactory
	venueFundingVenueFactory = func(_, _ string) (venue.Venue, error) {
		return &fakeVenueFundingTrader{}, nil
	}
	defer func() { venueFundingVenueFactory = orig }()
	err = c.Start(RuntimeStartRequest{Mode: string(RuntimeModeLive), SecretPath: "secret.yaml"})
	if err != nil {
		t.Fatalf("gate Start 应接受 funding 引擎: %v", err)
	}
	if err := c.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	waitForRuntimeControllerStopped(t, c)
}
