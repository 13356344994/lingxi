package app

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"strings"
	"time"
)

// 本文件从 runtime_controller.go 拆出：仅承载实盘启动前的安全前置检查（dashboard 本地监听、
// records 目录可写、emergency stop、冷却期、服务器时间偏移、交易资金充足校验）与相关纯校验
// 助手（同包搬移，行为不变），让 runtime_controller.go 满足单文件 ≤ 800 行门禁。

func (c *RuntimeController) runLiveStartupChecks(ctx context.Context, secretPath string, cfg EngineConfig, runID string, cooldownUntil string) error {
	c.appendControllerTraceLog(runID, "", "开始执行实盘启动前检查")
	if err := validateLocalDashboardAddr(cfg.NetworkConfig.DashboardAddr); err != nil {
		return err
	}
	c.appendControllerTraceLog(runID, "", "启动前检查通过: dashboard 仅监听本地地址")
	if err := ensureWritableDirectory(cfg.NetworkConfig.RecordDir); err != nil {
		return err
	}
	c.appendControllerTraceLog(runID, "", "启动前检查通过: records 目录可写")
	if err := ensureEmergencyStopsClear(emergencyStopPaths(cfg)); err != nil {
		return err
	}
	c.appendControllerTraceLog(runID, "", "启动前检查通过: 未检测到 emergency stop 文件")
	if err := ensureCooldownExpired(cooldownUntil); err != nil {
		return err
	}
	if strings.TrimSpace(cooldownUntil) != "" {
		c.appendControllerTraceLog(runID, "", "启动前检查通过: 本地冷却期已结束")
	}

	engine, err := NewFundingRateSniperEngineWithConfig(secretPath, cfg)
	if err != nil {
		return fmt.Errorf("创建实盘引擎失败: %w", err)
	}
	engine.runID = strings.TrimSpace(runID)
	engine.setTraceLogIDs(engine.runID, "")
	engine.logger = log.New(newRuntimeLogWriter(c.logs, "live-preflight", os.Stdout, engine.traceIDs, engine.recordMetadata), "[LivePreflight] ", log.LstdFlags|log.Lmicroseconds)

	if err := engine.syncServerTimeOffset(ctx); err != nil {
		return fmt.Errorf("同步服务器时间失败: %w", err)
	}
	offset := engine.serverTimeOffsetDuration()
	if offset < 0 {
		offset = -offset
	}
	if offset > maxServerTimeOffset {
		return fmt.Errorf("服务器时间偏移过大: %s，超过允许阈值 %s", offset, maxServerTimeOffset)
	}
	c.appendControllerTraceLog(runID, "", "启动前检查通过: 服务器时间偏移=%s", engine.serverTimeOffsetDuration())
	if err := engine.verifyTradingEnvironment(ctx); err != nil {
		return err
	}
	if err := engine.verifyAPIKeyPermissions(ctx); err != nil {
		return err
	}
	c.appendControllerTraceLog(runID, "", "启动前检查通过: API Key 无提现权限")
	requiredTradingUSDT := configuredTradingCapitalUSDT(cfg)
	if requiredTradingUSDT > 0 {
		snapshot, err := engine.captureRuntimeCapitalSnapshot(ctx)
		if err != nil {
			return fmt.Errorf("读取实盘可调 USDT 失败: %w", err)
		}
		if snapshot.TotalMovableUSDT+executionQtyTolerance < requiredTradingUSDT {
			return fmt.Errorf("实盘启动前检查失败：当前配置要求交易资金 %.2f USDT，但总可调 USDT 仅 %.2f", requiredTradingUSDT, snapshot.TotalMovableUSDT)
		}
		c.appendControllerTraceLog(runID, "", "启动前检查通过: 总可调 USDT=%.2f，满足配置交易资金 %.2f", snapshot.TotalMovableUSDT, requiredTradingUSDT)
	}
	c.appendControllerTraceLog(runID, "", "实盘启动前检查已全部通过")
	return nil
}

func validateLocalDashboardAddr(addr string) error {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return fmt.Errorf("dashboard 地址不能为空")
	}
	host := addr
	if strings.Contains(addr, "://") {
		parsed, err := url.Parse(addr)
		if err != nil {
			return fmt.Errorf("dashboard 地址格式非法: %w", err)
		}
		host = parsed.Host
	}
	if parsedHost, _, err := net.SplitHostPort(host); err == nil {
		host = parsedHost
	} else if addrErr, ok := err.(*net.AddrError); ok && !strings.Contains(addrErr.Err, "missing port in address") {
		return fmt.Errorf("dashboard 地址格式非法: %w", err)
	}
	host = strings.Trim(host, "[]")
	switch strings.ToLower(strings.TrimSpace(host)) {
	case "", "127.0.0.1", "localhost", "::1":
		return nil
	default:
		return fmt.Errorf("dashboard 必须只监听本地地址，当前配置=%s", addr)
	}
}

func ensureWritableDirectory(dir string) error {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return fmt.Errorf("records 目录不能为空")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建 records 目录失败: %w", err)
	}
	tempFile, err := os.CreateTemp(dir, ".lingxi-write-check-*")
	if err != nil {
		return fmt.Errorf("records 目录不可写: %w", err)
	}
	path := tempFile.Name()
	if _, err := tempFile.WriteString("ok"); err != nil {
		_ = tempFile.Close()
		_ = os.Remove(path)
		return fmt.Errorf("records 目录写入校验失败: %w", err)
	}
	if err := tempFile.Close(); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("records 目录关闭校验文件失败: %w", err)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("records 目录清理校验文件失败: %w", err)
	}
	return nil
}

func ensureCooldownExpired(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	until, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return fmt.Errorf("读取当前冷却时间失败: %w", err)
	}
	if until.After(time.Now().UTC()) {
		return fmt.Errorf("当前仍处于冷却期，截止到 %s", until.UTC().Format(time.RFC3339))
	}
	return nil
}
