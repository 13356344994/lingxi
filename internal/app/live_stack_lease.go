package app

import "fmt"

// liveStackLeaseAcquire 供 startLiveStack 获取单写租约;单测可替换以注入模拟持有者。
var liveStackLeaseAcquire = acquireSingleWriterLease

// requiresLiveSingleWriterLease 判断当前装配是否会触达真钱交易循环,需独占单写租约。
// 纯面板或 cross_venue_scanner 只读扫描(未启用 executor 且未启用 unified_rotation)也不强制。
func requiresLiveSingleWriterLease(cfg EngineConfig) bool {
	// 方向栈即使处于 shadow-only 也会持续写行情、影子账本和持仓投影；
	// 同时未来只允许经单一执行门解除封火，因此方向栈始终必须独占。
	if cfg.OKXDirectional.Enabled {
		return true
	}
	if cfg.CrossVenueScanner.Enabled && cfg.CrossVenueExecutor.Enabled {
		return true
	}
	if cfg.UnifiedRotation.Enabled {
		return true
	}
	return false
}

// acquireLiveStackSingleWriterLease 在需要真钱独占写入时装配下单写租约;不需要时返回 (nil, nil)。
// 已有实例持锁时 fail-fast,错误信息便于运维识别双开。
func acquireLiveStackSingleWriterLease(cfg EngineConfig) (*singleWriterLease, error) {
	if !requiresLiveSingleWriterLease(cfg) {
		return nil, nil
	}
	lease, err := liveStackLeaseAcquire(cfg.NetworkConfig.RecordDir)
	if err != nil {
		return nil, fmt.Errorf("已有 lingxi live 实例在运行,拒绝双开: %w", err)
	}
	return lease, nil
}
