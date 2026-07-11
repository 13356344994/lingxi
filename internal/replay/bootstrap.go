package replay

import (
	"math"
	"math/rand"
	"sort"

	"lingxi/internal/money"
)

// BootstrapConfig 固定种子 episode bootstrap 配置。以 episode(相关簇)为最小
// 重采样单位,避免簇内相关交易被当成独立样本而低估尾部。
type BootstrapConfig struct {
	Seed        int64   // 固定种子:同输入同种子必须逐位复现
	Resamples   int     // 重采样次数(<=0 时取 2000)
	Confidence  float64 // 单侧置信度,如 0.95((0,1) 之外取 0.95)
	InitialUSDT float64 // 路径起始权益(破产判定用;<=0 时不做破产统计)
}

// BootstrapResult 固定种子 bootstrap 输出。
type BootstrapResult struct {
	Resamples int
	// TotalNetLowerUSDT 总净 PnL 的单侧置信「下界」(1-Confidence 分位)。
	TotalNetLowerUSDT float64
	// MeanEpisodeNetLowerUSDT 单 episode 平均净 PnL 的置信下界。
	MeanEpisodeNetLowerUSDT float64
	// RuinProb / RuinProbUpper:重采样路径中触及权益<=0 的频率及其置信上界
	// (正态近似 + 零命中时 3/N 三分法则,保证上界不为 0 的假安全)。
	RuinProb      float64
	RuinProbUpper float64
}

// BootstrapEpisodes 对 episode 净 PnL 做固定种子有放回重采样:
// 每轮抽 n 个 episode 组成一条虚拟账户路径,统计总净 PnL 分布与破产频率。
// episodes 为空返回零值。结果确定性:同 episodes+config 逐位一致。
func BootstrapEpisodes(episodes []Episode, cfg BootstrapConfig) BootstrapResult {
	var res BootstrapResult
	n := len(episodes)
	if n == 0 {
		return res
	}
	resamples := cfg.Resamples
	if resamples <= 0 {
		resamples = 2000
	}
	conf := cfg.Confidence
	if conf <= 0 || conf >= 1 {
		conf = 0.95
	}
	res.Resamples = resamples

	pnl := make([]float64, n)
	for i, e := range episodes {
		pnl[i] = e.NetUSDT
	}
	rng := rand.New(rand.NewSource(cfg.Seed))
	totals := make([]float64, resamples)
	ruined := 0
	for b := 0; b < resamples; b++ {
		var total money.Micros
		hitRuin := false
		equity := money.FromUSDT(cfg.InitialUSDT)
		for i := 0; i < n; i++ {
			v := money.FromUSDT(pnl[rng.Intn(n)])
			total = total.Add(v)
			if cfg.InitialUSDT > 0 {
				equity = equity.Add(v)
				if equity.USDT() <= 0 {
					hitRuin = true
				}
			}
		}
		totals[b] = total.USDT()
		if hitRuin {
			ruined++
		}
	}
	sort.Float64s(totals)
	res.TotalNetLowerUSDT = quantileSorted(totals, 1-conf)
	res.MeanEpisodeNetLowerUSDT = res.TotalNetLowerUSDT / float64(n)

	if cfg.InitialUSDT > 0 {
		p := float64(ruined) / float64(resamples)
		res.RuinProb = p
		z := normalUpperZ(conf)
		upper := p + z*math.Sqrt(p*(1-p)/float64(resamples))
		if ruined == 0 {
			// rule of three:零命中时的保守上界。
			upper = math.Max(upper, 3/float64(resamples))
		}
		res.RuinProbUpper = math.Min(1, upper)
	}
	return res
}

// quantileSorted 对升序切片取 q 分位(最近邻,向保守侧取整——下分位向下取)。
func quantileSorted(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if q <= 0 {
		return sorted[0]
	}
	if q >= 1 {
		return sorted[len(sorted)-1]
	}
	idx := int(math.Floor(q * float64(len(sorted))))
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// normalUpperZ 返回常用单侧置信度对应的正态分位数;非常用值线性保守取 1.6449(95%)
// 与 2.3263(99%)之间的粗略插值,避免引入外部统计库。
func normalUpperZ(conf float64) float64 {
	switch {
	case conf >= 0.99:
		return 2.3263
	case conf >= 0.975:
		return 1.9600
	case conf >= 0.95:
		return 1.6449
	case conf >= 0.90:
		return 1.2816
	default:
		return 1.2816
	}
}
