package app

import (
	"fmt"
	"net/http"
	"strings"
)

// venueAllSelector 是读 API 的「跨所聚合」哨兵值(?venue=all)。控制类写操作显式拒绝该值(必须指定单一所)。
const venueAllSelector = "all"

// venueScopePrimaryOnly 表示 ?venue=all 命中「单所视图」读接口时,实际回落缺省所(Primary)而非跨所聚合。
const venueScopePrimaryOnly = "primary_only"

// venueScopeLabel 为 summary/status 类单所读接口生成 venue_scope 响应字段:仅 venue=all 时返回 primary_only,其余为空(omitempty)。
func venueScopeLabel(selector string) string {
	if selector == venueAllSelector {
		return venueScopePrimaryOnly
	}
	return ""
}

// venueParam 解析请求里的 ?venue= 选择器(裁空格 + 小写归一)。缺省(空)语义 = 缺省所(Primary),
// 与改造前单所行为逐字节一致;P3c 起额外支持具名所与 all 聚合。
func venueParam(r *http.Request) string {
	return strings.ToLower(strings.TrimSpace(r.URL.Query().Get("venue")))
}

// resolveVenue 把选择器解析成单个 venue 运行时。空 / all → Primary(缺省所,后向兼容);具名 → 精确命中或报错。
// 用于「单所视图」读接口(summary/status/config GET)——这些接口的 all 聚合是独立后续项(§9.2),P3c 先回落 Primary。
func (s *DashboardServer) resolveVenue(name string) (*venueRuntime, error) {
	if s == nil || s.orchestrator == nil {
		return nil, fmt.Errorf("编排器未初始化")
	}
	if name == "" || name == venueAllSelector {
		primary := s.orchestrator.Primary()
		if primary == nil {
			return nil, fmt.Errorf("无可用 venue")
		}
		return primary, nil
	}
	rt, ok := s.orchestrator.Get(name)
	if !ok {
		return nil, fmt.Errorf("未知 venue: %s", name)
	}
	return rt, nil
}

// resolveControlTarget 解析控制类(写)接口的单一目标所,并直接处理错误响应/审计。
// 规则:空 → 缺省所;具名 → 命中或 404;all → 显式 400 拒绝(写操作不允许跨所扇出,须指定单一所)。
// 返回 (运行时, true) 表示可继续;(nil, false) 表示已写出错误响应,调用方应直接 return。
func (s *DashboardServer) resolveControlTarget(w http.ResponseWriter, r *http.Request, action string) (*venueRuntime, bool) {
	name := venueParam(r)
	if name == venueAllSelector {
		const msg = "控制类操作不支持 venue=all,请指定单一交易所"
		s.auditControlAction(r, action, false, msg)
		http.Error(w, msg, http.StatusBadRequest)
		return nil, false
	}
	rt, err := s.resolveVenue(name)
	if err != nil {
		s.auditControlAction(r, action, false, err.Error())
		http.Error(w, err.Error(), http.StatusNotFound)
		return nil, false
	}
	return rt, true
}
