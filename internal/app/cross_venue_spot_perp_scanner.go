package app

import (
	"log"
	"math"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"lingxi/internal/venue"
)

const (
	crossVenueSpotPerpMinTradableNotionalUSDT    = 5.0
	crossVenueSpotPerpDefaultSpotFeeBps          = 10.0
	crossVenueSpotPerpDefaultPeakFundingHaircut  = 0.50
	crossVenueSpotPerpDefaultBasisConvergence    = 0.50
	crossVenueSpotPerpDefaultBasisWidenStressBps = 5.0
	crossVenueSpotPerpDefaultRebalanceCostBps    = 3.0
	crossVenueSpotPerpDefaultRebalanceLatencyH   = 2.0
	crossVenueSpotPerpDefaultPerpMaintMarginRate = 0.005
	crossVenueSpotPerpDefaultPerpWalletBuffer    = 0.10
	carryScannerDefaultFundingCaptureProbability = 0.50
	carryScannerPerpPerpDustLegFraction          = 0.50
	// carryScannerDustResidualFraction 残腿期望成本 = 较大腿 min-notional × 此系数(偶发遗留,非全额)。
	carryScannerDustResidualFraction = 0.05
)

// CrossVenueSpotPerpFundingSnap 是某所某 symbol 的永续资金费快照(扫描输入)。
type CrossVenueSpotPerpFundingSnap struct {
	Rate            float64   `json:"rate"`
	IntervalHours   int       `json:"interval_hours"`
	NextFundingTime time.Time `json:"next_funding_time"`
	// SpikeThreshold 尖峰衰减阈值(通常为历史 |rate| p90); <=0 时不做尖峰裁剪。
	SpikeThreshold float64 `json:"spike_threshold,omitempty"`
	// FundingHistorySamples 滚动费率历史样本数(软信号,用于置信度 sizing)。
	FundingHistorySamples int `json:"funding_history_samples,omitempty"`
}

// CrossVenueSpotPerpMarketSnap 是某所某 symbol 的现货/永续中间价与深度容量(扫描输入)。
type CrossVenueSpotPerpMarketSnap struct {
	SpotMid       float64 `json:"spot_mid"`
	PerpMid       float64 `json:"perp_mid"`
	SpotDepthUSDT float64 `json:"spot_depth_usdt"`
	PerpDepthUSDT float64 `json:"perp_depth_usdt"`
	HasSpot       bool    `json:"has_spot"`
	HasPerp       bool    `json:"has_perp"`
	// SpotDepth/PerpDepth 决策期二次拉取的完整盘口(可选);为空时流动性自适应仅依赖 24h 量项。
	SpotDepth *venue.Depth `json:"spot_depth,omitempty"`
	PerpDepth *venue.Depth `json:"perp_depth,omitempty"`
	// Volume24hSpotUSDT/Volume24hPerpUSDT 24h 成交额(USDT);无数据时参与率项跳过。
	Volume24hSpotUSDT float64 `json:"volume_24h_spot_usdt,omitempty"`
	Volume24hPerpUSDT float64 `json:"volume_24h_perp_usdt,omitempty"`
	// 扫描期 instruments 元数据(供执行层量化,非热路径拉取)。
	SpotTickSize    float64 `json:"spot_tick_size,omitempty"`
	SpotStepSize    float64 `json:"spot_step_size,omitempty"`
	SpotMinNotional float64 `json:"spot_min_notional,omitempty"`
	PerpTickSize    float64 `json:"perp_tick_size,omitempty"`
	PerpStepSize    float64 `json:"perp_step_size,omitempty"`
	PerpMinNotional float64 `json:"perp_min_notional,omitempty"`
}

// CrossVenueSpotPerpScannerConfig 跨所 spot-perp 扫描器成本与风控参数(v1 保守默认)。
type CrossVenueSpotPerpScannerConfig struct {
	Exec CrossVenueExecutorConfig `json:"exec"`

	SpotTakerFeeBps           float64            `json:"spot_taker_fee_bps"`
	PeakFundingHaircut        float64            `json:"peak_funding_haircut"`
	BasisConvergenceFraction  float64            `json:"basis_convergence_fraction"`
	BasisWideningStressBps    float64            `json:"basis_widening_stress_bps"`
	RebalanceTransferCostBps  float64            `json:"rebalance_transfer_cost_bps"`
	RebalanceLatencyHours     float64            `json:"rebalance_latency_hours"`
	PerpMaintenanceMarginRate float64            `json:"perp_maintenance_margin_rate"`
	PerpWalletBufferRatio     float64            `json:"perp_wallet_buffer_ratio"`
	ReserveUSDTByVenue        map[string]float64 `json:"reserve_usdt_by_venue,omitempty"`
	DefaultReserveUSDT        float64            `json:"default_reserve_usdt"`
	ExpectedHoldHours         float64            `json:"expected_hold_hours"`
	FundingCaptureProbability float64            `json:"funding_capture_probability"`
	PriceShockPcts            []float64          `json:"price_shock_pcts"`
}

// DefaultCrossVenueSpotPerpScannerConfig 返回 v1 保守默认(对齐 go_no_go 口径)。
func DefaultCrossVenueSpotPerpScannerConfig(exec CrossVenueExecutorConfig) CrossVenueSpotPerpScannerConfig {
	return CrossVenueSpotPerpScannerConfig{
		Exec:                      exec,
		SpotTakerFeeBps:           crossVenueSpotPerpDefaultSpotFeeBps,
		PeakFundingHaircut:        crossVenueSpotPerpDefaultPeakFundingHaircut,
		BasisConvergenceFraction:  crossVenueSpotPerpDefaultBasisConvergence,
		BasisWideningStressBps:    crossVenueSpotPerpDefaultBasisWidenStressBps,
		RebalanceTransferCostBps:  crossVenueSpotPerpDefaultRebalanceCostBps,
		RebalanceLatencyHours:     crossVenueSpotPerpDefaultRebalanceLatencyH,
		PerpMaintenanceMarginRate: crossVenueSpotPerpDefaultPerpMaintMarginRate,
		PerpWalletBufferRatio:     crossVenueSpotPerpDefaultPerpWalletBuffer,
		PriceShockPcts:            []float64{0.20, 0.30},
	}
}

// CrossVenueSpotPerpScanInput 是纯函数扫描器输入。
type CrossVenueSpotPerpScanInput struct {
	SnapshotID           string
	Now                  time.Time
	Symbols              []string
	FundingByVenue       map[string]map[string]CrossVenueSpotPerpFundingSnap
	MarketsByVenue       map[string]map[string]CrossVenueSpotPerpMarketSnap
	AvailableUSDTByVenue map[string]float64
	Config               CrossVenueSpotPerpScannerConfig
	// FundingRateVolPerHourByVenue 永续腿 venue→symbol→每小时归一化费率 stddev;缺失视为冷启动(保守缩窗)。
	FundingRateVolPerHourByVenue map[string]map[string]float64
}

// crossVenueCarryFunnelStats 跨所 carry 扫描漏斗计数(每轮 rollup 一次,非逐候选刷屏)。
type crossVenueCarryFunnelStats struct {
	Tag string

	SymbolsSeen        int
	NoSpotVenues       int
	NoPerpVenues       int
	NoBestPerp         int
	PairSkipSameVenue  int
	PairAttempts       int
	BuildMarketInvalid int
	BuildInstrument    int
	BuildMidDev        int
	BuildNotional      int
	BuildMarginStress  int
	BuildHoldHours     int
	BuildSettlements   int
	BuildFundingWindow int
	BuildFundingEV     int
	BuildConfSizing    int
	BuildNotBorrowable int

	Emitted         int
	EmittedRejected int
	EmittedTradable int
}

func (s *crossVenueCarryFunnelStats) recordEmit(opp UnifiedOpportunity) {
	if s == nil {
		return
	}
	s.Emitted++
	if len(opp.RejectReasons) == 0 {
		s.EmittedTradable++
		return
	}
	s.EmittedRejected++
}

func logCrossVenueCarryFunnel(stats crossVenueCarryFunnelStats) {
	crossVenueCarryFunnelLogFn(stats)
}

// crossVenueCarryFunnelLogFn 默认写标准 log;单测可临时替换以避免并行测试竞态。
var crossVenueCarryFunnelLogFn = func(stats crossVenueCarryFunnelStats) {
	log.Printf(
		"[carry-funnel] %s symbols=%d no_spot_venues=%d no_perp_venues=%d no_best_perp=%d pair_skip_same_venue=%d pair_attempts=%d "+
			"build_market=%d build_instrument=%d build_mid_dev=%d build_notional=%d build_margin_stress=%d build_hold_hours=%d build_settlements=%d "+
			"build_funding_window=%d build_funding_ev=%d build_conf_sizing=%d build_not_borrowable=%d emitted=%d emitted_rejected=%d emitted_tradable=%d",
		stats.Tag,
		stats.SymbolsSeen, stats.NoSpotVenues, stats.NoPerpVenues, stats.NoBestPerp, stats.PairSkipSameVenue, stats.PairAttempts,
		stats.BuildMarketInvalid, stats.BuildInstrument, stats.BuildMidDev, stats.BuildNotional, stats.BuildMarginStress,
		stats.BuildHoldHours, stats.BuildSettlements,
		stats.BuildFundingWindow, stats.BuildFundingEV, stats.BuildConfSizing, stats.BuildNotBorrowable,
		stats.Emitted, stats.EmittedRejected, stats.EmittedTradable,
	)
}

// ScanCrossVenueSpotPerpOpportunities 扫描跨所 spot-perp 机会(纯函数, plan-only)。
// v1: 仅正费率 carry(A 所买现货 + B 所空永续), 不借币、不做空现货。
func ScanCrossVenueSpotPerpOpportunities(in CrossVenueSpotPerpScanInput) []UnifiedOpportunity {
	cfg := normalizeCrossVenueSpotPerpScannerConfig(in.Config)
	now := in.Now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	symbols := in.Symbols
	if len(symbols) == 0 {
		symbols = crossVenueSpotPerpCollectSymbols(in.FundingByVenue, in.MarketsByVenue)
	}

	out := make([]UnifiedOpportunity, 0)
	funnel := crossVenueCarryFunnelStats{Tag: "cvsp"}
	drops := newScannerInstrumentDropLog()
	for _, sym := range symbols {
		sym = strings.TrimSpace(sym)
		if sym == "" {
			continue
		}
		funnel.SymbolsSeen++
		spotVenues := crossVenueSpotPerpSpotBuyVenues(in.MarketsByVenue, sym, drops)
		perpVenues := crossVenueSpotPerpPositivePerpVenues(in.FundingByVenue, in.MarketsByVenue, sym, drops)
		if len(spotVenues) == 0 {
			funnel.NoSpotVenues++
			continue
		}
		if len(perpVenues) == 0 {
			funnel.NoPerpVenues++
			continue
		}
		bestPerpVenue, bestRate := crossVenueSpotPerpBestPositivePerpVenue(perpVenues)
		if bestPerpVenue == "" || bestRate <= 0 {
			funnel.NoBestPerp++
			continue
		}
		for _, spotVenue := range spotVenues {
			if spotVenue == bestPerpVenue {
				funnel.PairSkipSameVenue++
				continue
			}
			funnel.PairAttempts++
			opp, ok := buildCrossVenueSpotPerpOpportunity(
				in.SnapshotID, now, sym, spotVenue, bestPerpVenue,
				in.FundingByVenue, in.MarketsByVenue, in.AvailableUSDTByVenue, cfg,
				in.FundingRateVolPerHourByVenue, &funnel,
			)
			if ok {
				out = append(out, opp)
				funnel.recordEmit(opp)
			}
		}
	}

	logCrossVenueCarryFunnel(funnel)
	sort.Slice(out, func(i, j int) bool {
		if out[i].ExpectedNetEVUSDT == out[j].ExpectedNetEVUSDT {
			return out[i].ID < out[j].ID
		}
		return out[i].ExpectedNetEVUSDT > out[j].ExpectedNetEVUSDT
	})
	return out
}

// AppendCrossVenueSpotPerpCandidates best-effort 写入 records/opportunities/unified_candidates.jsonl(plan-only)。
func AppendCrossVenueSpotPerpCandidates(recordDir, cycleID string, execCfg CrossVenueExecutorConfig, opps []UnifiedOpportunity) {
	if strings.TrimSpace(cycleID) == "" || len(opps) == 0 {
		return
	}
	base := strings.TrimSpace(recordDir)
	if base == "" {
		base = "records"
	}
	path := filepath.Join(opportunitiesRecordDir(base), unifiedCandidatesLogFileName)
	for _, opp := range opps {
		notional := crossVenueSpotPerpLegNotional(opp)
		rec := unifiedCandidateLogFromOpportunity(execCfg, cycleID, opp, len(opp.RejectReasons) == 0, notional)
		_ = appendJSONLRecord(path, rec)
	}
}

func normalizeCrossVenueSpotPerpScannerConfig(cfg CrossVenueSpotPerpScannerConfig) CrossVenueSpotPerpScannerConfig {
	if cfg.Exec.MaxPositionUSDT <= 0 {
		cfg.Exec.MaxPositionUSDT = 50
	}
	if cfg.Exec.MaxEffectiveLeverage <= 0 {
		cfg.Exec.MaxEffectiveLeverage = 2
	}
	if cfg.Exec.MarginSafetyFactor <= 0 {
		cfg.Exec.MarginSafetyFactor = 1.5
	}
	if cfg.Exec.MidPriceDeviationMax <= 0 {
		cfg.Exec.MidPriceDeviationMax = 0.03
	}
	if cfg.Exec.DepthUsageRatio <= 0 {
		cfg.Exec.DepthUsageRatio = 0.20
	}
	if cfg.Exec.EVExpectedSlippageBps <= 0 {
		cfg.Exec.EVExpectedSlippageBps = 3
	}
	if cfg.SpotTakerFeeBps <= 0 {
		cfg.SpotTakerFeeBps = crossVenueSpotPerpDefaultSpotFeeBps
	}
	if cfg.PeakFundingHaircut <= 0 {
		cfg.PeakFundingHaircut = crossVenueSpotPerpDefaultPeakFundingHaircut
	}
	if cfg.BasisConvergenceFraction <= 0 {
		cfg.BasisConvergenceFraction = crossVenueSpotPerpDefaultBasisConvergence
	}
	if cfg.BasisWideningStressBps <= 0 {
		cfg.BasisWideningStressBps = crossVenueSpotPerpDefaultBasisWidenStressBps
	}
	if cfg.RebalanceTransferCostBps <= 0 {
		cfg.RebalanceTransferCostBps = crossVenueSpotPerpDefaultRebalanceCostBps
	}
	if cfg.RebalanceLatencyHours <= 0 {
		cfg.RebalanceLatencyHours = crossVenueSpotPerpDefaultRebalanceLatencyH
	}
	if cfg.PerpMaintenanceMarginRate <= 0 {
		cfg.PerpMaintenanceMarginRate = crossVenueSpotPerpDefaultPerpMaintMarginRate
	}
	if cfg.PerpWalletBufferRatio <= 0 {
		cfg.PerpWalletBufferRatio = crossVenueSpotPerpDefaultPerpWalletBuffer
	}
	if len(cfg.PriceShockPcts) == 0 {
		cfg.PriceShockPcts = []float64{0.20, 0.30}
	}
	if cfg.FundingCaptureProbability <= 0 {
		cfg.FundingCaptureProbability = carryScannerDefaultFundingCaptureProbability
	}
	return cfg
}

func carryScannerNormalizedFundingCaptureProbability(p float64) float64 {
	if p <= 0 {
		return carryScannerDefaultFundingCaptureProbability
	}
	if p < carryScannerMinFundingCaptureProbability {
		return carryScannerMinFundingCaptureProbability
	}
	if p > 1 {
		return 1
	}
	return p
}

func carryScannerExpectedFundingUSDT(notional, effectiveRate float64, settlements int, captureProb float64) float64 {
	if notional <= 0 || effectiveRate <= 0 || settlements <= 0 {
		return 0
	}
	return notional * effectiveRate * float64(settlements) * carryScannerNormalizedFundingCaptureProbability(captureProb)
}

// carryScannerDustResidualCostUSDT 估计开/平仓因步进取整而偶发遗留残腿的期望成本。
// 残腿期望损失 ≈ 偶发概率 × 残余名义,远小于单腿 min-notional(开仓侧对齐 P2⑤ 后更罕见)。
// 故取"较大腿 min-notional × 小系数",而非两腿 min-notional 全额累加(后者会把每笔小仓 EV 直接压成负)。
func carryScannerDustResidualCostUSDT(minNotionals []float64, perpPerpSymmetric bool) float64 {
	maxMN := 0.0
	for _, mn := range minNotionals {
		if mn > maxMN {
			maxMN = mn
		}
	}
	if maxMN <= 0 {
		return 0
	}
	frac := carryScannerDustResidualFraction
	if perpPerpSymmetric {
		// 两腿同为永续、步进对称,残腿概率更低,再打折。
		frac *= carryScannerPerpPerpDustLegFraction
	}
	return maxMN * frac
}

func carryScannerPerpPerpDustResidualCostUSDT() float64 {
	return carryScannerDustResidualCostUSDT(
		[]float64{crossVenueSpotPerpMinTradableNotionalUSDT, crossVenueSpotPerpMinTradableNotionalUSDT},
		true,
	)
}

func crossVenueSpotPerpCollectSymbols(funding map[string]map[string]CrossVenueSpotPerpFundingSnap, markets map[string]map[string]CrossVenueSpotPerpMarketSnap) []string {
	seen := make(map[string]struct{})
	for _, bySym := range funding {
		for sym := range bySym {
			seen[sym] = struct{}{}
		}
	}
	for _, bySym := range markets {
		for sym := range bySym {
			seen[sym] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for sym := range seen {
		out = append(out, sym)
	}
	sort.Strings(out)
	return out
}

func crossVenueSpotPerpSpotBuyVenues(markets map[string]map[string]CrossVenueSpotPerpMarketSnap, sym string, drops *scannerInstrumentDropLog) []string {
	out := make([]string, 0)
	for venueName, bySym := range markets {
		m, ok := bySym[sym]
		if !ok {
			continue
		}
		if !scannerSpotLegEligible(m, "cvsp", sym, venueName, drops) {
			continue
		}
		out = append(out, venueName)
	}
	sort.Strings(out)
	return out
}

type crossVenueSpotPerpPerpCandidate struct {
	venue string
	rate  float64
}

func crossVenueSpotPerpPositivePerpVenues(
	funding map[string]map[string]CrossVenueSpotPerpFundingSnap,
	markets map[string]map[string]CrossVenueSpotPerpMarketSnap,
	sym string,
	drops *scannerInstrumentDropLog,
) []crossVenueSpotPerpPerpCandidate {
	out := make([]crossVenueSpotPerpPerpCandidate, 0)
	for venueName, bySym := range funding {
		fund, ok := bySym[sym]
		if !ok || fund.Rate <= 0 {
			continue
		}
		mkt, ok := markets[venueName][sym]
		if !ok {
			continue
		}
		if !scannerPerpLegEligible(mkt, "cvsp", sym, venueName, drops) {
			continue
		}
		out = append(out, crossVenueSpotPerpPerpCandidate{venue: venueName, rate: fund.Rate})
	}
	return out
}

func crossVenueSpotPerpBestPositivePerpVenue(candidates []crossVenueSpotPerpPerpCandidate) (string, float64) {
	bestVenue := ""
	bestRate := 0.0
	for _, c := range candidates {
		if c.rate > bestRate || (c.rate == bestRate && (bestVenue == "" || c.venue < bestVenue)) {
			bestVenue = c.venue
			bestRate = c.rate
		}
	}
	return bestVenue, bestRate
}

func buildCrossVenueSpotPerpOpportunity(
	snapshotID string,
	now time.Time,
	symbol, spotVenue, perpVenue string,
	funding map[string]map[string]CrossVenueSpotPerpFundingSnap,
	markets map[string]map[string]CrossVenueSpotPerpMarketSnap,
	available map[string]float64,
	cfg CrossVenueSpotPerpScannerConfig,
	fundingVolByVenue map[string]map[string]float64,
	funnel *crossVenueCarryFunnelStats,
) (UnifiedOpportunity, bool) {
	spotMkt := markets[spotVenue][symbol]
	perpMkt := markets[perpVenue][symbol]
	fund := funding[perpVenue][symbol]

	if !spotMkt.HasSpot || spotMkt.SpotMid <= 0 || !perpMkt.HasPerp || perpMkt.PerpMid <= 0 || fund.Rate <= 0 {
		if funnel != nil {
			funnel.BuildMarketInvalid++
		}
		return UnifiedOpportunity{}, false
	}
	if !scannerBothLegsInstrumentValid(spotMkt, perpMkt) {
		if funnel != nil {
			funnel.BuildInstrument++
		}
		return UnifiedOpportunity{}, false
	}

	midDev := math.Abs(perpMkt.PerpMid/spotMkt.SpotMid - 1)
	if midDev > cfg.Exec.MidPriceDeviationMax {
		if funnel != nil {
			funnel.BuildMidDev++
		}
		return UnifiedOpportunity{}, false
	}

	notional := crossVenueSpotPerpTradableNotionalForCrossVenue(
		cfg, spotMkt, perpMkt, spotVenue, perpVenue, available,
		func(v string) float64 { return crossVenueSpotPerpVenueReserve(v, cfg) },
	)
	if notional <= 0 {
		if funnel != nil {
			funnel.BuildNotional++
		}
		return crossVenueSpotPerpRejectedOpportunity(snapshotID, symbol, spotVenue, perpVenue, singleVenueSpotPerpRejectInsufficientDepth, fund, now, fund.Rate), true
	}

	if crossVenueSpotPerpPerpLiquidatedAtShock(cfg, notional) {
		if funnel != nil {
			funnel.BuildMarginStress++
		}
		return crossVenueSpotPerpRejectedOpportunity(snapshotID, symbol, spotVenue, perpVenue, singleVenueSpotPerpRejectMarginStress, fund, now, fund.Rate), true
	}

	holdHours := cfg.ExpectedHoldHours
	if holdHours <= 0 {
		holdHours = crossVenueSpotPerpExpectedHoldHours(now, fund)
	}
	if holdHours <= 0 {
		if funnel != nil {
			funnel.BuildHoldHours++
		}
		return UnifiedOpportunity{}, false
	}

	settlements := crossVenueSpotPerpFundingSettlementCount(now, holdHours, fund.IntervalHours, fund.NextFundingTime)
	if settlements <= 0 {
		if funnel != nil {
			funnel.BuildSettlements++
		}
		return UnifiedOpportunity{}, false
	}

	effectiveRate := crossVenueSpotPerpEffectiveFundingRate(fund.Rate, fund.SpikeThreshold, cfg.PeakFundingHaircut, cfg.Exec.CarryNoHistoryHaircut)

	entryBasisUSDT := crossVenueSpotPerpEntryBasisUSDT(spotMkt.SpotMid, perpMkt.PerpMid, notional)
	expectedBasisPnL := crossVenueSpotPerpExpectedBasisPnL(entryBasisUSDT, notional, cfg)

	openCost := crossVenueSpotPerpOpenCostUSDT(cfg, spotVenue, perpVenue, notional)
	closeCost := openCost
	slippage := crossVenueSpotPerpSlippageUSDT(cfg, notional, spotMkt, perpMkt)

	vol, volOK := carryFundingVolFromMap(fundingVolByVenue, perpVenue, symbol)
	entrySlippage := carryScannerEntrySlippageUSDT(cfg, notional, spotMkt, perpMkt)
	if ok, reason := carryScannerFundingEntryEligible(now, fund, holdHours, notional, effectiveRate, expectedBasisPnL, openCost, entrySlippage, cfg.Exec.FundingEntryGate(), vol, volOK); !ok {
		if funnel != nil {
			switch reason {
			case carryScannerRejectFundingWindowNotReached:
				funnel.BuildFundingWindow++
			default:
				funnel.BuildFundingEV++
			}
		}
		return UnifiedOpportunity{}, false
	}

	maxNotional := notional
	notional, _, ok := carrySizedNotionalAfterConfidence(maxNotional, cfg.Exec, fund, vol, volOK)
	if !ok {
		if funnel != nil {
			funnel.BuildConfSizing++
		}
		return UnifiedOpportunity{}, false
	}

	expectedFunding := carryScannerExpectedFundingUSDT(notional, effectiveRate, settlements, cfg.FundingCaptureProbability)
	entryBasisUSDT = crossVenueSpotPerpEntryBasisUSDT(spotMkt.SpotMid, perpMkt.PerpMid, notional)
	expectedBasisPnL = crossVenueSpotPerpExpectedBasisPnL(entryBasisUSDT, notional, cfg)
	openCost = crossVenueSpotPerpOpenCostUSDT(cfg, spotVenue, perpVenue, notional)
	closeCost = openCost
	slippage = crossVenueSpotPerpSlippageUSDT(cfg, notional, spotMkt, perpMkt)
	spotCashRequired := notional
	perpMarginRequired := crossVenueSpotPerpPerpMarginRequired(cfg, notional)
	rebalancePenalty := 0.0
	requiresRebalance := crossVenueSpotPerpRequiresRebalance(spotVenue, perpVenue, spotCashRequired, perpMarginRequired, available, cfg)
	if requiresRebalance {
		rebalancePenalty = notional * cfg.RebalanceTransferCostBps / 10000
	}
	spotLegInstr := legInstrumentFields(spotMkt.SpotTickSize, spotMkt.SpotStepSize, spotMkt.SpotMinNotional)
	perpLegInstr := legInstrumentFields(perpMkt.PerpTickSize, perpMkt.PerpStepSize, perpMkt.PerpMinNotional)
	closeCost += carryScannerDustResidualCostUSDT([]float64{spotLegInstr.minNotional, perpLegInstr.minNotional}, false)
	netEV := expectedFunding + expectedBasisPnL - openCost - closeCost - slippage - rebalancePenalty
	refPrice := (spotMkt.SpotMid + perpMkt.PerpMid) / 2
	qty := notional / refPrice

	dataQuality := crossVenueSpotPerpDataQualityScore(fund)
	executionScore := crossVenueExecutionScore(slippage, notional)
	liquidityScore := crossVenueSpotPerpLiquidityScore(spotMkt, perpMkt, notional, holdHours)
	riskScore := crossVenueSpotPerpRiskScore(cfg, requiresRebalance, midDev, dataQuality)

	totalCapital := spotCashRequired + perpMarginRequired
	capitalEfficiency := 0.0
	if totalCapital > 0 {
		capitalEfficiency = netEV / totalCapital
	}

	rejectReasons := make([]string, 0)
	if requiresRebalance {
		rejectReasons = append(rejectReasons, "requires_rebalance")
	}
	if netEV <= 0 {
		rejectReasons = append(rejectReasons, "net_ev<=0")
	}

	opp := UnifiedOpportunity{
		ID:           unifiedOpportunityID(UnifiedStrategyCrossVenueSpotPerp, symbol, snapshotID, spotVenue, perpVenue),
		StrategyType: UnifiedStrategyCrossVenueSpotPerp,
		Symbol:       symbol,
		Legs: []UnifiedOpportunityLeg{
			{Venue: spotVenue, Market: "spot", Side: "buy", Qty: qty, Notional: notional, TickSize: spotLegInstr.tick, StepSize: spotLegInstr.step, MinNotional: spotLegInstr.minNotional},
			{Venue: perpVenue, Market: "perp", Side: "short", Qty: qty, Notional: notional, TickSize: perpLegInstr.tick, StepSize: perpLegInstr.step, MinNotional: perpLegInstr.minNotional},
		},
		ExpectedFundingUSDT:      expectedFunding,
		ExpectedOpenCostUSDT:     openCost,
		ExpectedCloseCostUSDT:    closeCost,
		ExpectedSlippageUSDT:     slippage,
		ExpectedBorrowCostUSDT:   0,
		ExpectedRollbackCostUSDT: rebalancePenalty,
		ExpectedNetEVUSDT:        netEV,
		ExpectedHoldHours:        holdHours,
		CapitalRequiredByVenue: map[string]float64{
			spotVenue: spotCashRequired,
			perpVenue: perpMarginRequired,
		},
		RiskScore:              riskScore,
		ExecutionScore:         executionScore,
		LiquidityScore:         liquidityScore,
		DataQualityScore:       dataQuality,
		CapitalEfficiencyScore: capitalEfficiency,
		RejectReasons:          rejectReasons,
		RequiresRebalance:      requiresRebalance,
	}
	applyUnifiedFundingSnapDisplay(&opp, fund, now, fund.Rate)
	return opp, true
}

func crossVenueSpotPerpRejectedOpportunity(snapshotID, symbol, spotVenue, perpVenue, reason string, fund CrossVenueSpotPerpFundingSnap, now time.Time, displayRate float64) UnifiedOpportunity {
	return unifiedRejectedOpportunityWithFunding(
		UnifiedStrategyCrossVenueSpotPerp, snapshotID, symbol, spotVenue, perpVenue, reason, fund, now, displayRate,
	)
}

func crossVenueSpotPerpTradableNotional(cfg CrossVenueSpotPerpScannerConfig, spotMkt, perpMkt CrossVenueSpotPerpMarketSnap) float64 {
	maxNotional := cfg.Exec.MaxPositionUSDT
	if maxNotional <= 0 {
		maxNotional = 50
	}
	liqCap := crossVenueSpotPerpLiquidityNotionalCap(cfg.Exec, spotMkt, perpMkt)
	if cfg.Exec.SizingMaxSlippageBps > 0 || cfg.Exec.SizingMaxParticipationPct24h > 0 {
		maxNotional = applyLiquiditySizingCap(maxNotional, liqCap, resolvedSizingConservativeNoDepthFactor(cfg.Exec))
	} else {
		maxNotional = applyLiquidityCapToMaxPosition(maxNotional, liqCap)
	}
	// sizing_max_slippage_bps=0 时保留旧 depth_usage_ratio 路径:cap 不足 min 则拒绝,否则 size down。
	if cfg.Exec.SizingMaxSlippageBps <= 0 {
		minNotional := crossVenueSpotPerpMinTradableNotionalUSDT
		if spotMkt.SpotDepthUSDT > 0 {
			ratio := cfg.Exec.DepthUsageRatio
			if ratio <= 0 {
				ratio = 0.20
			}
			cap := spotMkt.SpotDepthUSDT * ratio
			if cap+1e-9 < minNotional {
				return 0
			}
			maxNotional = math.Min(maxNotional, cap)
		}
		if perpMkt.PerpDepthUSDT > 0 {
			ratio := cfg.Exec.DepthUsageRatio
			if ratio <= 0 {
				ratio = 0.20
			}
			cap := perpMkt.PerpDepthUSDT * ratio
			if cap+1e-9 < minNotional {
				return 0
			}
			maxNotional = math.Min(maxNotional, cap)
		}
	}
	if maxNotional <= 0 {
		return 0
	}
	return maxNotional
}

func crossVenueSpotPerpPerpMarginCoefficient(cfg CrossVenueSpotPerpScannerConfig) float64 {
	lev := cfg.Exec.MaxEffectiveLeverage
	if lev <= 0 {
		lev = 2
	}
	marginFactor := cfg.Exec.MarginSafetyFactor
	if marginFactor <= 0 {
		marginFactor = 1.5
	}
	return 1/lev*marginFactor + cfg.PerpWalletBufferRatio
}

func crossVenueSpotPerpTradableNotionalForCrossVenue(
	cfg CrossVenueSpotPerpScannerConfig,
	spotMkt, perpMkt CrossVenueSpotPerpMarketSnap,
	spotVenue, perpVenue string,
	available map[string]float64,
	venueReserve func(string) float64,
) float64 {
	base := crossVenueSpotPerpTradableNotional(cfg, spotMkt, perpMkt)
	if base <= 0 {
		return 0
	}
	perpCoeff := crossVenueSpotPerpPerpMarginCoefficient(cfg)
	spotAvail := math.Max(available[spotVenue]-venueReserve(spotVenue), 0)
	perpAvail := math.Max(available[perpVenue]-venueReserve(perpVenue), 0)
	cap := math.Min(base, spotAvail)
	if perpCoeff > 0 {
		cap = math.Min(cap, perpAvail/perpCoeff)
	}
	if cap+1e-9 < crossVenueSpotPerpMinTradableNotionalUSDT {
		return 0
	}
	return cap
}

func crossVenueSpotPerpTradableNotionalForSingleVenue(
	cfg CrossVenueSpotPerpScannerConfig,
	spotMkt, perpMkt CrossVenueSpotPerpMarketSnap,
	venueName string,
	available map[string]float64,
	venueReserve func(string) float64,
) float64 {
	base := crossVenueSpotPerpTradableNotional(cfg, spotMkt, perpMkt)
	if base <= 0 {
		return 0
	}
	coeff := 1 + crossVenueSpotPerpPerpMarginCoefficient(cfg)
	if coeff <= 0 {
		return 0
	}
	avail := math.Max(available[venueName]-venueReserve(venueName), 0)
	cap := math.Min(base, avail/coeff)
	if cap+1e-9 < crossVenueSpotPerpMinTradableNotionalUSDT {
		return 0
	}
	return cap
}

func crossVenueSpotPerpExpectedHoldHours(now time.Time, fund CrossVenueSpotPerpFundingSnap) float64 {
	window := crossVenueComparableWindowHours(now, fund.NextFundingTime, fund.NextFundingTime)
	if window > 0 {
		return window
	}
	interval := fund.IntervalHours
	if interval <= 0 {
		interval = 8
	}
	return float64(interval)
}

func crossVenueSpotPerpFundingSettlementCount(now time.Time, holdHours float64, intervalHours int, nextFunding time.Time) int {
	if holdHours <= 0 {
		return 0
	}
	if intervalHours <= 0 {
		intervalHours = 8
	}
	holdEnd := now.Add(time.Duration(holdHours * float64(time.Hour)))
	interval := time.Duration(intervalHours) * time.Hour
	t := nextFunding
	if t.IsZero() || !t.After(now) {
		t = now.Add(interval)
	} else if interval > 0 {
		// nextFunding 锚定结算网格;若距 now 超过一个 interval,回退到 (now, holdEnd] 内首个结算点。
		for t.Sub(now) > interval {
			t = t.Add(-interval)
		}
	}
	count := 0
	for !t.After(holdEnd) {
		if t.After(now) {
			count++
		}
		t = t.Add(interval)
	}
	return count
}

// carryScannerEntrySlippageUSDT 开仓窗口 EV 闸仅计开仓两腿滑点(非四腿 round-trip)。
func carryScannerEntrySlippageUSDT(cfg CrossVenueSpotPerpScannerConfig, notional float64, spotMkt, perpMkt CrossVenueSpotPerpMarketSnap) float64 {
	legs := crossVenueSpotPerpEVSlippageLegs(spotMkt, perpMkt)
	if len(legs) >= 2 && (spotMkt.SpotDepth != nil || perpMkt.PerpDepth != nil) {
		return crossVenueExpectedSlippageUSDTWithLegs(cfg.Exec, notional, legs[:2])
	}
	return crossVenueExpectedSlippageUSDT(cfg.Exec, notional, 2)
}

func crossVenueSpotPerpEffectiveFundingRate(rate, spikeThreshold, peakHaircut, noHistoryHaircut float64) float64 {
	if rate <= 0 {
		return 0
	}
	if spikeThreshold <= 0 {
		if noHistoryHaircut <= 0 {
			noHistoryHaircut = 1.0
		}
		return rate * noHistoryHaircut
	}
	if peakHaircut <= 0 {
		peakHaircut = crossVenueSpotPerpDefaultPeakFundingHaircut
	}
	if rate <= spikeThreshold {
		return rate
	}
	return spikeThreshold + peakHaircut*(rate-spikeThreshold)
}

func crossVenueSpotPerpEntryBasisUSDT(spotMid, perpMid, notional float64) float64 {
	if spotMid <= 0 || notional <= 0 {
		return 0
	}
	return (perpMid/spotMid - 1) * notional
}

func crossVenueSpotPerpExpectedBasisPnL(entryBasisUSDT, notional float64, cfg CrossVenueSpotPerpScannerConfig) float64 {
	stress := notional * cfg.BasisWideningStressBps / 10000
	if entryBasisUSDT >= 0 {
		return -stress
	}
	return entryBasisUSDT - stress
}

func crossVenueSpotPerpOpenCostUSDT(cfg CrossVenueSpotPerpScannerConfig, spotVenue, perpVenue string, notional float64) float64 {
	if notional <= 0 {
		return 0
	}
	spotFee := cfg.SpotTakerFeeBps
	perpFee := crossVenueTakerFeeBps(cfg.Exec, perpVenue)
	return notional * (spotFee + perpFee) / 10000
}

func crossVenueSpotPerpSlippageUSDT(cfg CrossVenueSpotPerpScannerConfig, notional float64, spotMkt, perpMkt CrossVenueSpotPerpMarketSnap) float64 {
	legs := crossVenueSpotPerpEVSlippageLegs(spotMkt, perpMkt)
	if len(legs) > 0 && (spotMkt.SpotDepth != nil || perpMkt.PerpDepth != nil) {
		return crossVenueExpectedSlippageUSDTWithLegs(cfg.Exec, notional, legs)
	}
	return crossVenueExpectedSlippageUSDT(cfg.Exec, notional, 4)
}

func crossVenueSpotPerpPerpMarginRequired(cfg CrossVenueSpotPerpScannerConfig, notional float64) float64 {
	margin := crossVenuePerVenueMarginUSDT(cfg.Exec, notional)
	buffer := notional * cfg.PerpWalletBufferRatio
	return margin + buffer
}

func crossVenueSpotPerpPerpLiquidatedAtShock(cfg CrossVenueSpotPerpScannerConfig, notional float64) bool {
	for _, shock := range cfg.PriceShockPcts {
		if shock <= 0 {
			continue
		}
		if crossVenueSpotPerpPerpWalletLiquidated(cfg, notional, shock) {
			return true
		}
	}
	return false
}

func crossVenueSpotPerpPerpWalletLiquidated(cfg CrossVenueSpotPerpScannerConfig, notional, shockPct float64) bool {
	if notional <= 0 {
		return false
	}
	lev := cfg.Exec.MaxEffectiveLeverage
	if lev <= 0 {
		lev = 2
	}
	initialMargin := notional / lev
	buffer := notional * cfg.PerpWalletBufferRatio
	maintenance := notional * cfg.PerpMaintenanceMarginRate
	equity := initialMargin + buffer - shockPct*notional
	return equity < maintenance
}

func crossVenueSpotPerpVenueReserve(venueName string, cfg CrossVenueSpotPerpScannerConfig) float64 {
	if cfg.ReserveUSDTByVenue != nil {
		if v, ok := cfg.ReserveUSDTByVenue[venueName]; ok {
			return math.Max(v, 0)
		}
	}
	return math.Max(cfg.DefaultReserveUSDT, 0)
}

func crossVenueSpotPerpRequiresRebalance(
	spotVenue, perpVenue string,
	spotCashRequired, perpMarginRequired float64,
	available map[string]float64,
	cfg CrossVenueSpotPerpScannerConfig,
) bool {
	spotAvail := available[spotVenue] - crossVenueSpotPerpVenueReserve(spotVenue, cfg)
	perpAvail := available[perpVenue] - crossVenueSpotPerpVenueReserve(perpVenue, cfg)
	return spotAvail+1e-9 < spotCashRequired || perpAvail+1e-9 < perpMarginRequired
}

func crossVenueSpotPerpDataQualityScore(fund CrossVenueSpotPerpFundingSnap) float64 {
	return carryFundingHistoryQuality(fund)
}

func crossVenueSpotPerpLiquidityScore(spotMkt, perpMkt CrossVenueSpotPerpMarketSnap, notional, holdHours float64) float64 {
	score := crossVenueLiquidityScore(notional, holdHours)
	if spotMkt.SpotDepthUSDT > 0 && spotMkt.SpotDepthUSDT*cfgDepthUsageDefault() < notional {
		score *= 0.8
	}
	if perpMkt.PerpDepthUSDT > 0 && perpMkt.PerpDepthUSDT*cfgDepthUsageDefault() < notional {
		score *= 0.8
	}
	return clamp01(score)
}

func cfgDepthUsageDefault() float64 { return 0.20 }

func crossVenueSpotPerpRiskScore(cfg CrossVenueSpotPerpScannerConfig, requiresRebalance bool, midDev, dataQuality float64) float64 {
	risk := 0.35
	if requiresRebalance {
		risk += 0.20
	}
	risk += clamp01(midDev/cfg.Exec.MidPriceDeviationMax) * 0.15
	risk += clamp01(1-dataQuality) * 0.20
	return clamp01(risk)
}

func crossVenueSpotPerpLegNotional(opp UnifiedOpportunity) float64 {
	for _, leg := range opp.Legs {
		if leg.Notional > 0 {
			return leg.Notional
		}
	}
	return 0
}
