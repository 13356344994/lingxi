package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// single_writer_lease.go 实现 S7-A「单写租约」(R14/HA):用文件 flock(LOCK_EX|LOCK_NB)保证同一 records 目录
// (即同一账户/部署)同一时刻只有一个实盘写进程,防止「双开」导致的双写撞单、仓位/对账错乱、风控状态互相覆盖。
//
// 为什么用 flock:advisory lock 由内核持有,进程退出(含崩溃/被 kill)时自动释放,天然规避「陈旧锁」问题——
// 无需心跳续期、无需手工清理。锁文件内写入持有者(pid/host/时间)仅供诊断,不参与互斥语义。

const singleWriterLeaseFileName = ".lingxi-live.lock"

// singleWriterLease 持有一个实盘单写租约。Release 前一直占用底层 fd 上的 flock。
type singleWriterLease struct {
	path string
	file *os.File
}

// acquireSingleWriterLease 在 dir 下以非阻塞独占方式获取单写租约。已有持有者时立即失败(不等待),
// 并在错误信息里带上既有持有者诊断信息,便于运维定位是哪台/哪个 pid 在跑。
func acquireSingleWriterLease(dir string) (*singleWriterLease, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil, fmt.Errorf("单写租约目录不能为空")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("创建单写租约目录失败: %w", err)
	}
	path := filepath.Join(dir, singleWriterLeaseFileName)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("打开单写租约文件失败: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		existing := strings.TrimSpace(readLeaseHolder(path))
		_ = file.Close()
		if existing == "" {
			existing = "未知(锁文件无持有者信息)"
		}
		return nil, fmt.Errorf("获取单写租约失败:已有实例持有 %s,拒绝双开以防双写同一账户(撞单/对账错乱)。既有持有者: %s; 底层错误: %w", path, existing, err)
	}
	// 抢锁成功后写入持有者诊断信息(覆盖旧内容)。
	holder := fmt.Sprintf("pid=%d host=%s acquiredAt=%s", os.Getpid(), leaseHostname(), time.Now().UTC().Format(time.RFC3339))
	if truncErr := file.Truncate(0); truncErr == nil {
		if _, seekErr := file.Seek(0, 0); seekErr == nil {
			_, _ = file.WriteString(holder + "\n")
			_ = file.Sync()
		}
	}
	return &singleWriterLease{path: path, file: file}, nil
}

// Release 释放 flock 并关闭文件。可安全多次调用 / 对 nil 调用。
func (l *singleWriterLease) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	_ = syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	err := l.file.Close()
	l.file = nil
	return err
}

func readLeaseHolder(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

func leaseHostname() string {
	if h, err := os.Hostname(); err == nil && strings.TrimSpace(h) != "" {
		return h
	}
	return "unknown"
}
