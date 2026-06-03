package app

import "strings"

const currentStrategyVersion = "funding-sniper-v1"

type TraceRecordMetadata struct {
	StrategyVersion string `json:"strategy_version,omitempty"`
	ConfigHash      string `json:"config_hash,omitempty"`
	AppVersion      string `json:"app_version,omitempty"`
	GitCommit       string `json:"git_commit,omitempty"`
}

func buildTraceRecordMetadata(cfg EngineConfig) TraceRecordMetadata {
	return TraceRecordMetadata{
		StrategyVersion: currentStrategyVersion,
		ConfigHash:      cfg.ConfigHash(),
		AppVersion:      strings.TrimSpace(appVersion),
		GitCommit:       strings.TrimSpace(gitCommit),
	}.normalized()
}

func (m TraceRecordMetadata) normalized() TraceRecordMetadata {
	m.StrategyVersion = strings.TrimSpace(m.StrategyVersion)
	m.ConfigHash = strings.TrimSpace(m.ConfigHash)
	m.AppVersion = strings.TrimSpace(m.AppVersion)
	m.GitCommit = strings.TrimSpace(m.GitCommit)
	return m
}
