package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// state_envelope.go 给 live 台账(未平仓对冲 / 敞口救援 / 日级风控)加「schema 版本 + 内容校验和」头(M-S1.1)。
//
// 目标:让落盘状态从「裸 JSON、无版本、无完整性校验」升级为带契约的信封。M-S1.1 只做两件事——
//   1. 落盘前打上 SchemaVersion + Checksum;
//   2. 提供 classifyStateEnvelope 完整性/版本分级(纯函数,已测),供 M-S1.2 的恢复契约据此区分
//      「自动清理 / 自动重排 / 拒绝接管」。
//
// 向后兼容:旧文件没有这两个字段,反序列化得到零值(version=0、checksum="")即判为 legacy,按既有行为放行,
// 不改变当前任何控制流(零行为变更)。

// liveStateSchemaVersion 是 live 台账的当前 schema 版本。结构发生(影响序列化的)字段变更时递增。
//
// v1 → v2:为统一轮动/路线B 给持仓结构新增字段(opportunity_id / expected_hold_hours /
// next_funding_time / last_funding_sync_at 等)。这类「加字段」改动会改变整体 JSON 序列化字节,
// 使旧版本写出的文件在新序列化下 checksum 必然不一致——历史上这会被误判为 checksum_mismatch 而拒绝接管,
// 导致每次带结构变更的部署都会 brick 掉旧台账。现通过 schema_version 升级 + 「旧版本良性演进」分级解决
// (见 classifyStateEnvelope:version < 当前 视同 legacy 信任并在下次落盘 re-stamp)。
const liveStateSchemaVersion = 2

// checksummableState 是带信封头(版本 + 校验和)的可落盘台账。
type checksummableState interface {
	getSchemaVersion() int
	setSchemaVersion(int)
	getChecksum() string
	setChecksum(string)
}

// computeStateChecksum 计算把 Checksum 字段置空后整体 JSON 的 SHA256(十六进制)。
// 置空保证写入端与校验端对「同一内容」取同一基准,不会把校验和自身计入。
func computeStateChecksum(s checksummableState) (string, error) {
	saved := s.getChecksum()
	s.setChecksum("")
	data, err := json.Marshal(s)
	s.setChecksum(saved)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// stampStateEnvelope 在落盘前打上当前 schema 版本与内容校验和。
func stampStateEnvelope(s checksummableState) error {
	s.setSchemaVersion(liveStateSchemaVersion)
	sum, err := computeStateChecksum(s)
	if err != nil {
		return err
	}
	s.setChecksum(sum)
	return nil
}

// stateEnvelopeStatus 是落盘台账的完整性/版本分级(M-S1.2 恢复契约的输入)。
type stateEnvelopeStatus int

const (
	// stateEnvelopeLegacy 旧文件:无 schema_version/checksum,按既有行为放行。
	stateEnvelopeLegacy stateEnvelopeStatus = iota
	// stateEnvelopeOK 版本兼容且校验和匹配。
	stateEnvelopeOK
	// stateEnvelopeChecksumMismatch 校验和不匹配(疑似损坏/被外部篡改)。
	stateEnvelopeChecksumMismatch
	// stateEnvelopeVersionAhead 文件版本高于本进程支持(疑似被新版本写过,降级运行不应自动接管)。
	stateEnvelopeVersionAhead
	// stateEnvelopeSchemaOlder 文件版本低于本进程支持(旧版本写出的良性演进:本进程结构已新增/调整字段,
	// 旧内容在新序列化下 checksum 必然不一致)。与 legacy 同等信任,可自动接管,接管后在下次落盘 re-stamp 升级。
	stateEnvelopeSchemaOlder
)

func (s stateEnvelopeStatus) String() string {
	switch s {
	case stateEnvelopeLegacy:
		return "legacy"
	case stateEnvelopeOK:
		return "ok"
	case stateEnvelopeChecksumMismatch:
		return "checksum_mismatch"
	case stateEnvelopeVersionAhead:
		return "version_ahead"
	case stateEnvelopeSchemaOlder:
		return "schema_older"
	default:
		return "unknown"
	}
}

// classifyStateEnvelope 对已反序列化的台账信封做完整性/版本分级(纯函数,不改入参的最终状态)。
func classifyStateEnvelope(s checksummableState) (stateEnvelopeStatus, error) {
	version := s.getSchemaVersion()
	checksum := s.getChecksum()
	if version == 0 && checksum == "" {
		return stateEnvelopeLegacy, nil
	}
	if version > liveStateSchemaVersion {
		return stateEnvelopeVersionAhead, nil
	}
	// 文件 schema 版本低于本进程:旧版本写出的良性演进。本进程结构相对旧版本已新增/调整字段,
	// 旧内容在新序列化下 checksum 必然不一致——不据此判损坏,与 legacy(version=0)同等信任接管,
	// 接管后在下次落盘 re-stamp 升级到当前版本。corruption 检测仍对「同版本 checksum 不符」保留。
	if version > 0 && version < liveStateSchemaVersion {
		return stateEnvelopeSchemaOlder, nil
	}
	expected, err := computeStateChecksum(s)
	if err != nil {
		return stateEnvelopeOK, err
	}
	if checksum != "" && checksum != expected {
		return stateEnvelopeChecksumMismatch, nil
	}
	return stateEnvelopeOK, nil
}

// stateEnvelopeAutoRecoverable 报告某分级是否允许「自动接管」(沿用既有恢复逻辑)。
// legacy(旧文件)、ok(版本兼容且校验和匹配)、schema_older(旧版本写出的良性演进)可自动接管;
// checksum_mismatch(同版本损坏/篡改)与 version_ahead(被更新版本写过)不可信,必须人工确认,
// 绝不在不可信状态上自动恢复。
func stateEnvelopeAutoRecoverable(status stateEnvelopeStatus) bool {
	return status == stateEnvelopeLegacy || status == stateEnvelopeOK || status == stateEnvelopeSchemaOlder
}

// guardStateEnvelope 是恢复契约(M-S1.2)的统一闸:对已反序列化的台账信封做完整性/版本分级,
// 凡不可自动接管者返回 error 拒绝自动接管(由启动期调用方据此阻断启动,要求人工核对)。
func guardStateEnvelope(label string, s checksummableState) error {
	status, err := classifyStateEnvelope(s)
	if err != nil {
		return fmt.Errorf("校验%s完整性失败: %w", label, err)
	}
	if !stateEnvelopeAutoRecoverable(status) {
		return fmt.Errorf("%s不可自动接管(envelope=%s schema_version=%d 本进程支持=%d):"+
			"为避免在不可信状态上自动恢复,已拒绝启动。请人工核对该文件——确认与交易所实际持仓/敞口一致,"+
			"或确认确无残留后删除该文件以重置",
			label, status, s.getSchemaVersion(), liveStateSchemaVersion)
	}
	return nil
}
