package datadrivenv3

import (
	"errors"
	"math"
)

type Direction int8

const (
	DirectionFlat  Direction = 0
	DirectionLong  Direction = 1
	DirectionShort Direction = -1
)

type FundingSettlement struct {
	TimeMS int64
	Rate   float64
}

type RewardCostInput struct {
	Known                  bool
	RoundTripFeeBPS        float64
	SpreadBPS              float64
	CapitalUSDT            float64
	RollingQuoteVolumeUSDT float64
	BarHigh                float64
	BarLow                 float64
	BarMid                 float64
	StressMultiplier       float64
}

type RewardInput struct {
	BaselineID             string
	Direction              Direction
	EntryPrice             float64
	ExitPrice              float64
	PositionNotional       float64
	EntryMS                int64
	ExitMS                 int64
	DelayBars              int
	Matured                bool
	EOF                    bool
	Funding                []FundingSettlement
	Cost                   RewardCostInput
	Cash                   float64
	MarkedEquity           float64
	RetainedCapitalBase    float64
	TotalAbsNotional       float64
	MaintenanceMarginKnown bool
	MaintenanceReserve     float64
	FrozenCloseCostBPS     float64
	WithdrawalTranches     int
}

type RewardResult struct {
	BaselineID                string    `json:"baseline_id"`
	Direction                 Direction `json:"direction"`
	NoTrade                   bool      `json:"no_trade"`
	Pending                   bool      `json:"pending"`
	EOFDiscarded              bool      `json:"eof_unmatured_discarded"`
	UnknownCost               bool      `json:"unknown_cost"`
	MTM                       float64   `json:"mtm"`
	FundingCashflow           float64   `json:"funding_cashflow"`
	ExecutionCost             float64   `json:"execution_cost"`
	Withdrawals               []float64 `json:"withdrawals"`
	WithdrawalDeferred        bool      `json:"mmr_unknown_withdrawal_deferred"`
	WithdrawalBlockedSolvency bool      `json:"withdrawal_blocked_solvency"`
	RequiredReserve           float64   `json:"required_reserve"`
	FreeEquity                float64   `json:"free_equity"`
	EndCash                   float64   `json:"end_cash"`
	Ruin                      bool      `json:"ruin"`
	NoTopup                   bool      `json:"no_topup"`
	ExchangeLiquidationProof  bool      `json:"exchange_liquidation_proof"`
}

func EvaluateReward(in RewardInput) (RewardResult, error) {
	out := RewardResult{BaselineID: in.BaselineID, Direction: in.Direction, NoTopup: true, EndCash: in.Cash}
	if in.BaselineID == "" || in.Cash < 0 || in.PositionNotional < 0 || in.TotalAbsNotional < 0 || in.WithdrawalTranches < 0 {
		return RewardResult{}, errors.New("invalid reward/account input")
	}
	if in.Direction == DirectionFlat {
		out.NoTrade = true
		return out, nil
	}
	if in.Direction != DirectionLong && in.Direction != DirectionShort {
		return RewardResult{}, errors.New("invalid direction")
	}
	if !in.Matured {
		if in.EOF {
			out.EOFDiscarded = true
		} else {
			out.Pending = true
		}
		return out, nil
	}
	if in.EntryPrice <= 0 || in.ExitPrice <= 0 {
		return RewardResult{}, errors.New("matured trade requires prices")
	}
	if !in.Cost.Known {
		out.UnknownCost = true
		out.NoTrade = true
		return out, nil
	}
	costBPS, err := executionCostBPS(in.Cost)
	if err != nil {
		return RewardResult{}, err
	}
	out.ExecutionCost = in.PositionNotional * costBPS / 10000
	priceReturn := in.ExitPrice/in.EntryPrice - 1
	out.MTM = in.PositionNotional * priceReturn * float64(in.Direction)
	for _, settlement := range in.Funding {
		if settlement.TimeMS > in.EntryMS && settlement.TimeMS <= in.ExitMS {
			out.FundingCashflow -= float64(in.Direction) * in.PositionNotional * settlement.Rate
		}
	}
	equity := in.MarkedEquity + out.MTM + out.FundingCashflow - out.ExecutionCost
	cash := in.Cash + out.MTM + out.FundingCashflow - out.ExecutionCost
	leverageReserve := in.TotalAbsNotional / 5
	maintenanceReserve := in.MaintenanceReserve + in.TotalAbsNotional*in.FrozenCloseCostBPS/10000
	out.RequiredReserve = math.Max(leverageReserve, maintenanceReserve)
	out.FreeEquity = math.Max(0, equity-out.RequiredReserve)
	if in.TotalAbsNotional > 0 && !in.MaintenanceMarginKnown && in.WithdrawalTranches > 0 {
		out.WithdrawalDeferred = true
	} else {
		request := .5 * in.RetainedCapitalBase
		for i := 0; i < in.WithdrawalTranches; i++ {
			free := math.Max(0, equity-out.RequiredReserve)
			withdraw := math.Floor(math.Min(request, free)*1e6) / 1e6
			if withdraw > cash || cash-withdraw < 0 {
				out.WithdrawalBlockedSolvency = true
				withdraw = 0
			}
			out.Withdrawals = append(out.Withdrawals, withdraw)
			equity -= withdraw
			cash -= withdraw
		}
	}
	if cash < 0 {
		out.Ruin = true
		out.EndCash = 0
	} else {
		out.EndCash = cash
	}
	if equity < 0 {
		out.Ruin = true
	}
	out.FreeEquity = math.Max(0, equity-out.RequiredReserve)
	return out, nil
}

func executionCostBPS(in RewardCostInput) (float64, error) {
	if in.RoundTripFeeBPS < 0 || in.SpreadBPS < 0 || in.CapitalUSDT < 0 || in.RollingQuoteVolumeUSDT <= 0 || in.BarMid <= 0 || in.BarHigh < in.BarLow {
		return 0, errors.New("invalid execution cost input")
	}
	fee := in.RoundTripFeeBPS
	if fee == 0 {
		fee = 10
	}
	spread := math.Max(2, in.SpreadBPS)
	impact := math.Min(50, in.CapitalUSDT/math.Max(in.RollingQuoteVolumeUSDT, 1)*10000)
	toxicity := math.Min(100, (in.BarHigh-in.BarLow)/math.Max(in.BarMid, 1e-12)*10000*.5)
	multiplier := in.StressMultiplier
	if multiplier == 0 {
		multiplier = 1
	}
	if multiplier < 1 {
		return 0, errors.New("cost stress multiplier below one")
	}
	return (fee + spread + impact + toxicity) * multiplier, nil
}

// DynamicExecutionCostBPS is the single shared g1/g2 execution-cost primitive.
// Discovery counterfactuals and later chronological replay must use this
// function so entry, exit, trim, and reverse legs cannot drift.
func DynamicExecutionCostBPS(in RewardCostInput) (float64, error) {
	return executionCostBPS(in)
}

// EvaluateBaseline forces every policy and baseline through identical code.
func EvaluateBaseline(baselineID string, in RewardInput) (RewardResult, error) {
	in.BaselineID = baselineID
	return EvaluateReward(in)
}

type SyntheticStress interface {
	EvaluateSynthetic(RewardInput) (RewardResult, error)
}
