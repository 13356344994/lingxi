package counterfactual

import (
	g10protocol "lingxi/internal/routerg10/protocol"
)

func applyOpenGates(ctx DecisionContext, m *ActionMetric) {
	if !m.Allowed {
		return
	}
	if isOpenAction(m.Action) {
		gateOpen(ctx, m)
	}
	if ctx.Event.HasEvent {
		return
	}
	if !isNoEventAllowed(m.Action) {
		m.Allowed = false
		m.RejectReasons = appendUnique(m.RejectReasons, RejectNoMatchingEvent)
	}
}

func gateOpen(ctx DecisionContext, m *ActionMetric) {
	direction := openDirection(m.Action, ctx)
	adj := resolvePersonalAdj(ctx, direction)
	if adj.CriticalUnknown && g10protocol.FingerprintUnknownBlocksOpen {
		m.Allowed = false
		m.RejectReasons = appendUnique(m.RejectReasons, RejectFingerprintUnknown)
		return
	}
	if ctx.Fingerprint.FingerprintHash == "" && g10protocol.FingerprintUnknownBlocksOpen {
		m.Allowed = false
		m.RejectReasons = appendUnique(m.RejectReasons, RejectFingerprintUnknown)
		return
	}
	if ctx.Memory.VetoOpen {
		m.Allowed = false
		m.SizeScale = 0
		m.RejectReasons = appendUnique(m.RejectReasons, RejectMemoryVeto)
		return
	}
	if ctx.Memory.SizeScale <= 0 && containsCode(ctx.Memory.AdviceCodes, episodicInsufficientSample()) {
		m.Allowed = false
		m.SizeScale = 0
		m.RejectReasons = appendUnique(m.RejectReasons, RejectInsufficientSample)
		return
	}
	if ctx.OOD {
		m.Allowed = false
		m.RejectReasons = appendUnique(m.RejectReasons, RejectOOD)
		return
	}
	if !ctx.Solvent {
		m.Allowed = false
		m.RejectReasons = appendUnique(m.RejectReasons, RejectStressInsolvent)
		return
	}
	if m.PersonalConservativeNetBPS < g10protocol.PersonalConservativeExpectedNetMin {
		m.Allowed = false
		m.RejectReasons = appendUnique(m.RejectReasons, RejectBreakevenFail)
		return
	}
	if !m.MinLotFeasible {
		m.Allowed = false
		return
	}
	if ctx.Position.ConcurrentEvents >= ctx.Position.MaxConcurrent && ctx.Position.MaxConcurrent > 0 {
		m.Allowed = false
		m.RejectReasons = appendUnique(m.RejectReasons, RejectConcurrencyFull)
		return
	}
	if m.PersonalConservativeNetBPS >= 0 && m.PersonalConservativeNetBPS < NearZeroEdgeThresholdBPS {
		m.SizeScale = mathMin(m.SizeScale, ctx.Memory.SizeScale, 0.25)
		m.RejectReasons = appendUnique(m.RejectReasons, RejectNearZeroEdge)
		if m.SizeScale <= 0 {
			m.Allowed = false
			return
		}
	}
	if ctx.Memory.SizeScale > 0 && ctx.Memory.SizeScale < 1 {
		m.SizeScale = mathMin(m.SizeScale, ctx.Memory.SizeScale, 1)
	}
	stressNet := m.PersonalConservativeNetBPS - m.StressLossBPS
	if stressNet < 0 {
		m.Allowed = false
		m.RejectReasons = appendUnique(m.RejectReasons, RejectStressInsolvent)
	}
}

func episodicInsufficientSample() string { return "MEMORY_INSUFFICIENT_SAMPLE" }

func isOpenAction(action string) bool {
	for _, a := range OpenActions {
		if a == action {
			return true
		}
	}
	return action == "ADD" || action == "REVERSE"
}

func isNoEventAllowed(action string) bool {
	for _, a := range NoEventAllowed {
		if a == action {
			return true
		}
	}
	return action == "NO_TRADE"
}

func appendUnique(dst []string, items ...string) []string {
	for _, item := range items {
		found := false
		for _, d := range dst {
			if d == item {
				found = true
				break
			}
		}
		if !found {
			dst = append(dst, item)
		}
	}
	return dst
}

func mathMin(vals ...float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	out := vals[0]
	for _, v := range vals[1:] {
		if v < out {
			out = v
		}
	}
	return out
}

func containsCode(codes []string, want string) bool {
	for _, c := range codes {
		if c == want {
			return true
		}
	}
	return false
}
