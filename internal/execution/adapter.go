package execution

// adapter.go 定义私有事件源的消费侧契约:Engine 只依赖 PrivateEventSource 接口即可消费任意
// 交易所的订单/成交私有流,不关心具体传输(真实 WS/轮询/回放 fixture)。真实网络鉴权与连接
// 循环留给各 venue 适配器实现(见 internal/venue/okx/private_events.go 的签名/解析骨架);
// 本文件只提供消费侧契约 + 一个确定性 fixture 实现,保证在没有真实网络凭证的情况下也能把
// claim/reducer/recovery 全部收敛逻辑跑通并写单测。
import (
	"sync"
	"time"
)

// PrivateEvent 是从任意交易所私有流翻译成的中性事件。Order 与 Fill 至多一个非空
// (一条 WS 推送通常只描述其中一类变化;个别所把两者揉进同一条推送时,适配器应拆成两条)。
type PrivateEvent struct {
	Order      *OrderSnapshot
	Fill       *Fill
	ReceivedAt time.Time
}

// PrivateEventSource 是私有事件源的最小接口。
type PrivateEventSource interface {
	// Events 返回事件通道;通道关闭表示流已结束(正常关闭或不可恢复错误),
	// 具体原因见 Err()。重连/心跳等传输细节完全由实现方内部处理,不在此接口暴露。
	Events() <-chan PrivateEvent
	// Err 返回流结束的原因;正常关闭(调用方主动 Close)应返回 nil。
	Err() error
	// Close 主动停止事件源并释放底层资源。可安全多次调用。
	Close() error
}

// FixtureEventSource 是确定性回放实现:按构造时给定的事件序列全部推入通道后关闭,
// 不依赖真实时间/网络,适合驱动 recovery/reducer 的确定性单测与「无真实凭证时的接入验证」。
type FixtureEventSource struct {
	ch       chan PrivateEvent
	mu       sync.Mutex
	closed   bool
	closeErr error
}

// NewFixtureEventSource 构造一个立即把 events 全部投递、随后关闭通道的确定性事件源。
func NewFixtureEventSource(events []PrivateEvent) *FixtureEventSource {
	ch := make(chan PrivateEvent, len(events))
	for _, e := range events {
		ch <- e
	}
	close(ch)
	return &FixtureEventSource{ch: ch}
}

// Events 返回事件通道。
func (s *FixtureEventSource) Events() <-chan PrivateEvent { return s.ch }

// Err 对确定性 fixture 源恒为 nil:它没有失败模式,失败场景请直接构造包含异常事件的序列
// 或在测试里另行模拟错误路径。
func (s *FixtureEventSource) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closeErr
}

// Close 是幂等的 no-op(通道已经在构造时关闭)。
func (s *FixtureEventSource) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

// Closed 报告 Close 是否已被调用,便于测试断言。
func (s *FixtureEventSource) Closed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}
