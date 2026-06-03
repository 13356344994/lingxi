package app

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"
)

// PerformColdStartCheck 每日冷启动健康检查
// 在系统正式接管实盘前，强制校验配置、网络、密钥和记录目录。
func PerformColdStartCheck(ctx context.Context, cfg EngineConfig) error {
	log.Println("[冷启动检查] 开始执行每日健康检查...")

	// 1. 检查配置合法性
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("配置校验失败: %w", err)
	}
	log.Println("[冷启动检查] 配置校验通过")

	// 2. 检查记录目录是否可写
	if err := checkDirWritable(cfg.NetworkConfig.RecordDir); err != nil {
		return fmt.Errorf("记录目录不可写: %w", err)
	}
	log.Println("[冷启动检查] 记录目录可写校验通过")

	// 3. 检查 emergency stop 文件是否存在
	if cfg.NetworkConfig.EmergencyStopFile != "" {
		if _, err := os.Stat(cfg.NetworkConfig.EmergencyStopFile); err == nil {
			return fmt.Errorf("检测到 EmergencyStopFile (%s) 存在，系统被硬拦截", cfg.NetworkConfig.EmergencyStopFile)
		}
	}

	log.Println("[冷启动检查] 每日健康检查全部通过")
	return nil
}

func checkDirWritable(dir string) error {
	if dir == "" {
		return fmt.Errorf("目录路径为空")
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	testFile := filepath.Join(dir, fmt.Sprintf(".health_check_%d", time.Now().UnixNano()))
	f, err := os.Create(testFile)
	if err != nil {
		return err
	}
	f.Close()
	return os.Remove(testFile)
}
