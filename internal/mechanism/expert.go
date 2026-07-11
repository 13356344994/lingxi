package mechanism

// Evaluation 是专家对一张事件卡的裁决：要么接单给出论点，要么带理由拒绝。
type Evaluation struct {
	// Accepted 是否接单。
	Accepted bool
	// Thesis 接单时的方向论点（Accepted=false 时为零值）。
	Thesis Thesis
	// RejectReason 拒绝理由（Accepted=true 时为空）。
	RejectReason string
}

// Expert 是机制专家统一接口：消费一张连续事件卡，输出方向论点或拒绝。
// 实现必须是纯函数（无网络、无全局状态），保证可脱网单测与确定性回放。
type Expert interface {
	// Name 返回专家稳定标识（贯穿归因）。
	Name() string
	// Evaluate 对一张事件卡出具裁决。
	Evaluate(card MechanismEventCard) Evaluation
}

// accept 把候选论点跑一遍 Validate 后再决定是否接单：任何缺 payer / 期限 / 可观测证伪器的
// 论点都会在这里被强制降级为拒绝，专家无法绕过。
func accept(t Thesis) Evaluation {
	if ok, reason := t.Validate(); !ok {
		return Evaluation{Accepted: false, RejectReason: reason}
	}
	return Evaluation{Accepted: true, Thesis: t}
}

// reject 构造一个带理由的拒绝裁决。
func reject(reason string) Evaluation {
	return Evaluation{Accepted: false, RejectReason: reason}
}
