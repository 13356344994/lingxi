package core

import "strings"

// RunMode 是规范的三态运行模式,取代把 旧模式当生产态的用法。
//
//   - OFF: 不开仓、manage 不下真单、不写真实 position/settlement 台账
//   - CANARY_LIVE: 真钱(规模/采数据标记与 LIVE 区分,后续任务用)
//   - LIVE: 真钱全量
type RunMode string

const (
	RunModeOFF        RunMode = "OFF"
	RunModeCANARYLive RunMode = "CANARY_LIVE"
	RunModeLIVE       RunMode = "LIVE"
)

// ParseRunMode 解析配置/控制面字符串;无法识别时返回 OFF。
func ParseRunMode(raw string) RunMode {
	switch strings.ToUpper(strings.TrimSpace(raw)) {
	case string(RunModeOFF), "0", "FALSE", "OFFLINE":
		return RunModeOFF
	case string(RunModeCANARYLive), "CANARY":
		return RunModeCANARYLive
	case string(RunModeLIVE), "1", "TRUE", "ON":
		return RunModeLIVE
	default:
		return RunModeOFF
	}
}

// IsLiveMoney 是否允许真钱下单(含 CANARY 与 LIVE)。
func (m RunMode) IsLiveMoney() bool {
	return m == RunModeCANARYLive || m == RunModeLIVE
}

// AllowsRealLedgerWrites 是否允许写真实 positions.json / settlements.jsonl。
func (m RunMode) AllowsRealLedgerWrites() bool {
	return m.IsLiveMoney()
}
