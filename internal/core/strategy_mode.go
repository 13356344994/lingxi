package core

import "strings"

// StrategyMode 是统一策略运行模式,一处配置驱动 RunMode(真钱规模)、归因标签与成本闸严格度。
//
//   - collect_data(默认):放松经济闸、采真实成交数据;部署闸+控制面开启真钱时 RunMode=LIVE
//   - canary_profit:经济闸同 collect_data;真钱小规模/采数据标记;RunMode=CANARY_LIVE
//   - profit_seeking:诚实扣真实成本;仅该模式样本进 go/no-go alpha 判定;RunMode=LIVE
type StrategyMode string

const (
	StrategyModeCollectData   StrategyMode = "collect_data"
	StrategyModeCanaryProfit  StrategyMode = "canary_profit"
	StrategyModeProfitSeeking StrategyMode = "profit_seeking"
)

// profitSeeking 诚实成本闸下限(与 defaults.go RiskConfig.MinNetProfitRate 出厂默认对齐)。
const profitSeekingMinNetProfitRate = 0.0020

// defaultMaxScanBasisRate / defaultOpenSpreadAPR 为 profit_seeking 收回的扫描/跨所价差闸默认量级。
const (
	defaultMaxScanBasisRate = 0.0015
	defaultOpenSpreadAPR    = 1.5
)

// ParseStrategyMode 解析配置字符串;空串或未识别时回退 collect_data(保持现状默认)。
func ParseStrategyMode(raw string) StrategyMode {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case string(StrategyModeCanaryProfit):
		return StrategyModeCanaryProfit
	case string(StrategyModeProfitSeeking):
		return StrategyModeProfitSeeking
	default:
		return StrategyModeCollectData
	}
}

// IsValidStrategyMode 校验配置期 strategy_mode 取值(空串视为合法,运行期解析为 collect_data)。
func IsValidStrategyMode(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", string(StrategyModeCollectData), string(StrategyModeCanaryProfit), string(StrategyModeProfitSeeking):
		return true
	default:
		return false
	}
}

// RunModeWhenLiveEnabled 在部署闸与控制面已允许真钱时,按策略模式映射规范 RunMode。
func (m StrategyMode) RunModeWhenLiveEnabled() RunMode {
	if m == StrategyModeCanaryProfit {
		return RunModeCANARYLive
	}
	return RunModeLIVE
}

// ApplyStrategyModeToEconomics 按策略模式覆盖经济闸派生参数(不读写 config.yaml,仅在加载/装配层生效)。
// collect_data / canary_profit:保持 YAML/默认放松值不变;profit_seeking:强制诚实成本闸。
func ApplyStrategyModeToEconomics(cfg *EngineConfig, mode StrategyMode) {
	if cfg == nil {
		return
	}
	if mode != StrategyModeProfitSeeking {
		return
	}
	cfg.StrategyConfig.AutoDeriveFeeReserveEnabled = true
	cfg.StrategyConfig.ImpactReserveInGateEnabled = true
	if cfg.RiskConfig.MinNetProfitRate < profitSeekingMinNetProfitRate {
		cfg.RiskConfig.MinNetProfitRate = profitSeekingMinNetProfitRate
	}
	if cfg.StrategyConfig.MaxScanBasisRate > defaultMaxScanBasisRate {
		cfg.StrategyConfig.MaxScanBasisRate = defaultMaxScanBasisRate
	}
	if cfg.StrategyConfig.FavorableBasisMaxRate > defaultMaxScanBasisRate {
		cfg.StrategyConfig.FavorableBasisMaxRate = defaultMaxScanBasisRate
	}
	if cfg.CrossVenueExecutor.OpenSpreadAPR < defaultOpenSpreadAPR {
		cfg.CrossVenueExecutor.OpenSpreadAPR = defaultOpenSpreadAPR
	}
}
