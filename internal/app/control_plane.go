package app

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"
)

// controlPlaneFileName 统一控制面持久化文件(附加层,不改 config schema)。
const controlPlaneFileName = "control_plane.json"

// ControlPlanePersistedState 是 control_plane.json 落盘结构。
// 默认值(文件不存在)须与改造前行为等价:各闸默认放行,仅 .halt-live / 既有开关生效。
type ControlPlanePersistedState struct {
	GlobalHalt          bool   `json:"global_halt"`
	AllowNewOpens       bool   `json:"allow_new_opens"`
	AllowManageOrders   bool   `json:"allow_manage_orders"`
	AllowVerifiedCloses bool   `json:"allow_verified_closes"`
	PerpPerpLiveEnabled bool   `json:"perp_perp_live_enabled"`
	RotationLiveEnabled bool   `json:"rotation_live_enabled"`
	UpdatedAt           string `json:"updated_at,omitempty"`
}

// ControlPlaneStatusView 供面板/API 展示的统一控制态(含运行时合成字段)。
type ControlPlaneStatusView struct {
	GlobalHalt          bool   `json:"global_halt"`
	HaltFileActive      bool   `json:"halt_file_active"`
	EffectiveGlobalHalt bool   `json:"effective_global_halt"`
	AllowNewOpens       bool   `json:"allow_new_opens"`
	AllowManageOrders   bool   `json:"allow_manage_orders"`
	AllowVerifiedCloses bool   `json:"allow_verified_closes"`
	PerpPerpLiveEnabled bool   `json:"perp_perp_live_enabled"`
	RotationLiveEnabled bool   `json:"rotation_live_enabled"`
	PerpPerpConfigLive  bool   `json:"perp_perp_config_live"`
	PerpPerpOpeningsOn  bool   `json:"perp_perp_openings_on"`
	RotationActive      bool   `json:"rotation_active"`
	PerpPerpCanOpen     bool   `json:"perp_perp_can_open"`
	RotationCanOpen     bool   `json:"rotation_can_open"`
	PerpPerpMode        string `json:"perp_perp_mode"`
	RotationMode        string `json:"rotation_mode"`
	StrategyMode        string `json:"strategy_mode"`
	PerpPerpRunMode     string `json:"perp_perp_run_mode"`
	RotationRunMode     string `json:"rotation_run_mode"`
}

// ControlPlane 统一控制面:线程安全,持久化到 records/control_plane.json。
// 与既有 .halt-live / openings / rotation active 取 AND,只加闸不放宽。
type ControlPlane struct {
	mu        sync.RWMutex
	recordDir string
	haltPaths []string
	state     ControlPlanePersistedState
	logger    *log.Logger
}

func controlPlaneFilePath(recordDir string) string {
	dir := strings.TrimSpace(recordDir)
	if dir == "" {
		dir = "records"
	}
	return dir + "/" + controlPlaneFileName
}

func defaultControlPlaneState() ControlPlanePersistedState {
	return ControlPlanePersistedState{
		GlobalHalt:          false,
		AllowNewOpens:       true,
		AllowManageOrders:   true,
		AllowVerifiedCloses: true,
		PerpPerpLiveEnabled: true,
		RotationLiveEnabled: true,
	}
}

// NewControlPlane 构造并加载持久化状态;haltPaths 用于 .halt-live 并入 global_halt 语义。
func NewControlPlane(recordDir string, haltPaths []string, logger *log.Logger) *ControlPlane {
	cp := &ControlPlane{
		recordDir: strings.TrimSpace(recordDir),
		haltPaths: append([]string(nil), haltPaths...),
		state:     defaultControlPlaneState(),
		logger:    logger,
	}
	if cp.logger == nil {
		cp.logger = log.Default()
	}
	if st, ok, err := loadControlPlaneState(cp.recordDir); err != nil {
		if cp.logger != nil {
			cp.logger.Printf("[控制面] 读取 %s 失败,使用默认(零行为变化): %v", controlPlaneFilePath(cp.recordDir), err)
		}
	} else if ok {
		cp.state = st
	}
	return cp
}

func loadControlPlaneState(recordDir string) (ControlPlanePersistedState, bool, error) {
	path := controlPlaneFilePath(recordDir)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ControlPlanePersistedState{}, false, nil
		}
		return ControlPlanePersistedState{}, false, err
	}
	raw := string(data)
	var st ControlPlanePersistedState
	if err := json.Unmarshal(data, &st); err != nil {
		return ControlPlanePersistedState{}, false, fmt.Errorf("解析 %s 失败: %w", path, err)
	}
	// 旧文件缺字段时 json 零值为 false,会错误收紧闸;补默认 true。
	def := defaultControlPlaneState()
	if !strings.Contains(raw, `"allow_new_opens"`) {
		st.AllowNewOpens = def.AllowNewOpens
	}
	if !strings.Contains(raw, `"allow_manage_orders"`) {
		st.AllowManageOrders = def.AllowManageOrders
	}
	if !strings.Contains(raw, `"allow_verified_closes"`) {
		st.AllowVerifiedCloses = def.AllowVerifiedCloses
	}
	if !strings.Contains(raw, `"perp_perp_live_enabled"`) {
		st.PerpPerpLiveEnabled = def.PerpPerpLiveEnabled
	}
	if !strings.Contains(raw, `"rotation_live_enabled"`) {
		st.RotationLiveEnabled = def.RotationLiveEnabled
	}
	return st, true, nil
}

func saveControlPlaneState(recordDir string, st ControlPlanePersistedState) error {
	path := controlPlaneFilePath(recordDir)
	dir := strings.TrimSpace(recordDir)
	if dir == "" {
		dir = "records"
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	st.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if err := writeJSONFile(path, st); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

// PersistedState 返回持久化字段快照(不含 .halt-live 运行时态)。
func (cp *ControlPlane) PersistedState() ControlPlanePersistedState {
	if cp == nil {
		return defaultControlPlaneState()
	}
	cp.mu.RLock()
	defer cp.mu.RUnlock()
	return cp.state
}

func (cp *ControlPlane) haltFileActive() bool {
	if cp == nil {
		return false
	}
	return ensureEmergencyStopsClear(cp.haltPaths) != nil
}

// IsGlobalHaltActive 持久化 global_halt 或 .halt-live 文件存在即视为总闸生效。
func (cp *ControlPlane) IsGlobalHaltActive() bool {
	if cp == nil {
		return false
	}
	cp.mu.RLock()
	persisted := cp.state.GlobalHalt
	cp.mu.RUnlock()
	return persisted || cp.haltFileActive()
}

// IsEmergencyHaltActive 仅 .halt-live 紧急文件(人工硬急停)生效即返回 true;
// 不含账户回撤型持久 global_halt。供"探索档不因普通风控停机、只为人工急停而停"使用。
func (cp *ControlPlane) IsEmergencyHaltActive() bool {
	if cp == nil {
		return false
	}
	return cp.haltFileActive()
}

func (cp *ControlPlane) AllowNewOpens() bool {
	if cp == nil {
		return true
	}
	cp.mu.RLock()
	defer cp.mu.RUnlock()
	return cp.state.AllowNewOpens
}

func (cp *ControlPlane) AllowManageOrders() bool {
	if cp == nil {
		return true
	}
	cp.mu.RLock()
	defer cp.mu.RUnlock()
	return cp.state.AllowManageOrders
}

func (cp *ControlPlane) AllowVerifiedCloses() bool {
	if cp == nil {
		return true
	}
	cp.mu.RLock()
	defer cp.mu.RUnlock()
	return cp.state.AllowVerifiedCloses
}

func (cp *ControlPlane) PerpPerpLiveEnabled() bool {
	if cp == nil {
		return true
	}
	cp.mu.RLock()
	defer cp.mu.RUnlock()
	return cp.state.PerpPerpLiveEnabled
}

func (cp *ControlPlane) RotationLiveEnabled() bool {
	if cp == nil {
		return true
	}
	cp.mu.RLock()
	defer cp.mu.RUnlock()
	return cp.state.RotationLiveEnabled
}

func (cp *ControlPlane) setPersisted(mut func(*ControlPlanePersistedState)) error {
	if cp == nil {
		return fmt.Errorf("控制面未初始化")
	}
	cp.mu.Lock()
	mut(&cp.state)
	st := cp.state
	cp.mu.Unlock()
	if cp.recordDir == "" {
		return nil
	}
	if err := saveControlPlaneState(cp.recordDir, st); err != nil {
		return err
	}
	if cp.logger != nil {
		cp.logger.Printf("[控制面] 状态已落盘 %s global_halt=%v allow_new_opens=%v",
			controlPlaneFilePath(cp.recordDir), st.GlobalHalt, st.AllowNewOpens)
	}
	return nil
}

// SetGlobalHalt 设置持久化总闸(面板 STOP/START 使用);.halt-live 仍独立生效。
func (cp *ControlPlane) SetGlobalHalt(on bool) error {
	return cp.setPersisted(func(st *ControlPlanePersistedState) {
		st.GlobalHalt = on
	})
}

// BuildStatusView 合成可读状态,消除 config 与 runtime 不一致误导。
func (cp *ControlPlane) BuildStatusView(perpPerpConfigLive, perpPerpOpeningsOn, rotationActive, perpPerpHealthHalt bool) ControlPlaneStatusView {
	st := cp.PersistedState()
	haltFile := cp.haltFileActive()
	effectiveHalt := st.GlobalHalt || haltFile

	perpCanOpen := perpPerpConfigLive && perpPerpOpeningsOn && st.AllowNewOpens && st.PerpPerpLiveEnabled &&
		!effectiveHalt && !perpPerpHealthHalt
	rotCanOpen := rotationActive && st.AllowNewOpens && st.RotationLiveEnabled && !effectiveHalt

	perpMode := "OFF"
	if perpPerpConfigLive {
		if perpCanOpen {
			perpMode = "LIVE"
		} else if effectiveHalt {
			perpMode = "halt"
		} else {
			perpMode = "paused"
		}
	}
	rotMode := "OFF"
	if rotationActive {
		if rotCanOpen {
			rotMode = "LIVE"
		} else if effectiveHalt {
			rotMode = "halt"
		} else {
			rotMode = "paused"
		}
	} else if effectiveHalt {
		rotMode = "halt"
	}

	return ControlPlaneStatusView{
		GlobalHalt:          st.GlobalHalt,
		HaltFileActive:      haltFile,
		EffectiveGlobalHalt: effectiveHalt,
		AllowNewOpens:       st.AllowNewOpens,
		AllowManageOrders:   st.AllowManageOrders,
		AllowVerifiedCloses: st.AllowVerifiedCloses,
		PerpPerpLiveEnabled: st.PerpPerpLiveEnabled,
		RotationLiveEnabled: st.RotationLiveEnabled,
		PerpPerpConfigLive:  perpPerpConfigLive,
		PerpPerpOpeningsOn:  perpPerpOpeningsOn,
		RotationActive:      rotationActive,
		PerpPerpCanOpen:     perpCanOpen,
		RotationCanOpen:     rotCanOpen,
		PerpPerpMode:        perpMode,
		RotationMode:        rotMode,
	}
}
