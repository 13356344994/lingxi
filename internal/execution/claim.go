package execution

// claim.go 实现 per-symbol 原子 claim:任一时刻同一 symbol 至多一个 intent 持有 claim,
// 防止并发决策对同一币重复开仓/重复下单。Quarantine 与 claim 共用锁,保证「隔离期间不允许
// 新 claim」这条不变量在并发下也不会被绕过(plan §3:
// 「未知期间不释放 claim、不允许同币新仓」)。
import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrSymbolClaimed 表示该 symbol 当前已被另一个 owner 持有 claim。
var ErrSymbolClaimed = errors.New("execution: symbol 已被占用 claim")

// ErrSymbolQuarantined 表示该 symbol 处于隔离态,拒绝任何新 claim。
var ErrSymbolQuarantined = errors.New("execution: symbol 处于隔离态,拒绝新 claim")

// Claim 记录一次 per-symbol 独占持有的元数据。
type Claim struct {
	Symbol     string
	Owner      string // 建议传 intent_id,便于用日志谱系定位「谁在持有」
	AcquiredAt time.Time
}

// QuarantineRecord 记录一次隔离的元数据,便于运维/对账排查「为什么这个 symbol 被冻住」。
type QuarantineRecord struct {
	Symbol string
	Reason string
	Since  time.Time
	// RelatedOrderID 可选携带触发隔离的订单/成交标识,便于定位根因。
	RelatedOrderID string
}

// ClaimManager 是进程内 per-symbol claim + quarantine 的并发安全实现。
// 它只做内存互斥,不做跨进程锁——跨进程独占已由 internal/app/live_stack_lease.go 的
// 单写租约在更外层保证,本类型只解决「同进程内并发决策对同一 symbol 打架」的问题。
type ClaimManager struct {
	mu         sync.Mutex
	claims     map[string]*Claim
	quarantine map[string]QuarantineRecord
}

// NewClaimManager 构造空的 claim 管理器。
func NewClaimManager() *ClaimManager {
	return &ClaimManager{
		claims:     make(map[string]*Claim),
		quarantine: make(map[string]QuarantineRecord),
	}
}

// TryAcquire 尝试为 symbol 建立一个新 claim。owner 必须非空且在调用方视角内唯一(建议用
// intent_id)。并发下恰好一个调用者能成功,其余全部返回 ErrSymbolClaimed;若 symbol 处于
// 隔离态,一律拒绝新 claim(即使当前无人持有),返回 ErrSymbolQuarantined。
//
// 成功时返回的 release 函数用于释放 claim;release 对「非当前持有者」调用是安全的 no-op
// (防止调用方在竞态下误释放别人的 claim)。
func (m *ClaimManager) TryAcquire(symbol, owner string, now time.Time) (release func(), err error) {
	if owner == "" {
		return nil, fmt.Errorf("execution: claim owner 不能为空")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, quarantined := m.quarantine[symbol]; quarantined {
		return nil, ErrSymbolQuarantined
	}
	if existing, held := m.claims[symbol]; held {
		if existing.Owner == owner {
			// 同一 owner 重复获取视为幂等成功(如重试路径误重入),返回可安全调用多次的 release。
			return m.releaseFunc(symbol, owner), nil
		}
		return nil, ErrSymbolClaimed
	}
	m.claims[symbol] = &Claim{Symbol: symbol, Owner: owner, AcquiredAt: now}
	return m.releaseFunc(symbol, owner), nil
}

func (m *ClaimManager) releaseFunc(symbol, owner string) func() {
	return func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if existing, ok := m.claims[symbol]; ok && existing.Owner == owner {
			delete(m.claims, symbol)
		}
	}
}

// Holder 返回 symbol 当前的持有者(若有)。
func (m *ClaimManager) Holder(symbol string) (Claim, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.claims[symbol]
	if !ok {
		return Claim{}, false
	}
	return *c, true
}

// Quarantine 把 symbol 标记为隔离态。已存在的 claim 不会被强制释放——「未知期间不释放 claim」,
// 只阻断后续新 claim;重复调用只刷新 reason/时间,不报错。
func (m *ClaimManager) Quarantine(symbol, reason string, since time.Time, relatedOrderID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.quarantine[symbol] = QuarantineRecord{Symbol: symbol, Reason: reason, Since: since, RelatedOrderID: relatedOrderID}
}

// IsQuarantined 报告 symbol 当前是否处于隔离态。
func (m *ClaimManager) IsQuarantined(symbol string) (QuarantineRecord, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.quarantine[symbol]
	return rec, ok
}

// ClearQuarantine 显式清除 symbol 的隔离态。调用方必须已完成对账收敛(如 Recover 后
// discrepancy 已归零)才能调用——本类型不做任何自动过期,避免「悬而未决」被时间静默抹平。
func (m *ClaimManager) ClearQuarantine(symbol string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.quarantine, symbol)
}

// QuarantinedSymbols 返回当前全部隔离中的 symbol(顺序不保证),用于面板/告警展示。
func (m *ClaimManager) QuarantinedSymbols() []QuarantineRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]QuarantineRecord, 0, len(m.quarantine))
	for _, rec := range m.quarantine {
		out = append(out, rec)
	}
	return out
}
