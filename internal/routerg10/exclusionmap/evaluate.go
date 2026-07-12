package exclusionmap

import (
	"lingxi/internal/routerg10/residualmap"
	"lingxi/internal/routerg6/eventmap"
)

func evaluateInstitution(ctx MetricContext) AxisExclusion {
	bindings := map[string]MetricBinding{}
	t10k := ctx.Tiers["10000"]
	t100k := ctx.Tiers["100000"]
	capacityCliff := (t10k.CapacityFail && t10k.CapitalUSDT > 0) || (t100k.CapacityFail && t100k.CapitalUSDT > 0)
	bindings["capacity_cliff"] = MetricBinding{
		Metric: "large_tier_capacity_fail", Value: boolToFloat(capacityCliff), Threshold: 1, Passed: capacityCliff,
	}
	absProfit := absoluteProfitUSDT(t100k)
	absInsufficient := absProfit < minAbsoluteProfitUSDT
	bindings["absolute_profit_insufficient"] = MetricBinding{
		Metric: "absolute_profit_usdt_100k", Value: absProfit, Threshold: minAbsoluteProfitUSDT, Passed: absInsufficient,
	}
	idleRatio := capitalIdleRatio(t100k, ctx.Base)
	idleTooLong := idleRatio >= capitalIdleOccupationRatio
	bindings["capital_idle_too_long"] = MetricBinding{
		Metric: "capital_idle_occupation_ratio", Value: idleRatio, Threshold: capitalIdleOccupationRatio, Passed: idleTooLong,
	}
	bindings["custody_authorization_limit"] = MetricBinding{
		Metric: "okx_self_custody_only", Value: custodyMetricValue(ctx), Threshold: 1, Passed: custodyExcludesInstitutions(ctx),
	}
	impact := t100k.ImpactBPS
	largeImpact := impact >= largeOrderImpactThreshold
	bindings["large_order_impact"] = MetricBinding{
		Metric: "impact_bps_100k", Value: impact, Threshold: largeOrderImpactThreshold, Passed: largeImpact,
	}
	axis := finalizeAxis(AxisInstitution, InstitutionEvidence, bindings)
	return axis
}

func evaluateRetail(ctx MetricContext) AxisExclusion {
	bindings := map[string]MetricBinding{}
	idleRequired := ctx.Base.HoldHours >= longIdleHoldHours && float64(ctx.CellCount) <= eventSparsityCountThreshold
	bindings["long_idle_required"] = MetricBinding{
		Metric: "hold_hours_and_cell_sparsity", Value: ctx.Base.HoldHours, Threshold: longIdleHoldHours, Passed: idleRequired,
	}
	cannotChase := ctx.Base.DelayBars >= 1 || ctx.Item.ProfileID == eventmap.ProfileOKX5M
	bindings["cannot_chase_price"] = MetricBinding{
		Metric: "delay_or_profile_chase_penalty", Value: float64(ctx.Base.DelayBars), Threshold: 1, Passed: cannotChase,
	}
	drawdown := barRangeBPS(ctx.Base)
	drawdownThesis := drawdown >= drawdownRangeThresholdBPS
	bindings["must_tolerate_large_drawdown_with_thesis_exit"] = MetricBinding{
		Metric: "decision_bar_range_bps", Value: drawdown, Threshold: drawdownRangeThresholdBPS, Passed: drawdownThesis,
	}
	t30 := ctx.Tiers["30"]
	nonlinear := ctx.DynamicMinNotionalUSDT > 0 && minLotNonlinearityRatio(t30, ctx.DynamicMinNotionalUSDT) >= minLotNonlinearityThreshold
	bindings["min_lot_nonlinearity"] = MetricBinding{
		Metric: "pit_dynamic_min_notional_to_capital_ratio_30", Value: minLotNonlinearityRatio(t30, ctx.DynamicMinNotionalUSDT), Threshold: minLotNonlinearityThreshold, Passed: nonlinear,
	}
	complexOps := ctx.VisibleAnnouncements > 0 || ctx.Item.ShockCluster != "" && ctx.Item.ShockCluster != "none"
	bindings["complex_operational_discipline"] = MetricBinding{
		Metric: "visible_announcements_or_shock_ops", Value: float64(ctx.VisibleAnnouncements), Threshold: 1, Passed: complexOps,
	}
	return finalizeAxis(AxisRetail, RetailEvidence, bindings)
}

func evaluateBot(ctx MetricContext, crowding CrowdingStress) AxisExclusion {
	bindings := map[string]MetricBinding{}
	sparse := float64(ctx.CellCount) <= eventSparsityCountThreshold
	bindings["event_sparsity"] = MetricBinding{
		Metric: "cell_outcome_count", Value: float64(ctx.CellCount), Threshold: eventSparsityCountThreshold, Passed: sparse,
	}
	fixedFail := ctx.Item.Event.EventFamily == eventmap.FamilyExtremeContinuation && !sparse
	bindings["fixed_threshold_failure"] = MetricBinding{
		Metric: "continuation_family_with_sparse_cell", Value: boolToFloat(fixedFail), Threshold: 1, Passed: fixedFail,
	}
	comboChanges := ctx.Item.ShockCluster != "" && ctx.Item.ShockCluster != "none"
	bindings["conditional_combo_changes_per_event"] = MetricBinding{
		Metric: "shock_cluster_present", Value: boolToFloat(comboChanges), Threshold: 1, Passed: comboChanges,
	}
	t100 := ctx.Tiers["100"]
	queueCrowd := t100.CapacityFail && t100.CapitalUSDT > 0
	bindings["queue_crowding"] = MetricBinding{
		Metric: "capacity_fail_100", Value: boolToFloat(queueCrowd), Threshold: 1, Passed: queueCrowd,
	}
	decay := ctx.PostPublicationDecayBPS > 0
	bindings["post_publication_decay"] = MetricBinding{
		Metric: "post_publication_decay_bps", Value: ctx.PostPublicationDecayBPS, Threshold: 1, Passed: decay,
	}
	stressPassed := crowding.Passed
	bindings["crowding_stress_1_3_5_micro_competitors"] = MetricBinding{
		Metric: "edge_collapse_bps_at_5_competitors",
		Value:  edgeCollapseAt(crowding, 5),
		Threshold: crowdingCollapseThresholdBPS,
		Passed: stressPassed,
	}
	return finalizeAxis(AxisBot, BotEvidence, bindings)
}

func finalizeAxis(axis string, evidenceOrder []string, bindings map[string]MetricBinding) AxisExclusion {
	out := AxisExclusion{
		Axis:             axis,
		EvidenceBindings: bindings,
	}
	for _, evidence := range evidenceOrder {
		b, ok := bindings[evidence]
		if ok && b.Passed {
			out.Excluded = true
			out.PrimaryEvidence = evidence
			return out
		}
	}
	return out
}

func edgeCollapseAt(crowding CrowdingStress, competitors int) float64 {
	for _, level := range crowding.Levels {
		if level.Competitors == competitors {
			return level.EdgeCollapseBPS
		}
	}
	return 0
}

func boolToFloat(v bool) float64 {
	if v {
		return 1
	}
	return 0
}

func classifyAcceptReason(ctx MetricContext, institution, bot AxisExclusion) string {
	if personalTiersPass(ctx.Tiers) && !largeTiersPass(ctx.Tiers) {
		return AcceptPersonalConditionDependent
	}
	if institution.EvidenceBindings["absolute_profit_insufficient"].Passed && bot.EvidenceBindings["crowding_stress_1_3_5_micro_competitors"].Passed {
		return AcceptLowMotivationLowCapacity
	}
	if institution.Excluded && bot.Excluded && personalTiersPass(ctx.Tiers) {
		return AcceptPersonalConditionDependent
	}
	return ""
}

func forbiddenGates(ctx MetricContext, institution, retail, bot AxisExclusion) (hits []string, rejects []string) {
	if isSmallOnly(ctx, institution, retail, bot) {
		hits = append(hits, ForbiddenWildEvidence[3])
		rejects = append(rejects, RejectSmallOnly)
	}
	if isPublicCrowded(ctx, ctx.Tiers) {
		hits = append(hits, ForbiddenWildEvidence[1])
		rejects = append(rejects, RejectPublicCrowded)
	}
	if isFixedPublicSignal(ctx.Item, ctx.Tiers) {
		hits = append(hits, ForbiddenWildEvidence[2])
		rejects = append(rejects, RejectFixedSignal)
	}
	if isFirstInOnly(ctx.Item) {
		hits = append(hits, "first_in_only")
		rejects = append(rejects, RejectFirstInOnly)
	}
	return hits, rejects
}

func tripleAxisComplete(institution, retail, bot AxisExclusion) bool {
	return institution.Excluded && retail.Excluded && bot.Excluded
}

// EvaluateEvent computes metric-bound triple exclusion for one triggered event.
func EvaluateEvent(ctx MetricContext, gross float64) EvaluationResult {
	if gross <= 0 {
		return EvaluationResult{
			EventID:       ctx.Item.Event.EventID,
			RejectReasons: []string{RejectNoValidReason},
			Provenance: Provenance{
				MapManifestHash:          ctx.MapManifestHash,
				AnnouncementTapeHash:     ctx.AnnouncementTapeHash,
				AnnouncementManifest:     ctx.AnnouncementManifestHash,
				ConstitutionHash:         ctx.ConstitutionHash,
				DecisionBarIndex:         ctx.DecisionBarIdx,
				RollingQuoteVolume:       ctx.Base.RollingQuoteVolumeUSDT,
			},
		}
	}
	crowding := ComputeCrowdingStress(ctx, gross)
	institution := evaluateInstitution(ctx)
	retail := evaluateRetail(ctx)
	bot := evaluateBot(ctx, crowding)
	forbiddenHits, rejectReasons := forbiddenGates(ctx, institution, retail, bot)

	result := EvaluationResult{
		EventID:       ctx.Item.Event.EventID,
		Institution:   institution,
		Retail:        retail,
		Bot:           bot,
		Crowding:      crowding,
		ForbiddenHits: forbiddenHits,
		RejectReasons: rejectReasons,
		Direction:     "long",
		Horizon:       ctx.Base.Horizon,
		DelayBars:     ctx.Base.DelayBars,
		Provenance: Provenance{
			MapManifestHash:          ctx.MapManifestHash,
			AnnouncementTapeHash:     ctx.AnnouncementTapeHash,
			AnnouncementManifest:     ctx.AnnouncementManifestHash,
			ConstitutionHash:         ctx.ConstitutionHash,
			DecisionBarIndex:         ctx.DecisionBarIdx,
			RollingQuoteVolume:       ctx.Base.RollingQuoteVolumeUSDT,
		},
	}
	if len(rejectReasons) > 0 {
		return result
	}
	if !tripleAxisComplete(institution, retail, bot) {
		result.RejectReasons = append(result.RejectReasons, RejectAxisIncomplete)
		return result
	}
	acceptReason := classifyAcceptReason(ctx, institution, bot)
	if acceptReason == "" {
		result.RejectReasons = append(result.RejectReasons, RejectNoValidReason)
		return result
	}
	result.Accepted = true
	result.AcceptReason = acceptReason
	return result
}

// EvaluateFromGross is a test helper that evaluates from a pre-built metric context.
func EvaluateFromGross(ctx MetricContext, gross float64) EvaluationResult {
	ctx.Tiers = residualmap.ComputeTierCosts(gross, ctx.Base, ctx.Adj, 100)
	return EvaluateEvent(ctx, gross)
}
