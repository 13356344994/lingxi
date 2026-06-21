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

// readTargets 解析读接口的目标所集合:空 → [Primary];具名 → [该所];all → 全部所(注册序)。
// 列表类读接口据此对每个所的 ReportStore 取数后拼接,实现 venue=all 聚合;单所时退化为缺省所,行为不变。
func (s *DashboardServer) readTargets(r *http.Request) ([]*venueRuntime, error) {
	if s == nil || s.orchestrator == nil {
		return nil, fmt.Errorf("编排器未初始化")
	}
	if venueParam(r) == venueAllSelector {
		return s.orchestrator.All(), nil
	}
	rt, err := s.resolveVenue(venueParam(r))
	if err != nil {
		return nil, err
	}
	return []*venueRuntime{rt}, nil
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

// venueTagger 由可被读时注入来源所标签的记录类型实现(指针接收者 SetVenue)。仅在 venue=all 跨所聚合时调用,
// 单所/默认路径不注入 → JSON 输出与落盘逐字节同改造前(配合 `json:"venue,omitempty"`)。
type venueTagger interface {
	SetVenue(string)
}

// aggregateRecords 对一组所的 ReportStore 各取 limit 条后顺序拼接(venue=all 聚合的通用工具)。
// 单所时即该所的 list(limit),与改造前逐字节一致。某所取数失败即整体失败(避免悄悄漏所)。
// injectVenue=true(仅 venue=all)时,为每条记录读时注入其来源所标签(§9.2),不改落盘。
func aggregateRecords[T any](targets []*venueRuntime, limit int, list func(*ReportStore, int) ([]T, error), injectVenue bool) ([]T, error) {
	var out []T
	for _, rt := range targets {
		if rt == nil || rt.store == nil {
			continue
		}
		recs, err := list(rt.store, limit)
		if err != nil {
			return nil, err
		}
		if injectVenue {
			for i := range recs {
				if tagger, ok := any(&recs[i]).(venueTagger); ok {
					tagger.SetVenue(rt.name)
				}
			}
		}
		out = append(out, recs...)
	}
	return out, nil
}

// aggregateStats 同 aggregateRecords,用于无 limit 的统计类读接口(残差严重度/场景分布)。
func aggregateStats[T any](targets []*venueRuntime, list func(*ReportStore) ([]T, error)) ([]T, error) {
	var out []T
	for _, rt := range targets {
		if rt == nil || rt.store == nil {
			continue
		}
		recs, err := list(rt.store)
		if err != nil {
			return nil, err
		}
		out = append(out, recs...)
	}
	return out, nil
}
