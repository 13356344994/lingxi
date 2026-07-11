package replay

import (
	"testing"
	"time"
)

func episodesFromNets(nets []float64) []Episode {
	eps := make([]Episode, len(nets))
	for i, v := range nets {
		eps[i] = Episode{NetUSDT: v, Start: testBase.Add(time.Duration(i) * time.Minute)}
	}
	return eps
}

func TestBootstrapDeterministicWithFixedSeed(t *testing.T) {
	eps := episodesFromNets([]float64{5, -3, 10, -20, 7, 2, -1, 4})
	cfg := BootstrapConfig{Seed: 42, Resamples: 500, Confidence: 0.95, InitialUSDT: 50}
	a := BootstrapEpisodes(eps, cfg)
	b := BootstrapEpisodes(eps, cfg)
	if a != b {
		t.Fatalf("固定种子必须逐位复现: %+v vs %+v", a, b)
	}
	if a.Resamples != 500 {
		t.Fatalf("resamples want 500 got %d", a.Resamples)
	}
}

func TestBootstrapConstantEpisodesExactBound(t *testing.T) {
	// 所有 episode 净 PnL 相同:任意重采样总和恒等,下界应精确等于 n×v。
	eps := episodesFromNets([]float64{2.5, 2.5, 2.5, 2.5})
	res := BootstrapEpisodes(eps, BootstrapConfig{Seed: 1, Resamples: 100, Confidence: 0.95})
	if !almostEqual(res.TotalNetLowerUSDT, 10, 1e-9) {
		t.Fatalf("常数样本总净下界应为 10,got %v", res.TotalNetLowerUSDT)
	}
	if !almostEqual(res.MeanEpisodeNetLowerUSDT, 2.5, 1e-9) {
		t.Fatalf("单 episode 净下界应为 2.5,got %v", res.MeanEpisodeNetLowerUSDT)
	}
}

func TestBootstrapLowerBoundIsConservative(t *testing.T) {
	nets := []float64{-40, -10, 5, 8, 12, 20, 30, -25, 15, 3}
	var sum float64
	for _, v := range nets {
		sum += v
	}
	res := BootstrapEpisodes(episodesFromNets(nets), BootstrapConfig{Seed: 7, Resamples: 2000, Confidence: 0.95})
	if res.TotalNetLowerUSDT >= sum {
		t.Fatalf("95%% 下界应低于点估计 %v,got %v", sum, res.TotalNetLowerUSDT)
	}
}

func TestBootstrapRuinProbabilityBounds(t *testing.T) {
	// 全正 episode:破产频率 0,但上界必须保留 rule-of-three 的非零保守值。
	safe := BootstrapEpisodes(episodesFromNets([]float64{1, 2, 3, 4}),
		BootstrapConfig{Seed: 3, Resamples: 300, Confidence: 0.95, InitialUSDT: 100})
	if safe.RuinProb != 0 {
		t.Fatalf("全正样本破产频率应为 0,got %v", safe.RuinProb)
	}
	if !almostEqual(safe.RuinProbUpper, 3.0/300, 1e-12) {
		t.Fatalf("零命中上界应为 3/N=0.01,got %v", safe.RuinProbUpper)
	}

	// 深度亏损 episode + 小本金:破产频率应接近 1,上界被夹在 [频率,1]。
	risky := BootstrapEpisodes(episodesFromNets([]float64{-80, -90, -70, 5}),
		BootstrapConfig{Seed: 3, Resamples: 300, Confidence: 0.95, InitialUSDT: 60})
	if risky.RuinProb < 0.9 {
		t.Fatalf("重亏样本破产频率应接近 1,got %v", risky.RuinProb)
	}
	if risky.RuinProbUpper < risky.RuinProb || risky.RuinProbUpper > 1 {
		t.Fatalf("上界应在 [频率,1]: %+v", risky)
	}
}

func TestBootstrapEmptyEpisodes(t *testing.T) {
	res := BootstrapEpisodes(nil, BootstrapConfig{Seed: 1})
	if res != (BootstrapResult{}) {
		t.Fatalf("空样本应返回零值,got %+v", res)
	}
}
