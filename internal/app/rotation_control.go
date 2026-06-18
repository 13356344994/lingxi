package app

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

const rotationControlFileName = "rotation_control.json"

type rotationControlState struct {
	Active    bool   `json:"active"`
	Live      bool   `json:"live,omitempty"` // legacy; ignored on load (rotation is live when active)
	UpdatedAt string `json:"updated_at"`
}

func rotationControlFilePath(recordDir string) string {
	return strings.TrimSpace(recordDir) + "/" + rotationControlFileName
}

func loadRotationControlState(recordDir string) (rotationControlState, bool, error) {
	path := rotationControlFilePath(recordDir)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return rotationControlState{}, false, nil
		}
		return rotationControlState{}, false, err
	}
	var st rotationControlState
	if err := json.Unmarshal(data, &st); err != nil {
		return rotationControlState{}, false, fmt.Errorf("解析 %s 失败: %w", path, err)
	}
	return st, true, nil
}

func saveRotationControlState(recordDir string, active bool) error {
	dir := strings.TrimSpace(recordDir)
	if dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := rotationControlFilePath(dir)
	if err := writeJSONFile(path, rotationControlState{
		Active:    active,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}
