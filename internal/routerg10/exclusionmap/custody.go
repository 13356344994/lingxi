package exclusionmap

// custodyMetricValue reports whether personal OKX self-custody execution is required.
func custodyMetricValue(ctx MetricContext) float64 {
	if ctx.ConstitutionHash == "" {
		return 0
	}
	return 1
}

// custodyExcludesInstitutions is true when institutional delegated custody cannot
// execute the idiosyncratic personal flow (sparse/complex/announcement-bound).
func custodyExcludesInstitutions(ctx MetricContext) bool {
	if ctx.ConstitutionHash == "" {
		return false
	}
	sparse := float64(ctx.CellCount) <= eventSparsityCountThreshold
	complexOps := ctx.VisibleAnnouncements > 0 ||
		(ctx.Item.ShockCluster != "" && ctx.Item.ShockCluster != "none")
	longHold := ctx.Base.HoldHours >= longIdleHoldHours
	return sparse || complexOps || longHold
}
