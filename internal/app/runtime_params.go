package app

// ===== 运行时资金参数热覆盖(2026-07-09 用户拍板:页面输入名义/回撤/杠杆,点应用即生效,
// 不重启;新仓用新参数,旧仓按开仓时参数维护到底)=====
//
// 边界(纪律):只覆盖【资金参数】(名义/杠杆/账户级日亏/回撤)=用户主权区;
// 策略切点(40/10/600 等冻结口径)与探针预算(20笔/0.5U)刻意不进此机制("改=作弊"区)。
// 生效方式:records/runtime_params.json,5s TTL 热读;0/缺省=沿用 config;硬顶防手滑。

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const runtimeParamsFile = "runtime_params.json"

// RuntimeParams 运行时覆盖(0=沿用 config)。
type RuntimeParams struct {
	PerTradeNotionalUSDT   float64   `json:"per_trade_notional_usdt,omitempty"` // fast 实弹每笔名义
	Leverage               int       `json:"leverage,omitempty"`
	MaxDailyLossUSDT       float64   `json:"max_daily_loss_usdt,omitempty"`       // 账户级日亏硬停
	MaxAccountDrawdownUSDT float64   `json:"max_account_drawdown_usdt,omitempty"` // 账户级回撤硬停
	UpdatedAt              time.Time `json:"updated_at,omitempty"`
	Note                   string    `json:"note,omitempty"`
}

// 硬顶(防手滑输错一个 0;要更高须改代码=强制 review)
const (
	rpMaxNotionalUSDT = 50.0
	rpMaxLeverage     = 10
)

// sanitizeRuntimeParams 纯函数:裁剪到安全域(负数归零=沿用 config)。
func sanitizeRuntimeParams(p RuntimeParams) RuntimeParams {
	if p.PerTradeNotionalUSDT < 0 {
		p.PerTradeNotionalUSDT = 0
	}
	if p.PerTradeNotionalUSDT > rpMaxNotionalUSDT {
		p.PerTradeNotionalUSDT = rpMaxNotionalUSDT
	}
	if p.Leverage < 0 {
		p.Leverage = 0
	}
	if p.Leverage > rpMaxLeverage {
		p.Leverage = rpMaxLeverage
	}
	if p.MaxDailyLossUSDT < 0 {
		p.MaxDailyLossUSDT = 0
	}
	if p.MaxAccountDrawdownUSDT < 0 {
		p.MaxAccountDrawdownUSDT = 0
	}
	return p
}

var (
	rpMu    sync.Mutex
	rpCache = map[string]RuntimeParams{} // recordDir -> params
	rpAt    = map[string]time.Time{}
)

// loadRuntimeParams 热读(5s TTL;文件不存在=零值=全部沿用 config)。
func loadRuntimeParams(recordDir string) RuntimeParams {
	rpMu.Lock()
	defer rpMu.Unlock()
	if time.Since(rpAt[recordDir]) < 5*time.Second {
		return rpCache[recordDir]
	}
	var p RuntimeParams
	if data, err := os.ReadFile(filepath.Join(recordDir, runtimeParamsFile)); err == nil {
		_ = json.Unmarshal(data, &p)
	}
	p = sanitizeRuntimeParams(p)
	rpCache[recordDir] = p
	rpAt[recordDir] = time.Now()
	return p
}

// rpInvalidate 应用后立即失效缓存(下一次读取即新值)。
func rpInvalidate(recordDir string) {
	rpMu.Lock()
	defer rpMu.Unlock()
	delete(rpAt, recordDir)
}

// handleRuntimeParamsAPI GET=当前生效;POST=应用(控制鉴权+审计留痕,不重启即生效)。
func (s *DashboardServer) handleRuntimeParamsAPI(w http.ResponseWriter, r *http.Request) {
	// 运行时参数文件放全局 records/ 根(recordDirOrDefault),四所引擎共读
	dir := s.recordDirOrDefault()
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, loadRuntimeParams(dir))
	case http.MethodPost:
		if !s.authorizeRuntimeControl(w, r, "runtime_params_apply") {
			return
		}
		var p RuntimeParams
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			http.Error(w, "参数解析失败: "+err.Error(), http.StatusBadRequest)
			return
		}
		p = sanitizeRuntimeParams(p)
		p.UpdatedAt = time.Now().UTC()
		if err := writeJSONFile(filepath.Join(dir, runtimeParamsFile), p); err != nil {
			http.Error(w, "落盘失败: "+err.Error(), http.StatusInternalServerError)
			return
		}
		rpInvalidate(dir)
		s.auditControlAction(r, "runtime_params_apply", true,
			"名义="+fmtF(p.PerTradeNotionalUSDT)+" 杠杆="+itoa(p.Leverage)+
				" 日亏="+fmtF(p.MaxDailyLossUSDT)+" 回撤="+fmtF(p.MaxAccountDrawdownUSDT))
		writeJSON(w, p)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func fmtF(v float64) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func itoa(v int) string {
	b, _ := json.Marshal(v)
	return string(b)
}
