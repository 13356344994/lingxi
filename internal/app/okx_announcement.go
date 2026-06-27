package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	okxAnnouncementBaseURL           = "https://www.okx.com/api/v5/support/announcements"
	okxAnnouncementDelistType        = "announcements-delistings"
	okxAnnouncementMaintenanceType    = "announcements-system-maintenance-or-update"
	okxAnnouncementCacheTTL          = 30 * time.Minute
	okxAnnouncementHTTPTimeout       = 8 * time.Second
	okxAnnouncementReasonTitleMaxRunes = 80
)

var okxAnnouncementPairRe = regexp.MustCompile(`([A-Z0-9]{2,15})-USDT`)

type announcementHit struct {
	Kind  string
	Title string
	Time  time.Time
}

type okxAnnouncementDetail struct {
	AnnType string
	PTime   string
	Title   string
	URL     string
}

type okxAnnouncementResponse struct {
	Code string `json:"code"`
	Data []struct {
		TotalPage string                  `json:"totalPage"`
		Details   []okxAnnouncementDetail `json:"details"`
	} `json:"data"`
}

type okxAnnouncementGuard struct {
	mu        sync.RWMutex
	risky     map[string]announcementHit
	fetchedAt time.Time
	httpGet   func(ctx context.Context, url string) ([]byte, error)
	nowFn     func() time.Time
	logger    *log.Logger
}

func newOKXAnnouncementGuard(logger *log.Logger) *okxAnnouncementGuard {
	if logger == nil {
		logger = log.Default()
	}
	g := &okxAnnouncementGuard{
		risky:   make(map[string]announcementHit),
		nowFn:   time.Now,
		logger:  logger,
		httpGet: defaultOKXAnnouncementHTTPGet,
	}
	return g
}

func defaultOKXAnnouncementHTTPGet(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: okxAnnouncementHTTPTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return body, nil
}

func parseOKXAnnouncementDetails(body []byte) []okxAnnouncementDetail {
	if len(body) == 0 {
		return nil
	}
	var resp okxAnnouncementResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil
	}
	if resp.Code != "0" {
		return nil
	}
	var out []okxAnnouncementDetail
	for _, block := range resp.Data {
		out = append(out, block.Details...)
	}
	return out
}

func titleHasRiskKeyword(title string) bool {
	lower := strings.ToLower(title)
	if strings.Contains(lower, "delist") || strings.Contains(lower, "maintenance") {
		return true
	}
	for _, kw := range []string{"下架", "退市", "暂停"} {
		if strings.Contains(title, kw) {
			return true
		}
	}
	return false
}

func extractTickersFromTitle(title string) []string {
	upper := strings.ToUpper(strings.TrimSpace(title))
	if upper == "" {
		return nil
	}
	seen := make(map[string]struct{})
	add := func(sym string) {
		sym = strings.ToUpper(strings.TrimSpace(sym))
		if sym == "" || sym == "USDT" {
			return
		}
		if _, ok := seen[sym]; ok {
			return
		}
		seen[sym] = struct{}{}
	}
	// 只认标题里显式的 `XXX-USDT` 交易对(OKX 下架/维护公告即此格式);
	// 不做"所有大写词都当币种"的裸词匹配——否则 DELIST/SWAP/PERPETUAL/SPOT 等英文噪声词会变成风险键,误杀正常币、挡住开仓(fail-safe 偏保守:宁可漏判,不可误杀)。
	for _, m := range okxAnnouncementPairRe.FindAllStringSubmatch(upper, -1) {
		if len(m) > 1 {
			add(m[1] + "-USDT")
			add(m[1])
		}
	}
	out := make([]string, 0, len(seen))
	for sym := range seen {
		out = append(out, sym)
	}
	return out
}

func announcementKind(annType string) string {
	switch strings.TrimSpace(annType) {
	case okxAnnouncementDelistType:
		return "下架"
	case okxAnnouncementMaintenanceType:
		return "维护"
	default:
		return "公告"
	}
}

func parseOKXAnnouncementPTime(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	ms, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

func truncateAnnouncementTitle(title string, maxRunes int) string {
	title = strings.TrimSpace(title)
	if maxRunes <= 0 {
		return title
	}
	runes := []rune(title)
	if len(runes) <= maxRunes {
		return title
	}
	return string(runes[:maxRunes]) + "..."
}

func buildRiskyMapFromDetails(details []okxAnnouncementDetail) map[string]announcementHit {
	out := make(map[string]announcementHit)
	for _, d := range details {
		title := strings.TrimSpace(d.Title)
		if title == "" || !titleHasRiskKeyword(title) {
			continue
		}
		tickers := extractTickersFromTitle(title)
		if len(tickers) == 0 {
			continue
		}
		hit := announcementHit{
			Kind:  announcementKind(d.AnnType),
			Title: title,
			Time:  parseOKXAnnouncementPTime(d.PTime),
		}
		for _, sym := range tickers {
			key := normalizeAnnouncementSymbol(sym)
			if key == "" {
				continue
			}
			if prev, ok := out[key]; ok && !hit.Time.After(prev.Time) {
				continue
			}
			out[key] = hit
		}
	}
	return out
}

func normalizeAnnouncementSymbol(symbol string) string {
	sym := strings.ToUpper(strings.TrimSpace(symbol))
	sym = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, sym)
	return sym
}

func (g *okxAnnouncementGuard) fetchAnnouncements(ctx context.Context, annType string) ([]okxAnnouncementDetail, bool) {
	url := fmt.Sprintf("%s?annType=%s&page=1", okxAnnouncementBaseURL, annType)
	body, err := g.httpGet(ctx, url)
	if err != nil {
		if g.logger != nil {
			g.logger.Printf("[OKX公告] 拉取 %s 失败(fail-open): %v", annType, err)
		}
		return nil, false
	}
	details := parseOKXAnnouncementDetails(body)
	if details == nil && len(body) > 0 {
		if g.logger != nil {
			g.logger.Printf("[OKX公告] 解析 %s 响应失败(fail-open)", annType)
		}
		return nil, false
	}
	return details, true
}

func (g *okxAnnouncementGuard) Refresh(ctx context.Context) {
	if g == nil {
		return
	}
	now := g.nowFn().UTC()
	g.mu.RLock()
	if !g.fetchedAt.IsZero() && now.Sub(g.fetchedAt) < okxAnnouncementCacheTTL {
		g.mu.RUnlock()
		return
	}
	g.mu.RUnlock()

	// 只查已确认可用的下架类型;维护类型 annType 名不稳(实测 HTTP 400),暂不查询以免无谓噪声。
	annTypes := []string{okxAnnouncementDelistType}
	risky := make(map[string]announcementHit)
	okCount := 0
	for _, annType := range annTypes {
		details, ok := g.fetchAnnouncements(ctx, annType)
		if !ok {
			continue
		}
		okCount++
		for sym, hit := range buildRiskyMapFromDetails(details) {
			if prev, exists := risky[sym]; !exists || hit.Time.After(prev.Time) {
				risky[sym] = hit
			}
		}
	}
	if okCount == 0 {
		return
	}

	g.mu.Lock()
	g.risky = risky
	g.fetchedAt = now
	g.mu.Unlock()
	if g.logger != nil {
		g.logger.Printf("[OKX公告] 已刷新风险集合: symbols=%d", len(risky))
	}
}

func (g *okxAnnouncementGuard) IsRisky(symbol string) (bool, string) {
	if g == nil {
		return false, ""
	}
	sym := normalizeAnnouncementSymbol(symbol)
	if sym == "" {
		return false, ""
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	if len(g.risky) == 0 {
		return false, ""
	}
	if hit, ok := g.risky[sym]; ok {
		return true, fmt.Sprintf("公告:%s %s", hit.Kind, truncateAnnouncementTitle(hit.Title, okxAnnouncementReasonTitleMaxRunes))
	}
	if i := strings.Index(sym, "-"); i > 0 {
		if hit, ok := g.risky[sym[:i]]; ok {
			return true, fmt.Sprintf("公告:%s %s", hit.Kind, truncateAnnouncementTitle(hit.Title, okxAnnouncementReasonTitleMaxRunes))
		}
	}
	if hit, ok := g.risky[sym+"-USDT"]; ok {
		return true, fmt.Sprintf("公告:%s %s", hit.Kind, truncateAnnouncementTitle(hit.Title, okxAnnouncementReasonTitleMaxRunes))
	}
	return false, ""
}
