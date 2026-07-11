package execution

// exec_ids.go 统一 decision_id/intent_id/client_order_id/trade_id 的生成与派生规则,确保
// 从「决策」到「订单」到「成交」到「归因」全程可按前缀+分隔符还原谱系(plan §3:
// 「统一 decision_id/intent_id/client_order_id/trade_id,从候选贯穿订单、成交、持仓、结算和归因」)。
//
// 派生关系:
//
//	decision_id  = d<base36(UnixNano)><进程序列><随机2位>     (一次交易决策)
//	intent_id    = <decision_id>.i<seq2位>                    (同一决策可能派生多个 intent,如换币重试)
//	client_order_id = <intent_id>.a<attempt2位>               (同一 intent 的第几次下单尝试/追价)
//	trade_id(本地合成) = <client_order_id>.f<fillSeq4位>       (交易所未给 tradeId 时的本地兜底,
//	                                                            一旦交易所侧 tradeId 到达必须优先使用
//	                                                            交易所 tradeId 做去重键)
//
// 全部产物只含 [a-z0-9.],不含大小写混用/连字符,天然满足大多数交易所 clOrdId 的字符集限制;
// 各 venue 适配器仍需按自己的长度上限二次裁剪(如 OKX 32 字符,见 internal/venue/okx/trading.go
// 的 sanitizeClOrdID),本包只保证「同一输入必产出同一 ID」的确定性,不对交易所做具体适配。
import (
	"crypto/rand"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const idBase = 36

// randomIDSuffix 生成长度为 n 的随机 base36 后缀,用于降低同一纳秒内并发生成 decision_id 的碰撞
// 概率(理论上仍可能碰撞,调用方在极端并发下应结合业务序列号进一步去重)。crypto/rand 失败时
// (极罕见)回退到时间戳派生,保证永不返回空串。
func randomIDSuffix(n int) string {
	if n <= 0 {
		return ""
	}
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		fallback := strconv.FormatInt(time.Now().UTC().UnixNano(), idBase)
		if len(fallback) >= n {
			return fallback[len(fallback)-n:]
		}
		return strings.Repeat("0", n-len(fallback)) + fallback
	}
	var b strings.Builder
	for _, v := range buf {
		b.WriteByte(idAlphabet[int(v)%len(idAlphabet)])
	}
	return b.String()
}

const idAlphabet = "0123456789abcdefghijklmnopqrstuvwxyz"

var decisionSequence atomic.Uint64

// NewDecisionID 生成新的 decision_id：时间主体 + 原子进程序列保证同进程并发唯一，
// 再附两位随机尾缀降低跨进程同纳秒碰撞概率。
func NewDecisionID(at time.Time) string {
	body := strconv.FormatInt(at.UTC().UnixNano(), idBase)
	seq := strconv.FormatUint(decisionSequence.Add(1), idBase)
	if len(seq) < 4 {
		seq = strings.Repeat("0", 4-len(seq)) + seq
	}
	return "d" + body + seq + randomIDSuffix(2)
}

// DeriveIntentID 由 decision_id + seq 派生 intent_id(纯函数,同输入同输出)。
// seq 从 0 开始,同一决策下的多个 intent(如条件单换币、失败重试换 symbol)各自递增。
func DeriveIntentID(decisionID string, seq int) string {
	return fmt.Sprintf("%s.i%02d", decisionID, normalizeSeq(seq))
}

// DeriveClientOrderID 由 intent_id + attempt 派生 client_order_id(纯函数)。
// attempt 从 0 开始,同一 intent 下的追价/重试各自递增,交易所侧据此天然去重同一意图的重复提交。
func DeriveClientOrderID(intentID string, attempt int) string {
	return fmt.Sprintf("%s.a%02d", intentID, normalizeSeq(attempt))
}

// DeriveLocalTradeID 由 client_order_id + fillSeq 派生「本地合成 trade_id」,仅用于交易所尚未
// 回填 tradeId 之前的占位去重键;一旦交易所侧 tradeId 到达,reducer 必须切换为使用交易所 tradeId
// 作为去重键(见 reducer.go Fill.TradeID 注释),避免本地序号与交易所真相脱节。
func DeriveLocalTradeID(clientOrderID string, fillSeq int) string {
	return fmt.Sprintf("%s.f%04d", clientOrderID, normalizeSeq(fillSeq))
}

// normalizeSeq 把负数序号钳制为 0,防止格式化出现负号破坏 ID 字符集约束。
func normalizeSeq(seq int) int {
	if seq < 0 {
		return 0
	}
	return seq
}
