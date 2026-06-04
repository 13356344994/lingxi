package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

// liveStateSchemaVersion 是 live 台账的当前 schema 版本。结构发生不兼容变更时递增。
const liveStateSchemaVersion = 1

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
	expected, err := computeStateChecksum(s)
	if err != nil {
		return stateEnvelopeOK, err
	}
	if checksum != "" && checksum != expected {
		return stateEnvelopeChecksumMismatch, nil
	}
	return stateEnvelopeOK, nil
}
