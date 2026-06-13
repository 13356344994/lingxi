package app

import (
	"encoding/json"
	"fmt"
	"os"
)

// crossVenueControlFileName 跨所执行引擎运行时控制状态(开仓开关等),落盘于 records/crossvenue/control.json。
const crossVenueControlFileName = "control.json"

// crossVenueControlState 是 control.json 的落盘结构(原子写)。
type crossVenueControlState struct {
	OpeningsEnabled bool `json:"openings_enabled"`
}

func crossVenueControlFilePath(recordDir string) string {
	return crossVenueExecutorRecordDir(recordDir) + "/" + crossVenueControlFileName
}

// loadCrossVenueControlState 读开仓开关;文件不存在时默认 false(停),由面板策略总控显式开启。
func loadCrossVenueControlState(recordDir string) (bool, error) {
	path := crossVenueControlFilePath(recordDir)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	var st crossVenueControlState
	if err := json.Unmarshal(data, &st); err != nil {
		return false, fmt.Errorf("解析 %s 失败: %w", path, err)
	}
	return st.OpeningsEnabled, nil
}

func saveCrossVenueControlState(recordDir string, enabled bool) error {
	path := crossVenueControlFilePath(recordDir)
	if err := os.MkdirAll(crossVenueExecutorRecordDir(recordDir), 0o755); err != nil {
		return err
	}
	return writeJSONFile(path, crossVenueControlState{OpeningsEnabled: enabled})
}

// OpeningsEnabled 返回运行时开仓开关(第三道闸,叠加 enabled/live 双闸,不替代)。
func (e *CrossVenueExecutor) OpeningsEnabled() bool {
	if e == nil {
		return false
	}
	return e.openingsEnabled.Load()
}

// SetOpeningsEnabled 设置运行时开仓开关并原子落盘;OFF 时跳过 evaluateNewOpens,manageOpenPositions 不受影响。
func (e *CrossVenueExecutor) SetOpeningsEnabled(enabled bool) error {
	if e == nil {
		return fmt.Errorf("跨所执行引擎未初始化")
	}
	e.openingsEnabled.Store(enabled)
	if e.recordDir == "" {
		return nil
	}
	if err := saveCrossVenueControlState(e.recordDir, enabled); err != nil {
		return err
	}
	if e.logger != nil {
		state := "关"
		if enabled {
			state = "开"
		}
		e.logger.Printf("[跨所执行] 运行时开仓开关已设为 %s(已落盘 %s)", state, crossVenueControlFilePath(e.recordDir))
	}
	return nil
}
