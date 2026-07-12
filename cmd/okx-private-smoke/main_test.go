package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lingxi/internal/venue"
)

const testAPIKey = "test-api-key-smoke"
const testSecret = "0123456789abcdef0123456789abcdef"
const testPassphrase = "test-passphrase-smoke"

func writeTestSecret(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "secret.yaml")
	content := "okx:\n  api: " + testAPIKey + "\n  secret: " + testSecret + "\n  passphrase: " + testPassphrase + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDryValidationOffline(t *testing.T) {
	secretPath := writeTestSecret(t)
	var buf bytes.Buffer
	deps := defaultDeps(&buf)
	if _, err := runDryValidation(deps, secretPath); err != nil {
		t.Fatalf("dry validation: %v\n%s", err, buf.String())
	}
	out := buf.String()
	for _, forbidden := range []string{testAPIKey, testSecret, testPassphrase} {
		if strings.Contains(out, forbidden) {
			t.Fatalf("输出不得包含密钥明文: %q\n%s", forbidden, out)
		}
	}
	if !strings.Contains(out, "dry_validation=OK") {
		t.Fatalf("缺少 dry_validation=OK:\n%s", out)
	}
	if !strings.Contains(out, "fixtures orders=true positions=true") {
		t.Fatalf("fixture 校验应通过:\n%s", out)
	}
}

func TestDefaultRunDoesNotRequireNetwork(t *testing.T) {
	secretPath := writeTestSecret(t)
	var buf bytes.Buffer
	deps := defaultDeps(&buf)
	if err := run([]string{"-secret", secretPath}, &buf, deps); err != nil {
		t.Fatalf("default run: %v\n%s", err, buf.String())
	}
	out := buf.String()
	if strings.Contains(out, "connect 只读探针") {
		t.Fatal("默认模式不应进入 connect 探针")
	}
	if strings.Contains(out, "handshake login=") {
		t.Fatal("默认模式不应输出 WS 握手探针")
	}
}

func TestConnectProbeTracksLatencyAndReconcile(t *testing.T) {
	eventTime := time.Date(2026, 7, 11, 8, 0, 0, 0, time.UTC)
	receivedAt := eventTime.Add(120 * time.Millisecond)
	probe := fakeProbe{
		events: []venue.UserEvent{
			{
				Kind: venue.UserEventAccountUpdate, Symbol: "BTC-USDT",
				Position: &venue.Position{Symbol: "BTC-USDT", PositionAmt: -0.03},
				Time: eventTime, ReceivedAt: receivedAt,
			},
			{Kind: venue.UserEventOrderUpdate, Symbol: "BTC-USDT", RawType: "live"},
		},
		positions: []venue.Position{
			{Symbol: "BTC-USDT", PositionAmt: -0.03},
		},
	}
	var buf bytes.Buffer
	rep, err := runConnectProbe(context.Background(), probe, 10*time.Millisecond, &buf)
	if err != nil {
		t.Fatalf("connect probe: %v\n%s", err, buf.String())
	}
	if !rep.HandshakeOK || !rep.OrdersSubOK || !rep.PositionsSubOK {
		t.Fatalf("握手/订阅健康标记不符: %+v", rep)
	}
	if rep.PositionEvents != 1 || rep.OrderEvents != 1 {
		t.Fatalf("事件计数不符: %+v", rep)
	}
	if rep.LatencySamples != 1 || rep.LatencyP50Ms < 119 || rep.LatencyP50Ms > 121 {
		t.Fatalf("延迟统计不符: %+v", rep)
	}
	if !rep.ReconcileOK || rep.RESTPositionN != 1 || rep.WSPositionN != 1 {
		t.Fatalf("REST 对账不符: %+v notes=%v", rep, rep.ReconcileNotes)
	}
}

func TestConnectProbeHandshakeFailure(t *testing.T) {
	probe := fakeProbe{streamErr: errHandshakeFailed}
	var buf bytes.Buffer
	rep, err := runConnectProbe(context.Background(), probe, time.Second, &buf)
	if err == nil {
		t.Fatal("握手失败应返回 error")
	}
	if rep.HandshakeOK {
		t.Fatal("握手失败时不应标记 HandshakeOK")
	}
}

func TestReconcilePositionsMismatch(t *testing.T) {
	ok, notes := reconcilePositions(
		map[venue.Symbol]float64{"BTC-USDT": -0.03},
		[]venue.Position{{Symbol: "ETH-USDT", PositionAmt: 1.0}},
	)
	if ok {
		t.Fatalf("条数/数量不一致应 reconcile_ok=false, notes=%v", notes)
	}
}

func TestValidateFixtures(t *testing.T) {
	if err := validateOrdersFixture(); err != nil {
		t.Fatal(err)
	}
	if err := validatePositionsFixture(); err != nil {
		t.Fatal(err)
	}
}

type fakeProbe struct {
	events    []venue.UserEvent
	positions []venue.Position
	streamErr error
}

func (f fakeProbe) StreamUserData(ctx context.Context) (<-chan venue.UserEvent, error) {
	if f.streamErr != nil {
		return nil, f.streamErr
	}
	ch := make(chan venue.UserEvent, len(f.events))
	for _, e := range f.events {
		ch <- e
	}
	close(ch)
	return ch, nil
}

func (f fakeProbe) FetchPositions(context.Context) ([]venue.Position, error) {
	return f.positions, nil
}

var errHandshakeFailed = handshakeError("login failed")

type handshakeError string

func (e handshakeError) Error() string { return string(e) }

func TestDryValidationMissingCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.yaml")
	if err := os.WriteFile(path, []byte("okx:\n  api: only-key\n  secret: \"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	_, err := runDryValidation(defaultDeps(&buf), path)
	if err == nil {
		t.Fatal("缺 secret 应失败")
	}
}
