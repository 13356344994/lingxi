package app

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestVenueEngineConfigDualHaltPaths 钉死双级急停的路径派生(§7.4):
// 币安(基目录 = 本所目录)全局与本所 halt 解析为同一路径,emergencyStopPaths 去重为一条(零行为变更);
// 其它所本所 halt 落到 records/<venue>/.halt-live,全局总闸仍锚在基目录 records/.halt-live → 两条。
func TestVenueEngineConfigDualHaltPaths(t *testing.T) {
	var base EngineConfig
	base.NetworkConfig.RecordDir = "records"

	binanceCfg := venueEngineConfig(base, "binance")
	if binanceCfg.NetworkConfig.RecordDir != "records" {
		t.Fatalf("币安应沿用基目录 records, got %q", binanceCfg.NetworkConfig.RecordDir)
	}
	if got := emergencyStopPaths(binanceCfg); len(got) != 1 || got[0] != filepath.Join("records", ".halt-live") {
		t.Fatalf("币安双级急停应去重为单条 records/.halt-live, got %v", got)
	}

	okxCfg := venueEngineConfig(base, "okx")
	if okxCfg.NetworkConfig.RecordDir != filepath.Join("records", "okx") {
		t.Fatalf("okx 本所目录应为 records/okx, got %q", okxCfg.NetworkConfig.RecordDir)
	}
	got := emergencyStopPaths(okxCfg)
	want := []string{filepath.Join("records", ".halt-live"), filepath.Join("records", "okx", ".halt-live")}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("okx 双级急停应为 [全局, 本所], got %v want %v", got, want)
	}
}

// TestDedupEmergencyStopPathsOrderingAndEmpties 校验「全局在前、去重去空」拼接规则。
func TestDedupEmergencyStopPathsOrderingAndEmpties(t *testing.T) {
	if got := dedupEmergencyStopPaths("g", "v"); len(got) != 2 || got[0] != "g" || got[1] != "v" {
		t.Fatalf("应 [global, perVenue], got %v", got)
	}
	if got := dedupEmergencyStopPaths("same", "same"); len(got) != 1 || got[0] != "same" {
		t.Fatalf("重合应去重为一条, got %v", got)
	}
	if got := dedupEmergencyStopPaths("", "v"); len(got) != 1 || got[0] != "v" {
		t.Fatalf("空全局应只剩本所(后向兼容), got %v", got)
	}
	if got := dedupEmergencyStopPaths("g", ""); len(got) != 1 || got[0] != "g" {
		t.Fatalf("空本所应只剩全局, got %v", got)
	}
	if got := dedupEmergencyStopPaths("", ""); len(got) != 0 {
		t.Fatalf("全空应为空, got %v", got)
	}
}

// TestCheckEmergencyStopTripsOnEitherLevel 钉死双级急停的运行期守卫:全局总闸或本所 halt 任一存在即停;
// 全清才放行。验证全局总闸能停一个其本所 halt 仍干净的所(多所运维总闸语义)。
func TestCheckEmergencyStopTripsOnEitherLevel(t *testing.T) {
	dir := t.TempDir()
	globalPath := filepath.Join(dir, ".halt-live")
	venueDir := filepath.Join(dir, "okx")
	if err := os.MkdirAll(venueDir, 0o755); err != nil {
		t.Fatalf("建本所目录失败: %v", err)
	}
	venuePath := filepath.Join(venueDir, ".halt-live")

	engine := &FundingRateSniperEngine{
		cfg:                     DefaultEngineConfig(),
		logger:                  log.Default(),
		runtimeReporter:         newRuntimeReporter(nil),
		emergencyStopFile:       venuePath,
		globalEmergencyStopFile: globalPath,
		dailyRiskDate:           tradingDayUTC(time.Now().UTC()),
	}
	engine.runtimeReporter = newRuntimeReporter(engine)

	// 两级都干净 → 放行。
	if err := engine.checkEmergencyStop("test"); err != nil {
		t.Fatalf("两级急停文件均不存在应放行, got %v", err)
	}

	// 仅全局总闸存在 → 停(本所 halt 干净也要停)。
	if err := os.WriteFile(globalPath, []byte("halt"), 0o644); err != nil {
		t.Fatalf("写全局急停失败: %v", err)
	}
	err := engine.checkEmergencyStop("test")
	if err == nil {
		t.Fatal("全局总闸存在应触发停机")
	}
	if !strings.Contains(err.Error(), globalPath) {
		t.Fatalf("停机原因应指向全局总闸路径, got %v", err)
	}

	// 移除全局、仅本所 halt 存在 → 停(单所闸只停该所)。
	if err := os.Remove(globalPath); err != nil {
		t.Fatalf("移除全局急停失败: %v", err)
	}
	if err := os.WriteFile(venuePath, []byte("halt"), 0o644); err != nil {
		t.Fatalf("写本所急停失败: %v", err)
	}
	err = engine.checkEmergencyStop("test")
	if err == nil {
		t.Fatal("本所 halt 存在应触发停机")
	}
	if !strings.Contains(err.Error(), venuePath) {
		t.Fatalf("停机原因应指向本所 halt 路径, got %v", err)
	}

	// 两级都清除 → 重新放行,并清掉触发标记。
	if err := os.Remove(venuePath); err != nil {
		t.Fatalf("移除本所急停失败: %v", err)
	}
	if err := engine.checkEmergencyStop("test"); err != nil {
		t.Fatalf("清除全部急停文件后应恢复放行, got %v", err)
	}
	if engine.emergencyStopTriggered {
		t.Fatal("全清后 emergencyStopTriggered 应复位为 false")
	}
}
