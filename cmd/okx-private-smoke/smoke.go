package main

import (
	"context"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"time"

	"lingxi/internal/app"
	"lingxi/internal/venue"
	"lingxi/internal/venue/okx"
)

const (
	posQtyEpsilon = 1e-9
)

type secretConfig struct {
	APIKey     string
	Secret     string
	Passphrase string
	ProxyURL   string
}

func (s secretConfig) present() credentialPresence {
	return credentialPresence{
		APIKey:     strings.TrimSpace(s.APIKey) != "",
		Secret:     strings.TrimSpace(s.Secret) != "",
		Passphrase: strings.TrimSpace(s.Passphrase) != "",
	}
}

type credentialPresence struct {
	APIKey     bool
	Secret     bool
	Passphrase bool
}

func (p credentialPresence) complete() bool {
	return p.APIKey && p.Secret && p.Passphrase
}

type dryReport struct {
	SecretPath         string
	CredentialsLoaded  bool
	CredentialsPresent credentialPresence
	AdapterOK          bool
	HasNativeDeadMan   bool
	UnifiedWallet      bool
	ContractBasedPerp  bool
	OrdersFixtureOK    bool
	PositionsFixtureOK bool
	LoginFrameOK       bool
	LoadError          string
}

type connectReport struct {
	HandshakeOK      bool
	OrdersSubOK      bool
	PositionsSubOK   bool
	Duration         time.Duration
	EventCount       int
	OrderEvents      int
	PositionEvents   int
	TradeEvents      int
	LatencySamples   int
	LatencyP50Ms     float64
	LatencyMaxMs     float64
	RESTPositionN    int
	WSPositionN      int
	ReconcileOK      bool
	ReconcileNotes   []string
	StreamError      string
}

type privateProbe interface {
	StreamUserData(ctx context.Context) (<-chan venue.UserEvent, error)
	FetchPositions(ctx context.Context) ([]venue.Position, error)
}

type smokeDeps struct {
	out        io.Writer
	loadSecret func(path string) (secretConfig, error)
	newAdapter func(sec secretConfig) (*okx.Adapter, error)
}

func defaultDeps(out io.Writer) smokeDeps {
	return smokeDeps{
		out:        out,
		loadSecret: loadOKXSecret,
		newAdapter: buildOKXAdapter,
	}
}

func loadOKXSecret(path string) (secretConfig, error) {
	sec, err := app.LoadVenueSecretConfig(path, okx.VenueName)
	if err != nil {
		return secretConfig{}, err
	}
	return secretConfig{
		APIKey: sec.APIKey, Secret: sec.Secret, Passphrase: sec.Passphrase, ProxyURL: sec.ProxyURL,
	}, nil
}

func buildOKXAdapter(sec secretConfig) (*okx.Adapter, error) {
	return okx.New(venue.Config{
		Name: okx.VenueName, APIKey: sec.APIKey, APISecret: sec.Secret,
		Passphrase: sec.Passphrase, ProxyURL: sec.ProxyURL,
	})
}

func runDryValidation(deps smokeDeps, secretPath string) (dryReport, error) {
	rep := dryReport{SecretPath: secretPath}
	sec, err := deps.loadSecret(secretPath)
	if err != nil {
		rep.LoadError = err.Error()
		rep.CredentialsPresent = secretConfig{}.present()
	} else {
		rep.CredentialsLoaded = true
		rep.CredentialsPresent = sec.present()
	}
	rep.OrdersFixtureOK = validateOrdersFixture() == nil
	rep.PositionsFixtureOK = validatePositionsFixture() == nil
	if rep.CredentialsPresent.complete() {
		if _, err := okx.BuildPrivateLoginFrame(sec.APIKey, sec.Secret, sec.Passphrase, time.Now().UTC()); err == nil {
			rep.LoginFrameOK = true
		}
	}
	if rep.CredentialsLoaded && rep.CredentialsPresent.complete() {
		adp, err := deps.newAdapter(sec)
		if err == nil && adp != nil {
			rep.AdapterOK = true
			caps := adp.Capabilities()
			rep.HasNativeDeadMan = caps.HasNativeDeadMan
			rep.UnifiedWallet = caps.UnifiedWallet
			rep.ContractBasedPerp = caps.ContractBasedPerp
		}
	}
	printDryReport(deps.out, rep)
	if !rep.OrdersFixtureOK || !rep.PositionsFixtureOK {
		return rep, fmt.Errorf("fixture 解析自检失败")
	}
	if rep.LoadError != "" {
		return rep, fmt.Errorf("凭证加载失败")
	}
	if !rep.CredentialsPresent.complete() {
		return rep, fmt.Errorf("凭证字段不完整(需 api/secret/passphrase)")
	}
	if !rep.AdapterOK || !rep.LoginFrameOK {
		return rep, fmt.Errorf("适配器或登录帧构造失败")
	}
	return rep, nil
}

func validateOrdersFixture() error {
	push, err := okx.ParsePrivateOrdersPush([]byte(ordersPushFixture))
	if err != nil {
		return err
	}
	if push.Arg.Channel != "orders" || len(push.Data) != 1 {
		return fmt.Errorf("orders fixture 结构不符")
	}
	return nil
}

func validatePositionsFixture() error {
	push, err := okx.ParsePrivatePositionsPush([]byte(positionsPushFixture))
	if err != nil {
		return err
	}
	if push.Arg.Channel != "positions" || len(push.Data) != 1 {
		return fmt.Errorf("positions fixture 结构不符")
	}
	if push.Data[0].SignedQty(0.01) != -0.03 {
		return fmt.Errorf("positions ctVal 换算不符")
	}
	return nil
}

func printDryReport(w io.Writer, rep dryReport) {
	fmt.Fprintln(w, "========================================================================")
	fmt.Fprintln(w, "okx-private-smoke: 离线 dry 校验(不拨网;绝不下单;不打印密钥)")
	fmt.Fprintln(w, "========================================================================")
	fmt.Fprintf(w, "secret_path=%s credentials_loaded=%v\n", rep.SecretPath, rep.CredentialsLoaded)
	fmt.Fprintf(w, "credential_fields api=%v secret=%v passphrase=%v\n",
		rep.CredentialsPresent.APIKey, rep.CredentialsPresent.Secret, rep.CredentialsPresent.Passphrase)
	if rep.LoadError != "" {
		fmt.Fprintf(w, "load_error=%s\n", rep.LoadError)
	}
	fmt.Fprintf(w, "adapter_ok=%v login_frame_ok=%v\n", rep.AdapterOK, rep.LoginFrameOK)
	fmt.Fprintf(w, "capabilities dead_man=%v unified_wallet=%v contract_perp=%v\n",
		rep.HasNativeDeadMan, rep.UnifiedWallet, rep.ContractBasedPerp)
	fmt.Fprintf(w, "fixtures orders=%v positions=%v\n", rep.OrdersFixtureOK, rep.PositionsFixtureOK)
	if rep.OrdersFixtureOK && rep.PositionsFixtureOK && rep.LoadError == "" &&
		rep.CredentialsPresent.complete() && rep.AdapterOK && rep.LoginFrameOK {
		fmt.Fprintln(w, "dry_validation=OK")
	} else {
		fmt.Fprintln(w, "dry_validation=FAIL")
	}
}

func runConnectProbe(ctx context.Context, probe privateProbe, duration time.Duration, w io.Writer) (connectReport, error) {
	rep := connectReport{Duration: duration}
	events, err := probe.StreamUserData(ctx)
	if err != nil {
		rep.StreamError = err.Error()
		printConnectReport(w, rep)
		return rep, fmt.Errorf("私有流握手失败: %w", err)
	}
	rep.HandshakeOK = true
	rep.OrdersSubOK = true
	rep.PositionsSubOK = true

	deadline := time.Now().Add(duration)
	wsPositions := make(map[venue.Symbol]float64)
	var latencies []float64

	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		select {
		case <-ctx.Done():
			remaining = 0
		case event, ok := <-events:
			if !ok {
				remaining = 0
				break
			}
			rep.EventCount++
			switch event.Kind {
			case venue.UserEventOrderUpdate:
				rep.OrderEvents++
			case venue.UserEventAccountUpdate:
				rep.PositionEvents++
				if event.Position != nil && math.Abs(event.Position.PositionAmt) > posQtyEpsilon {
					wsPositions[event.Symbol] = event.Position.PositionAmt
				}
			case venue.UserEventTrade:
				rep.TradeEvents++
			}
			if !event.Time.IsZero() && !event.ReceivedAt.IsZero() {
				ms := event.ReceivedAt.Sub(event.Time).Seconds() * 1000
				if ms >= 0 {
					latencies = append(latencies, ms)
				}
			}
		}
		if remaining <= 0 {
			break
		}
	}

	rep.LatencySamples = len(latencies)
	rep.LatencyP50Ms, rep.LatencyMaxMs = latencyStats(latencies)
	rep.WSPositionN = len(wsPositions)

	restCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	restPositions, restErr := probe.FetchPositions(restCtx)
	if restErr != nil {
		rep.ReconcileNotes = append(rep.ReconcileNotes, "REST FetchPositions 失败: "+restErr.Error())
	} else {
		rep.RESTPositionN = countNonZeroPositions(restPositions)
		rep.ReconcileOK, rep.ReconcileNotes = reconcilePositions(wsPositions, restPositions)
	}

	printConnectReport(w, rep)
	if rep.StreamError != "" {
		return rep, fmt.Errorf("stream error")
	}
	return rep, nil
}

func countNonZeroPositions(positions []venue.Position) int {
	n := 0
	for _, p := range positions {
		if math.Abs(p.PositionAmt) > posQtyEpsilon {
			n++
		}
	}
	return n
}

func reconcilePositions(ws map[venue.Symbol]float64, rest []venue.Position) (bool, []string) {
	restMap := make(map[venue.Symbol]float64)
	for _, p := range rest {
		if math.Abs(p.PositionAmt) <= posQtyEpsilon {
			continue
		}
		restMap[p.Symbol] = p.PositionAmt
	}
	var notes []string
	ok := true
	if len(ws) != len(restMap) {
		ok = false
		notes = append(notes, fmt.Sprintf("持仓条数 WS=%d REST=%d", len(ws), len(restMap)))
	}
	symbols := make(map[venue.Symbol]struct{})
	for s := range ws {
		symbols[s] = struct{}{}
	}
	for s := range restMap {
		symbols[s] = struct{}{}
	}
	keys := make([]string, 0, len(symbols))
	for s := range symbols {
		keys = append(keys, string(s))
	}
	sort.Strings(keys)
	for _, sym := range keys {
		s := venue.Symbol(sym)
		wQty, rQty := ws[s], restMap[s]
		if math.Abs(wQty-rQty) > posQtyEpsilon {
			ok = false
			notes = append(notes, fmt.Sprintf("%s WS=%.8f REST=%.8f", sym, wQty, rQty))
		}
	}
	if ok && len(notes) == 0 {
		notes = append(notes, "WS 与 REST 持仓数量一致")
	}
	return ok, notes
}

func latencyStats(samples []float64) (p50, max float64) {
	if len(samples) == 0 {
		return 0, 0
	}
	cp := append([]float64(nil), samples...)
	sort.Float64s(cp)
	p50 = cp[len(cp)/2]
	max = cp[len(cp)-1]
	return p50, max
}

func printConnectReport(w io.Writer, rep connectReport) {
	fmt.Fprintln(w, "========================================================================")
	fmt.Fprintln(w, "okx-private-smoke: connect 只读探针(订阅 orders/positions;绝不下单;不打印密钥)")
	fmt.Fprintln(w, "========================================================================")
	fmt.Fprintf(w, "handshake login=%v orders_sub=%v positions_sub=%v duration=%s\n",
		rep.HandshakeOK, rep.OrdersSubOK, rep.PositionsSubOK, rep.Duration)
	if rep.StreamError != "" {
		fmt.Fprintf(w, "stream_error=%s\n", rep.StreamError)
	}
	fmt.Fprintf(w, "events total=%d orders=%d positions=%d trades=%d\n",
		rep.EventCount, rep.OrderEvents, rep.PositionEvents, rep.TradeEvents)
	fmt.Fprintf(w, "latency samples=%d p50_ms=%.2f max_ms=%.2f\n",
		rep.LatencySamples, rep.LatencyP50Ms, rep.LatencyMaxMs)
	fmt.Fprintf(w, "positions ws_nonzero=%d rest_nonzero=%d reconcile_ok=%v\n",
		rep.WSPositionN, rep.RESTPositionN, rep.ReconcileOK)
	for _, note := range rep.ReconcileNotes {
		fmt.Fprintf(w, "reconcile: %s\n", note)
	}
}
