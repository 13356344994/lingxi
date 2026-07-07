package app

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ===== ㊳④ 资金划转申报(2026-07-08)=====
// 双账本佐证制(见 account_risk_watch.go)已保证"划转不再触发硬停",本文件补齐声明式资金流:
// 用户在面板提前/事后申报"我转了钱",风控的「未解释资金流出」告警据此静默——机器连问都不用问。
// 申报只用于告警消音与对账参考,不修改 cum_fund_flow(真金口径仍由交易所账单/启发式独立确权,
// 避免第三来源与账单 API 双计)。

const declaredFundFlowsFile = "declared_fund_flows.jsonl"

// declaredFundFlow 一条申报(amount_usdt 带符号:+转入 −转出)。
type declaredFundFlow struct {
	DeclaredAt time.Time `json:"declared_at"`
	AmountUSDT float64   `json:"amount_usdt"`
	Note       string    `json:"note,omitempty"`
	Source     string    `json:"source,omitempty"` // panel
}

// appendDeclaredFundFlow 落盘一条申报。
func appendDeclaredFundFlow(recordDir string, rec declaredFundFlow) error {
	return appendJSONLRecord(filepath.Join(recordDir, declaredFundFlowsFile), rec)
}

// sumDeclaredFundFlowsUSDT 窗口内申报净额(+净转入 −净转出)。读失败按 0(fail-safe:不消音)。
func sumDeclaredFundFlowsUSDT(recordDir string, since, until time.Time) float64 {
	f, err := os.Open(filepath.Join(recordDir, declaredFundFlowsFile))
	if err != nil {
		return 0
	}
	defer f.Close()
	var sum float64
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var rec declaredFundFlow
		if json.Unmarshal([]byte(line), &rec) != nil {
			continue
		}
		if rec.DeclaredAt.Before(since) || rec.DeclaredAt.After(until) {
			continue
		}
		sum += rec.AmountUSDT
	}
	return sum
}

// declaredFlowExplainsOutflow 申报净转出是否足以解释权益缺口(≥60% 量级即认,划转常带手续费/时差)。
func declaredFlowExplainsOutflow(declaredNet, equityDropUSDT float64) bool {
	if equityDropUSDT <= 0 || declaredNet >= 0 {
		return false
	}
	return math.Abs(declaredNet) >= 0.6*equityDropUSDT
}

// FundFlowDeclareRequest POST /api/fund-flow/declare 入参。
type FundFlowDeclareRequest struct {
	AmountUSDT float64 `json:"amount_usdt"` // 正数
	Direction  string  `json:"direction"`   // in / out
	Note       string  `json:"note,omitempty"`
}

// handleFundFlowDeclareAPI 面板申报资金划转(控制类鉴权+审计留痕)。
func (s *DashboardServer) handleFundFlowDeclareAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorizeRuntimeControl(w, r, "fund_flow_declare") {
		return
	}
	var req FundFlowDeclareRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("解析失败: %v", err), http.StatusBadRequest)
		return
	}
	amt := req.AmountUSDT
	dir := strings.ToLower(strings.TrimSpace(req.Direction))
	if amt <= 0 || (dir != "in" && dir != "out") {
		http.Error(w, "amount_usdt 须>0 且 direction ∈ {in,out}", http.StatusBadRequest)
		return
	}
	if dir == "out" {
		amt = -amt
	}
	rec := declaredFundFlow{
		DeclaredAt: time.Now().UTC(),
		AmountUSDT: amt,
		Note:       strings.TrimSpace(req.Note),
		Source:     "panel",
	}
	if err := appendDeclaredFundFlow(s.recordDirOrDefault(), rec); err != nil {
		s.auditControlAction(r, "fund_flow_declare", false, err.Error())
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.auditControlAction(r, "fund_flow_declare", true, fmt.Sprintf("amount=%.2f note=%q", amt, rec.Note))
	writeJSON(w, map[string]any{"ok": true, "declared": rec})
}
