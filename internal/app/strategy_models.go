package app

import (
	"time"
)

// MarketCapacitySnapshot 描述当前市场的容量和流动性快照
type MarketCapacitySnapshot struct {
	FuturesOpenInterestNotional float64
	RecentSpotQuoteVolume       float64
	RecentFuturesQuoteVolume    float64
	VisibleSpotAskNotional      float64
	VisibleFuturesBidNotional   float64
	AvailableMetrics            int
}

// FundingSnapshot 描述目标资金费率的预估情况
type FundingSnapshot struct {
	Symbol          string
	NextFundingTime time.Time
	EstimatedRate   float64
	LastFundingRate float64
	BasisRate       float64
	FundingJump     float64
	Direction       fundingCarryDirection
	IsStale         bool
}

// AccountBudget 描述当前可用的资金和敞口预算
type AccountBudget struct {
	AvailableUSDT      float64
	MaxPerSymbolUSDT   float64
	RemainingCycleUSDT float64
	RemainingDailyUSDT float64
}

// RiskGate 描述当前运行环境的安全限制
type RiskGate struct {
	CurrentGear         EngineGear
	IsSymbolCooledDown  bool
	ConsecutiveBreaches int
	MaxBreachLimit      int
}

// StrategyDecision 描述策略层输出的具体意图
type StrategyDecision struct {
	Candidate             FundingCandidate
	Action                StrategyAction
	RejectReason          string
	ConfidenceScore       float64
	TargetNotionalUSDT    float64
	ExpectedNetProfitRate float64
}

type StrategyAction string

const (
	StrategyActionOpen    StrategyAction = "open"
	StrategyActionObserve StrategyAction = "observe"
	StrategyActionReject  StrategyAction = "reject"
)
