package app

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"lingxi/internal/venue"
	"lingxi/internal/venue/binance"
)

// orchestrator.go 落地多所重构第 7 章「多引擎编排」的骨架(P3a)。
//
// 现状(单所)是严格单例:一个 ReportStore + 一个 RuntimeController + 一个 DashboardServer + 一个控制令牌。
// 多所要把这一整套「控制面」venue 化:1 个 Orchestrator + N 个 venue-scoped (Controller, Store) —— 而不是
// 让单个 Controller 内部 venue-aware(后者会把全局锁/状态揉成上帝对象,见 §7.1)。
//
// P3a 只引入类型与接缝,**只注册唯一一个 venue("binance"),包住现有那套单例**,行为与改造前逐字节一致:
//   - DashboardServer 改持有 Orchestrator,但所有 handler 仍走 Primary()(= binance)→ 路由零变化;
//   - run.go 用 Orchestrator 组装,而非散落的 store/controller 局部变量。
//
// P3b 起逐步:per-venue records/<venue>/ 隔离、控制 API ?venue= 路由、双级 halt、启动隔离、面板 venue 维度。
type venueRuntime struct {
	name string
	// venueAdapter 是该所的 venue.Venue 适配器(P3a 可为 nil;接第二家时每所注入各自适配器)。
	venueAdapter venue.Venue
	controller   *RuntimeController
	store        *ReportStore
	// allocatedCapitalUSDT 是该所绩效指标的收益率分母(§9.1),来自该所 cfg.AllocatedCapitalUSDT。
	// 0 表示未配置 → /api/performance 因分母缺失判 Insufficient(不静默猜)。
	allocatedCapitalUSDT float64
}

// Orchestrator 持有所有 venue 运行时,按 venue 名路由控制/读取请求。primary 是单所阶段(或缺省)使用的所。
type Orchestrator struct {
	mu      sync.RWMutex
	venues  map[string]*venueRuntime
	order   []string // 稳定展示/遍历顺序(注册序)
	primary string   // 缺省 venue 名(单所阶段 = "binance")
}

// NewOrchestrator 创建空编排器。
func NewOrchestrator() *Orchestrator {
	return &Orchestrator{venues: make(map[string]*venueRuntime)}
}

// recordDirForVenue 返回某 venue 的记录根目录(P3b 隔离的单一事实源)。
//
// ⚠️ 后向兼容:币安(primary)**沿用** cfg.NetworkConfig.RecordDir(默认 "records")——服务器上既有实盘
// 记录/对账/单写租约/halt 文件都在该目录,改路径会孤立既有数据、破坏断点恢复。其它所隔离到
// <recordDir>/<venue>/,各自一套 ReportStore / 单写租约(.lingxi-live.lock)/ 风控状态 / halt 文件。
func recordDirForVenue(cfg EngineConfig, venueName string) string {
	base := strings.TrimSpace(cfg.NetworkConfig.RecordDir)
	if base == "" {
		base = "records"
	}
	if venueName == binance.VenueName {
		return base
	}
	return filepath.Join(base, venueName)
}

// buildVenueRuntime 为某 venue 组装独立的 (ReportStore, RuntimeController)。每所持有「以该所记录根目录为 recordDir」
// 的 cfg 副本,使单写租约/紧急停机/风控状态文件天然按所隔离(§7.2)。币安取值与改造前逐字节一致(recordDir=records/)。
// venueEngineConfig 从基 cfg 派生某 venue 的引擎配置:把 RecordDir 切到该所目录(币安=records/ 后向兼容),
// 并注入「全局急停总闸」路径(§7.4 双级急停之全局级)。
//
// 全局总闸固定锚在**基记录目录**(币安基目录 records/ → records/.halt-live,即改造前那条总闸);用「基目录 cfg」
// 复用 effectiveEmergencyStopFile 同一套解析,确保 EmergencyStopFile 自定义文件名/绝对路径语义与本所级一致。
// 币安基目录 = 本所目录 → 全局与本所 halt 解析为同一路径,emergencyStopPaths 去重后行为与改造前逐字节一致。
func venueEngineConfig(cfg EngineConfig, venueName string) EngineConfig {
	venueCfg := cfg
	venueCfg.NetworkConfig.RecordDir = recordDirForVenue(cfg, venueName)
	venueCfg.NetworkConfig.GlobalEmergencyStopFile = effectiveEmergencyStopFile(cfg)
	return venueCfg
}

func buildVenueRuntime(ctx context.Context, cfg EngineConfig, venueName string, logger *log.Logger) (*venueRuntime, error) {
	venueCfg := venueEngineConfig(cfg, venueName)

	store, err := NewReportStore(venueCfg.NetworkConfig.RecordDir)
	if err != nil {
		return nil, err
	}
	// 日志 venue 维度(§12.1):非币安所给 logger 加 `[venue=<name>] ` 前缀,四所并行时进程日志可按所区分;
	// 币安(primary)沿用原 logger(无前缀)→ 既有日志逐字节不变。结构化记录已按 records/<venue>/ 物理隔离。
	venueLogger := venueScopedLogger(logger, venueName)
	controller, err := NewRuntimeController(ctx, store, venueCfg, venueLogger)
	if err != nil {
		return nil, err
	}
	controller.venueName = venueName // 资金快照/调仓据此分支(币安走引擎,其它走 venue 适配器)
	return &venueRuntime{
		name:                 venueName,
		controller:           controller,
		store:                store,
		allocatedCapitalUSDT: venueCfg.AllocatedCapitalUSDT,
	}, nil
}

// registerEnabledVenues 按 enabled 计划逐所构建并注册运行时,实现**启动隔离**(§7.9 / §12.1):某所构建失败
// 或 panic → 记录告警并跳过(该所 disabled),**绝不阻塞其它所注册**。build 为注入的运行时构造器(便于单测打桩,
// 也便于真实路径复用 buildVenueRuntime)。返回成功注册的所数。
func registerEnabledVenues(orch *Orchestrator, plans []VenuePlan, build func(VenuePlan) (*venueRuntime, error), logger *log.Logger) int {
	logf := func(format string, args ...any) {
		if logger != nil {
			logger.Printf(format, args...)
		}
	}
	registered := 0
	for _, plan := range plans {
		func() {
			defer func() {
				if rec := recover(); rec != nil {
					logf("[启动隔离] venue=%s 构建 panic,已禁用该所,不影响其它所: %v", plan.Name, rec)
				}
			}()
			rt, err := build(plan)
			if err != nil || rt == nil {
				logf("[启动隔离] venue=%s 启动失败,已禁用该所,其它所照常 Start: %v", plan.Name, err)
				return
			}
			orch.Register(rt)
			registered++
			logf("[编排] venue=%s 运行时已注册(enabled,默认 idle,plan.autostart=%v)", plan.Name, plan.Autostart)
		}()
	}
	return registered
}

// venueScopedLogger 为非 primary(币安)所派生带 `[venue=<name>] ` 前缀的 logger,使单进程多引擎的进程日志
// 可按所区分(§12.1)。币安所或入参为空时原样返回 → 既有日志零变化。复用同一底层 writer/flags。
func venueScopedLogger(logger *log.Logger, venueName string) *log.Logger {
	if logger == nil || venueName == "" || venueName == binance.VenueName {
		return logger
	}
	return log.New(logger.Writer(), fmt.Sprintf("%s[venue=%s] ", logger.Prefix(), venueName), logger.Flags())
}

// Register 登记一个 venue 运行时。首个登记者自动成为 primary(可被 SetPrimary 覆盖)。
func (o *Orchestrator) Register(rt *venueRuntime) {
	if o == nil || rt == nil || rt.name == "" {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, exists := o.venues[rt.name]; !exists {
		o.order = append(o.order, rt.name)
	}
	o.venues[rt.name] = rt
	if o.primary == "" {
		o.primary = rt.name
	}
}

// Get 按 venue 名取运行时。
func (o *Orchestrator) Get(name string) (*venueRuntime, bool) {
	if o == nil {
		return nil, false
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	rt, ok := o.venues[name]
	return rt, ok
}

// Primary 返回缺省 venue 运行时(单所阶段即唯一那个)。
func (o *Orchestrator) Primary() *venueRuntime {
	if o == nil {
		return nil
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	if o.primary == "" {
		return nil
	}
	return o.venues[o.primary]
}

// All 按注册顺序返回所有 venue 运行时副本切片(遍历/聚合用)。
func (o *Orchestrator) All() []*venueRuntime {
	if o == nil {
		return nil
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	out := make([]*venueRuntime, 0, len(o.order))
	for _, name := range o.order {
		if rt := o.venues[name]; rt != nil {
			out = append(out, rt)
		}
	}
	return out
}

// Names 返回已注册 venue 名(排序,稳定输出)。
func (o *Orchestrator) Names() []string {
	if o == nil {
		return nil
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	names := append([]string(nil), o.order...)
	sort.Strings(names)
	return names
}
