package app

import (
	"bufio"
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"lingxi/internal/venue"
)

const equitySnapshotsFileName = "equity_snapshots.jsonl"
const equitySampleInterval = 3 * time.Minute

var equityStableCoins = map[string]struct{}{
	"USDT": {}, "USDC": {}, "FDUSD": {}, "DAI": {}, "TUSD": {}, "USDE": {},
}

type equitySnapshotRecord struct {
	TS       string  `json:"ts"`
	Venue    string  `json:"venue"`
	CashUSDT float64 `json:"cash_usdt"`
	Complete *bool   `json:"complete,omitempty"`
}

// AccountEquityService 账户级权益采样与收益核算服务:与任何策略引擎解耦,只依赖 venue 适配器集合。
// 快照数据沿用历史路径 records/crossvenue/equity_snapshots.jsonl 以保持连续性。
type AccountEquityService struct {
	venues       map[string]venue.Venue
	recordDir    string
	logger       *log.Logger
	nowFn        func() time.Time
	equitySnapMu sync.Mutex
}

// NewAccountEquityService 构造账户级权益服务。venues 为空时服务仍可构造(采样每轮记为盲区)。
func NewAccountEquityService(venues map[string]venue.Venue, recordDir string, logger *log.Logger) *AccountEquityService {
	if logger == nil {
		logger = log.Default()
	}
	if strings.TrimSpace(recordDir) == "" {
		recordDir = "records"
	}
	return &AccountEquityService{
		venues:    venues,
		recordDir: recordDir,
		logger:    logger,
		nowFn:     time.Now,
	}
}

// Venues 暴露适配器集合(充提核算 lookup 等账户级服务复用)。
func (s *AccountEquityService) Venues() map[string]venue.Venue {
	if s == nil {
		return nil
	}
	return s.venues
}

// accountEquityRecordDir 历史快照目录(沿用 crossvenue/ 子目录,保证既有数据连续)。
func accountEquityRecordDir(base string) string {
	if strings.TrimSpace(base) == "" {
		base = "records"
	}
	return filepath.Join(base, "crossvenue")
}

func (s *AccountEquityService) equitySnapshotsPath() string {
	return filepath.Join(accountEquityRecordDir(s.recordDir), equitySnapshotsFileName)
}

type equityReporter interface {
	FetchEquityUSDT(ctx context.Context) (float64, error)
}

func cashUSDTFromBalances(b venue.Balances) float64 {
	var total float64
	for asset, bal := range b.ByAsset {
		if _, ok := equityStableCoins[strings.ToUpper(asset)]; ok {
			total += bal.Free + bal.Locked
		}
	}
	return total
}

func (s *AccountEquityService) sampleEquityOnce(ctx context.Context) {
	if s == nil {
		return
	}
	now := s.nowFn().UTC()
	ts := now.Format(time.RFC3339)
	var records []equitySnapshotRecord
	for name, v := range s.venues {
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		var cash float64
		if er, ok := v.(equityReporter); ok {
			eq, err := er.FetchEquityUSDT(cctx)
			if err == nil {
				cash = eq
			} else {
				s.logger.Printf("[权益采样] %s 用现金口径回退(无总权益接口/拉取失败): %v", name, err)
				bal, balErr := v.FetchBalances(cctx)
				if balErr != nil {
					cancel()
					s.logger.Printf("[权益采样] %s 拉余额失败(跳过本轮): %v", name, balErr)
					continue
				}
				cash = cashUSDTFromBalances(bal)
			}
		} else {
			bal, err := v.FetchBalances(cctx)
			if err != nil {
				cancel()
				s.logger.Printf("[权益采样] %s 拉余额失败(跳过本轮): %v", name, err)
				continue
			}
			cash = cashUSDTFromBalances(bal)
		}
		cancel()
		records = append(records, equitySnapshotRecord{
			TS:       ts,
			Venue:    name,
			CashUSDT: cash,
		})
	}
	if len(records) == 0 {
		expectedVenues := len(s.venues)
		s.logger.Printf("[权益采样] 连通性盲区: 全部 venue 拉取失败 %d/%d,本轮跳过且标记盲区", 0, expectedVenues)
		if err := recordEquitySamplerAttempt(s.recordDir, ts, expectedVenues, 0, false); err != nil {
			s.logger.Printf("[权益采样] 写入健康信号失败: %v", err)
		}
		return
	}
	expectedVenues := len(s.venues)
	complete := len(records) >= expectedVenues
	if !complete {
		var missing []string
		got := make(map[string]struct{}, len(records))
		for _, rec := range records {
			got[rec.Venue] = struct{}{}
		}
		for name := range s.venues {
			if _, ok := got[name]; !ok {
				missing = append(missing, name)
			}
		}
		sort.Strings(missing)
		s.logger.Printf("[权益采样] 本轮残缺: 成功 %d/期望 %d, 缺 [%s]", len(records), expectedVenues, strings.Join(missing, ","))
	}
	completeFlag := complete
	for i := range records {
		records[i].Complete = &completeFlag
	}
	s.equitySnapMu.Lock()
	defer s.equitySnapMu.Unlock()
	path := s.equitySnapshotsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		s.logger.Printf("[权益采样] 创建目录失败: %v", err)
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		s.logger.Printf("[权益采样] 打开文件失败: %v", err)
		return
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, rec := range records {
		if err := enc.Encode(rec); err != nil {
			s.logger.Printf("[权益采样] 写入记录失败: %v", err)
			return
		}
	}
	if err := recordEquitySamplerAttempt(s.recordDir, ts, expectedVenues, len(records), complete); err != nil {
		s.logger.Printf("[权益采样] 写入健康信号失败: %v", err)
	}
}

func (s *AccountEquityService) RunEquitySampler(ctx context.Context) {
	if s == nil {
		return
	}
	s.logger.Printf("[权益采样] 已启动: interval=%s 口径=各所USDT现金余额真实变化", equitySampleInterval)
	s.sampleEquityOnce(ctx)
	ticker := time.NewTicker(equitySampleInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.sampleEquityOnce(ctx)
		}
	}
}

// TodayCSTEquityDeltas 按北京时间自然日界,用 equity_snapshots 计算各所账户总值相对日初的真实变化。
func (s *AccountEquityService) TodayCSTEquityDeltas(now time.Time) (deltas, currents, baselines map[string]float64, dayStart, earliestBaseline time.Time) {
	dayStart = beijingCSTDayStart(now)
	deltas, currents, baselines, earliestBaseline = s.equityEarnings(dayStart)
	return deltas, currents, baselines, dayStart, earliestBaseline
}

func (s *AccountEquityService) equityEarnings(windowStart time.Time) (deltas, currents, baselines map[string]float64, earliest time.Time) {
	deltas = make(map[string]float64)
	currents = make(map[string]float64)
	baselines = make(map[string]float64)
	if s == nil {
		return deltas, currents, baselines, earliest
	}
	s.equitySnapMu.Lock()
	defer s.equitySnapMu.Unlock()
	path := s.equitySnapshotsPath()
	f, err := os.Open(path)
	if err != nil {
		return deltas, currents, baselines, earliest
	}
	defer f.Close()

	latestTS := make(map[string]time.Time)
	baselineTS := make(map[string]time.Time)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var rec equitySnapshotRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		ts, err := time.Parse(time.RFC3339, rec.TS)
		if err != nil {
			continue
		}
		venueName := rec.Venue
		if prev, ok := latestTS[venueName]; !ok || !ts.Before(prev) {
			currents[venueName] = rec.CashUSDT
			latestTS[venueName] = ts
		}
		if !ts.Before(windowStart) {
			if prev, ok := baselineTS[venueName]; !ok || ts.Before(prev) {
				baselines[venueName] = rec.CashUSDT
				baselineTS[venueName] = ts
			}
		}
	}

	for venueName, current := range currents {
		baseline, ok := baselines[venueName]
		if !ok {
			baseline = current
			baselines[venueName] = current
		}
		deltas[venueName] = current - baseline
	}

	for _, ts := range baselineTS {
		if earliest.IsZero() || ts.Before(earliest) {
			earliest = ts
		}
	}
	return deltas, currents, baselines, earliest
}

func (s *AccountEquityService) pruneEquitySnapshots(before time.Time) (removed int, err error) {
	if s == nil {
		return 0, nil
	}
	s.equitySnapMu.Lock()
	defer s.equitySnapMu.Unlock()
	path := s.equitySnapshotsPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	lines := strings.Split(string(data), "\n")
	var kept []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		var rec equitySnapshotRecord
		if err := json.Unmarshal([]byte(trimmed), &rec); err != nil {
			kept = append(kept, line)
			continue
		}
		ts, err := time.Parse(time.RFC3339, rec.TS)
		if err != nil {
			kept = append(kept, line)
			continue
		}
		if ts.Before(before) {
			removed++
			continue
		}
		kept = append(kept, line)
	}
	if removed == 0 {
		return 0, nil
	}
	tmpPath := filepath.Join(filepath.Dir(path), ".equity_snapshots.jsonl.tmp")
	out := strings.Join(kept, "\n")
	if len(kept) > 0 {
		out += "\n"
	}
	if err := os.WriteFile(tmpPath, []byte(out), 0o600); err != nil {
		return 0, err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return 0, err
	}
	return removed, nil
}
