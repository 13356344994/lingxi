package app

import (
	"bufio"
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const autonomousStrategyFile = "autonomous_strategies.json"

type autonomousRule struct {
	Field string  `json:"field"`
	Op    string  `json:"op"`
	Value float64 `json:"value"`
}

type autonomousStrategySpec struct {
	Source          string           `json:"source"`
	Direction       string           `json:"direction"`
	HoldHours       int              `json:"hold_hours"`
	Rules           []autonomousRule `json:"rules"`
	State           string           `json:"state"` // shadow/probe/active/retired
	Generation      int              `json:"generation"`
	ShadowStartedAt time.Time        `json:"shadow_started_at"`
	ProbeStartedAt  time.Time        `json:"probe_started_at,omitempty"`
	MinShadowN      int              `json:"min_shadow_n"`
	MinPF           float64          `json:"min_pf"`
	MinMedianBps    float64          `json:"min_median_bps"`
	MaxTopShare     float64          `json:"max_top_share"`
	LastReason      string           `json:"last_reason,omitempty"`
}

type autonomousRegistry struct {
	SchemaVersion int                      `json:"schema_version"`
	UpdatedAt     time.Time                `json:"updated_at"`
	Strategies    []autonomousStrategySpec `json:"strategies"`
}

type autonomousMetrics struct {
	Ret1hBps float64
	BTC24Bps float64
	SymVolx  float64
	AgeDays  float64
}

type autonomousPerf struct {
	N          int
	Median     float64
	PF         float64
	NetSum     float64
	TopShare   float64
	GoodBlocks int
}

var autonomousStrategyMu sync.Mutex

func autonomousStrategyPath(recordDir string) string {
	return filepath.Join(recordDir, autonomousStrategyFile)
}

func defaultAutonomousStrategies(now time.Time, generation int) autonomousRegistry {
	common := []autonomousRule{
		{Field: "ret_1h_bps", Op: "le", Value: -200},
		{Field: "btc_24h_bps", Op: "le", Value: -300},
		{Field: "sym_volx", Op: "ge", Value: 1.5},
	}
	return autonomousRegistry{SchemaVersion: 1, UpdatedAt: now, Strategies: []autonomousStrategySpec{
		{
			Source: "panic_volume_fade_4h", Direction: "long", HoldHours: 4,
			Rules: append(append([]autonomousRule{}, common...), autonomousRule{Field: "age_days", Op: "ge", Value: 180}),
			State: "shadow", Generation: generation, ShadowStartedAt: now,
			MinShadowN: 60, MinPF: 1.30, MinMedianBps: 0, MaxTopShare: 0.10,
		},
		{
			Source: "panic_volume_fade_1h_mature", Direction: "long", HoldHours: 1,
			Rules: append(append([]autonomousRule{}, common...), autonomousRule{Field: "age_days", Op: "ge", Value: 365}),
			State: "shadow", Generation: generation, ShadowStartedAt: now,
			MinShadowN: 60, MinPF: 1.30, MinMedianBps: 0, MaxTopShare: 0.10,
		},
	}}
}

func loadAutonomousRegistry(recordDir string, now time.Time, generation int) autonomousRegistry {
	var reg autonomousRegistry
	if b, err := os.ReadFile(autonomousStrategyPath(recordDir)); err == nil &&
		json.Unmarshal(b, &reg) == nil && reg.SchemaVersion == 1 {
		return reg
	}
	reg = defaultAutonomousStrategies(now, generation)
	_ = saveAutonomousRegistry(recordDir, reg)
	return reg
}

func saveAutonomousRegistry(recordDir string, reg autonomousRegistry) error {
	reg.SchemaVersion = 1
	reg.UpdatedAt = time.Now().UTC()
	return writeJSONFile(autonomousStrategyPath(recordDir), reg)
}

func autonomousRuleValue(m autonomousMetrics, field string) (float64, bool) {
	switch field {
	case "ret_1h_bps":
		return m.Ret1hBps, true
	case "btc_24h_bps":
		return m.BTC24Bps, true
	case "sym_volx":
		return m.SymVolx, true
	case "age_days":
		return m.AgeDays, true
	default:
		return 0, false
	}
}

func autonomousRulesMatch(rules []autonomousRule, m autonomousMetrics) bool {
	for _, rule := range rules {
		v, ok := autonomousRuleValue(m, rule.Field)
		if !ok || math.IsNaN(v) || math.IsInf(v, 0) {
			return false
		}
		switch rule.Op {
		case "le":
			if v > rule.Value {
				return false
			}
		case "ge":
			if v < rule.Value {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func autonomousPerfFrom(values []float64, symbols []string, minBlocks int) autonomousPerf {
	if len(values) == 0 || len(values) != len(symbols) {
		return autonomousPerf{}
	}
	cp := append([]float64(nil), values...)
	sort.Float64s(cp)
	gp, gl, sum := 0.0, 0.0, 0.0
	counts := map[string]int{}
	for i, v := range values {
		sum += v
		if v > 0 {
			gp += v
		} else if v < 0 {
			gl -= v
		}
		counts[symbols[i]]++
	}
	top := 0
	for _, n := range counts {
		if n > top {
			top = n
		}
	}
	goodBlocks := 0
	if minBlocks > 0 {
		for b := 0; b < minBlocks; b++ {
			lo, hi := len(values)*b/minBlocks, len(values)*(b+1)/minBlocks
			if hi > lo {
				part := append([]float64(nil), values[lo:hi]...)
				sort.Float64s(part)
				if part[len(part)/2] > 0 {
					goodBlocks++
				}
			}
		}
	}
	pf := 99.0
	if gl > 0 {
		pf = gp / gl
	}
	return autonomousPerf{
		N: len(values), Median: cp[len(cp)/2], PF: pf, NetSum: sum,
		TopShare: float64(top) / float64(len(values)), GoodBlocks: goodBlocks,
	}
}

func directionShadowPerf(path, source string, since time.Time) autonomousPerf {
	f, err := os.Open(path)
	if err != nil {
		return autonomousPerf{}
	}
	defer f.Close()
	var vals []float64
	var syms []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)
	for sc.Scan() {
		var ev directionFactorEvent
		if json.Unmarshal(sc.Bytes(), &ev) != nil || ev.Stage != "close" || ev.Source != source ||
			ev.RecordedAt.Before(since) {
			continue
		}
		vals = append(vals, ev.Net18Bps)
		syms = append(syms, ev.Symbol)
	}
	return autonomousPerfFrom(vals, syms, 6)
}

func (e *OKXDirectionalEngine) autonomousProbePerf(source string, since time.Time) autonomousPerf {
	rows, _ := loadTailJSONLFile[okxDirectionalSettlement](e.settlementsPath, since, 0)
	var vals []float64
	var syms []string
	for _, row := range rows {
		if row.FastCandidateSource != source || row.NotionalUSDT <= 0 {
			continue
		}
		vals = append(vals, row.RealizedUSDT/row.NotionalUSDT*1e4)
		syms = append(syms, row.Symbol)
	}
	return autonomousPerfFrom(vals, syms, 0)
}

func appendAllowedSource(st *autonomousRiskState, source string) {
	for _, s := range st.AllowedSources {
		if s == source {
			return
		}
	}
	st.AllowedSources = append(st.AllowedSources, source)
}

func removeAllowedSource(st *autonomousRiskState, source string) {
	out := st.AllowedSources[:0]
	for _, s := range st.AllowedSources {
		if s != source {
			out = append(out, s)
		}
	}
	st.AllowedSources = out
}

func (e *OKXDirectionalEngine) autonomousLifecycleTick() {
	autonomousStrategyMu.Lock()
	defer autonomousStrategyMu.Unlock()
	now := e.nowFn().UTC()
	riskDir := filepath.Dir(e.recordDir)
	risk, ok := loadAutonomousRisk(riskDir)
	if !ok {
		return
	}
	reg := loadAutonomousRegistry(e.recordDir, now, risk.Generation)
	changed := false

	if risk.Phase == autonomyPhaseLearning {
		for i := range reg.Strategies {
			e.fastAutoProbeRevoke(reg.Strategies[i].Source)
			e.fastProbeReset(reg.Strategies[i].Source)
			reg.Strategies[i].State = "shadow"
			reg.Strategies[i].Generation = risk.Generation
			reg.Strategies[i].ShadowStartedAt = now
			reg.Strategies[i].ProbeStartedAt = time.Time{}
			reg.Strategies[i].LastReason = "总止错后重新学习"
		}
		risk.Phase = autonomyPhaseShadow
		risk.AllowedSources = nil
		risk.RequireGenerationCert = true
		_ = saveAutonomousRisk(riskDir, risk)
		changed = true
	}

	for i := range reg.Strategies {
		s := &reg.Strategies[i]
		switch s.State {
		case "shadow":
			p := directionShadowPerf(filepath.Join(e.recordDir, directionFactorEventFile), s.Source, s.ShadowStartedAt)
			if p.N < s.MinShadowN {
				continue
			}
			if p.Median > s.MinMedianBps && p.PF >= s.MinPF && p.TopShare <= s.MaxTopShare && p.GoodBlocks >= 4 {
				if !automaticLivePromotionSealed && e.fastAutoProbeAdmit(s.Source) {
					e.fastProbeReset(s.Source)
					s.State = "probe"
					s.ProbeStartedAt = now
					s.LastReason = "shadow过门，自动进入最小真钱探针"
					appendAllowedSource(&risk, s.Source)
					risk.Phase = autonomyPhaseProbe
					risk.RequireGenerationCert = true
					_ = saveAutonomousRisk(riskDir, risk)
					if e.controlPlane != nil {
						_ = e.controlPlane.SetGlobalHalt(false)
					}
					changed = true
				}
			} else {
				s.State = "retired"
				s.LastReason = "shadow到线未过净中位/PF/分块/单币门"
				changed = true
			}
		case "probe":
			p := e.autonomousProbePerf(s.Source, s.ProbeStartedAt)
			if p.N < 20 {
				continue
			}
			if p.Median > 0 && p.PF >= 1.30 && p.NetSum > 0 && p.TopShare <= 0.20 {
				s.State = "active"
				s.LastReason = "最小真钱探针过门，自动持证"
				risk.Phase = autonomyPhaseActive
				appendAllowedSource(&risk, s.Source)
				_ = saveAutonomousRisk(riskDir, risk)
				changed = true
			} else {
				e.fastAutoProbeRevoke(s.Source)
				s.State = "retired"
				s.LastReason = "真钱探针失败，自动退役"
				removeAllowedSource(&risk, s.Source)
				risk.Phase = autonomyPhaseShadow
				_ = saveAutonomousRisk(riskDir, risk)
				if e.controlPlane != nil {
					_ = e.controlPlane.SetGlobalHalt(true)
				}
				changed = true
			}
		}
	}
	if changed {
		_ = saveAutonomousRegistry(e.recordDir, reg)
	}
}

func (e *OKXDirectionalEngine) autonomousLifecycleLoop(ctx context.Context) {
	t := time.NewTimer(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			e.autonomousLifecycleTick()
			t.Reset(10 * time.Minute)
		}
	}
}

func (e *OKXDirectionalEngine) autonomousStrategySnapshot() []autonomousStrategySpec {
	autonomousStrategyMu.Lock()
	defer autonomousStrategyMu.Unlock()
	risk, _ := loadAutonomousRisk(filepath.Dir(e.recordDir))
	return append([]autonomousStrategySpec(nil), loadAutonomousRegistry(e.recordDir, e.nowFn().UTC(), risk.Generation).Strategies...)
}

func autonomousSpecsMatching(recordDir string, now time.Time, generation int, m autonomousMetrics) []autonomousStrategySpec {
	autonomousStrategyMu.Lock()
	defer autonomousStrategyMu.Unlock()
	reg := loadAutonomousRegistry(recordDir, now, generation)
	var out []autonomousStrategySpec
	for _, s := range reg.Strategies {
		if s.State != "retired" && autonomousRulesMatch(s.Rules, m) {
			out = append(out, s)
		}
	}
	return out
}

func autonomyStrategyIsLive(recordDir, source string) bool {
	autonomousStrategyMu.Lock()
	defer autonomousStrategyMu.Unlock()
	reg := loadAutonomousRegistry(recordDir, time.Now().UTC(), 1)
	for _, s := range reg.Strategies {
		if s.Source == strings.TrimSpace(source) {
			return s.State == "probe" || s.State == "active"
		}
	}
	return false
}

func autonomyStrategyHoldHours(recordDir, source string) (int, bool) {
	autonomousStrategyMu.Lock()
	defer autonomousStrategyMu.Unlock()
	reg := loadAutonomousRegistry(recordDir, time.Now().UTC(), 1)
	for _, s := range reg.Strategies {
		if s.Source == strings.TrimSpace(source) && (s.State == "probe" || s.State == "active") && s.HoldHours > 0 {
			return s.HoldHours, true
		}
	}
	return 0, false
}
