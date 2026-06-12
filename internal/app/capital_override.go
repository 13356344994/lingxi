package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// capital_override.go:页面调仓后的「分配资金」持久化覆盖。
//
// 问题:页面调仓只做真实资金划转(把账户调到目标 USDT),但引擎的分配资金一直读
// config.yaml 的 allocated_capital_usdt——调仓到 127U 后执行器重启仍按 60U 跑,
// 多出的钱永久闲置。修复:调仓成功后把目标值写进 <recordDir>/capital_override.json,
// 任务启动时若存在覆盖文件则以它为准(优先级:覆盖文件 > config.yaml)。

const capitalOverrideFileName = "capital_override.json"

type venueCapitalOverride struct {
	AllocatedCapitalUSDT float64 `json:"allocated_capital_usdt"`
	UpdatedAt            string  `json:"updated_at"`
}

// writeVenueCapitalOverride 把调仓目标落盘为分配资金覆盖。
func writeVenueCapitalOverride(recordDir string, target float64) error {
	if strings.TrimSpace(recordDir) == "" {
		return fmt.Errorf("recordDir 为空,无法持久化分配资金覆盖")
	}
	if err := os.MkdirAll(recordDir, 0o755); err != nil {
		return err
	}
	return writeJSONFile(filepath.Join(recordDir, capitalOverrideFileName), venueCapitalOverride{
		AllocatedCapitalUSDT: target,
		UpdatedAt:            time.Now().UTC().Format(time.RFC3339),
	})
}

// loadVenueCapitalOverride 读取分配资金覆盖;文件不存在/损坏/非正值返回 (0,false)(回退 config.yaml)。
func loadVenueCapitalOverride(recordDir string) (float64, bool) {
	if strings.TrimSpace(recordDir) == "" {
		return 0, false
	}
	data, err := os.ReadFile(filepath.Join(recordDir, capitalOverrideFileName))
	if err != nil {
		return 0, false
	}
	var ov venueCapitalOverride
	if err := json.Unmarshal(data, &ov); err != nil || ov.AllocatedCapitalUSDT <= 0 {
		return 0, false
	}
	return ov.AllocatedCapitalUSDT, true
}
