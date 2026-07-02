package app

// 存储与急停文件的通用薄封装(原散落于 report_store_io.go / live_safety.go,遗留引擎删除后在此存活)。

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"lingxi/internal/store"
)

func writeJSONFile(path string, payload any) error { return store.WriteJSONFile(path, payload) }

func appendJSONLRecord(path string, payload any) error { return store.AppendJSONLRecord(path, payload) }

// defaultEmergencyStopFile 急停文件默认名(records/.halt-live)。
const defaultEmergencyStopFile = ".halt-live"

// effectiveEmergencyStopFile 解析本所急停文件路径(支持自定义文件名/绝对路径)。
func effectiveEmergencyStopFile(cfg EngineConfig) string {
	path := strings.TrimSpace(cfg.NetworkConfig.EmergencyStopFile)
	recordDir := strings.TrimSpace(cfg.NetworkConfig.RecordDir)
	if path == "" {
		if recordDir == "" {
			return defaultEmergencyStopFile
		}
		return filepath.Join(recordDir, defaultEmergencyStopFile)
	}
	if filepath.IsAbs(path) || recordDir == "" {
		return path
	}
	return filepath.Join(recordDir, path)
}

// emergencyStopPaths 返回某 cfg 下需检查的急停文件路径集合:全局总闸在前、本所 halt 在后。
func emergencyStopPaths(cfg EngineConfig) []string {
	perVenue := strings.TrimSpace(effectiveEmergencyStopFile(cfg))
	global := strings.TrimSpace(cfg.NetworkConfig.GlobalEmergencyStopFile)
	return dedupEmergencyStopPaths(global, perVenue)
}

// dedupEmergencyStopPaths 拼出 [global, perVenue] 并去重/去空,保持「全局在前」的检查序。
func dedupEmergencyStopPaths(global, perVenue string) []string {
	var paths []string
	if global != "" {
		paths = append(paths, global)
	}
	if perVenue != "" && perVenue != global {
		paths = append(paths, perVenue)
	}
	return paths
}

// ensureEmergencyStopsClear 依次检查多个急停文件(全局 + 本所),任一存在即返回阻断错误。
func ensureEmergencyStopsClear(paths []string) error {
	for _, p := range paths {
		if err := ensureEmergencyStopClear(p); err != nil {
			return err
		}
	}
	return nil
}

func ensureEmergencyStopClear(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	_, err := os.Stat(path)
	if err == nil {
		return fmt.Errorf("检测到 emergency stop 文件，实盘启动已阻断: %s", path)
	}
	if os.IsNotExist(err) {
		return nil
	}
	return fmt.Errorf("检查 emergency stop 文件失败: %w", err)
}

// crossVenueRecordDir 历史 crossvenue/ 子目录(权益快照/采样健康信号沿用,保证既有数据连续)。
func crossVenueRecordDir(base string) string { return accountEquityRecordDir(base) }
