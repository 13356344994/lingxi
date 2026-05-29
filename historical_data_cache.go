package main

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const historicalCacheSchemaVersion = 2
const historicalCacheRetention = 6 * time.Hour

type historicalPayloadCache struct {
	dir           string
	lastCleanupAt time.Time
	mu            sync.Mutex
}

type historicalCacheBucket struct {
	Version int               `json:"version"`
	Entries map[string]string `json:"entries"`
}

func newHistoricalPayloadCache(dir string) (*historicalPayloadCache, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("创建历史缓存目录失败: %w", err)
	}
	cache := &historicalPayloadCache{dir: dir}
	if err := cache.cleanupExpiredLocked(time.Now().UTC()); err != nil {
		return nil, err
	}
	return cache, nil
}

func (c *historicalPayloadCache) Load(rawURL string) ([]byte, bool, error) {
	if c == nil {
		return nil, false, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	bucket, err := c.readBucketLocked(rawURL)
	if err != nil {
		return nil, false, err
	}
	payload, ok := bucket.Entries[c.entryKey(rawURL)]
	if !ok {
		return nil, false, nil
	}
	return []byte(payload), true, nil
}

func (c *historicalPayloadCache) Save(rawURL string, payload []byte) error {
	if c == nil || len(payload) == 0 {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	bucket, err := c.readBucketLocked(rawURL)
	if err != nil {
		return err
	}
	if bucket.Entries == nil {
		bucket.Entries = make(map[string]string)
	}
	bucket.Version = historicalCacheSchemaVersion
	bucket.Entries[c.entryKey(rawURL)] = string(payload)
	cachePath := c.bucketPath(rawURL)
	tempPath := cachePath + ".tmp"
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		return fmt.Errorf("创建历史缓存子目录失败: %w", err)
	}
	encoded, marshalErr := json.Marshal(bucket)
	if marshalErr != nil {
		return fmt.Errorf("序列化历史缓存失败: %w", marshalErr)
	}
	if err := os.WriteFile(tempPath, encoded, 0o644); err != nil {
		return fmt.Errorf("写入历史缓存临时文件失败: %w", err)
	}
	if err := os.Rename(tempPath, cachePath); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("提交历史缓存失败: %w", err)
	}
	if err := c.maybeCleanupLocked(time.Now().UTC()); err != nil {
		return err
	}
	return nil
}

func (c *historicalPayloadCache) Remove(rawURL string) error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	bucket, err := c.readBucketLocked(rawURL)
	if err != nil {
		return err
	}
	if bucket.Entries == nil {
		return nil
	}
	delete(bucket.Entries, c.entryKey(rawURL))
	cachePath := c.bucketPath(rawURL)
	if len(bucket.Entries) == 0 {
		if err := os.Remove(cachePath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("删除历史缓存失败: %w", err)
		}
		return nil
	}
	encoded, marshalErr := json.Marshal(bucket)
	if marshalErr != nil {
		return fmt.Errorf("序列化历史缓存失败: %w", marshalErr)
	}
	tempPath := cachePath + ".tmp"
	if err := os.WriteFile(tempPath, encoded, 0o644); err != nil {
		return fmt.Errorf("写入历史缓存临时文件失败: %w", err)
	}
	if err := os.Rename(tempPath, cachePath); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("提交历史缓存失败: %w", err)
	}
	return nil
}

func (c *historicalPayloadCache) Clear() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := os.RemoveAll(c.dir); err != nil {
		return fmt.Errorf("清理历史缓存失败: %w", err)
	}
	if err := os.MkdirAll(c.dir, 0o755); err != nil {
		return fmt.Errorf("重建历史缓存目录失败: %w", err)
	}
	return nil
}

func (c *historicalPayloadCache) readBucketLocked(rawURL string) (historicalCacheBucket, error) {
	cachePath := c.bucketPath(rawURL)
	if expired, err := c.removeBucketIfExpiredLocked(cachePath, time.Now().UTC()); err != nil {
		return historicalCacheBucket{}, err
	} else if expired {
		return historicalCacheBucket{Version: historicalCacheSchemaVersion, Entries: make(map[string]string)}, nil
	}
	payload, err := os.ReadFile(cachePath)
	if err == nil {
		var bucket historicalCacheBucket
		if decodeErr := json.Unmarshal(payload, &bucket); decodeErr != nil {
			_ = os.Remove(cachePath)
			return historicalCacheBucket{Version: historicalCacheSchemaVersion, Entries: make(map[string]string)}, nil
		}
		if bucket.Version != historicalCacheSchemaVersion {
			_ = os.Remove(cachePath)
			return historicalCacheBucket{Version: historicalCacheSchemaVersion, Entries: make(map[string]string)}, nil
		}
		if bucket.Entries == nil {
			bucket.Entries = make(map[string]string)
		}
		return bucket, nil
	}
	if os.IsNotExist(err) {
		return historicalCacheBucket{Version: historicalCacheSchemaVersion, Entries: make(map[string]string)}, nil
	}
	return historicalCacheBucket{}, fmt.Errorf("读取历史缓存失败: %w", err)
}

func (c *historicalPayloadCache) maybeCleanupLocked(now time.Time) error {
	if !c.lastCleanupAt.IsZero() && now.Sub(c.lastCleanupAt) < time.Hour {
		return nil
	}
	return c.cleanupExpiredLocked(now)
}

func (c *historicalPayloadCache) cleanupExpiredLocked(now time.Time) error {
	cutoff := now.Add(-historicalCacheRetention)
	if err := filepath.WalkDir(c.dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Ext(path) != ".json" {
			return nil
		}
		info, statErr := d.Info()
		if statErr != nil {
			return statErr
		}
		if info.ModTime().Before(cutoff) {
			if removeErr := os.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) {
				return removeErr
			}
		}
		return nil
	}); err != nil {
		return fmt.Errorf("清理历史缓存失败: %w", err)
	}
	c.lastCleanupAt = now
	return nil
}

func (c *historicalPayloadCache) removeBucketIfExpiredLocked(path string, now time.Time) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("检查历史缓存时间失败: %w", err)
	}
	if !info.ModTime().Before(now.Add(-historicalCacheRetention)) {
		return false, nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("移除过期历史缓存失败: %w", err)
	}
	return true, nil
}

func (c *historicalPayloadCache) bucketPath(rawURL string) string {
	kind := classifyHistoricalPayload(rawURL)
	scope := classifyHistoricalPayloadScope(rawURL)
	return filepath.Join(c.dir, kind, scope+".json")
}

func (c *historicalPayloadCache) entryKey(rawURL string) string {
	sum := sha1.Sum([]byte(rawURL))
	return hex.EncodeToString(sum[:])
}

func classifyHistoricalPayload(rawURL string) string {
	switch {
	case strings.Contains(rawURL, "/fapi/v1/fundingRate"):
		return "funding"
	case strings.Contains(rawURL, "/fapi/v1/premiumIndexKlines"):
		return "premium_klines"
	case strings.Contains(rawURL, "/fapi/v1/indexPriceKlines"):
		return "index_klines"
	case strings.Contains(rawURL, "/fapi/v1/klines"):
		return "futures_klines"
	case strings.Contains(rawURL, "/api/v3/klines"):
		return "spot_klines"
	default:
		return "historical"
	}
}

func classifyHistoricalPayloadScope(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fallbackHistoricalScope(rawURL)
	}
	query := parsed.Query()
	scope := strings.TrimSpace(query.Get("symbol"))
	if scope == "" {
		scope = strings.TrimSpace(query.Get("pair"))
	}
	if scope == "" {
		scope = "global_" + shortHistoricalHash(rawURL)
	}
	return sanitizeHistoricalScope(scope)
}

func fallbackHistoricalScope(rawURL string) string {
	return "global_" + shortHistoricalHash(rawURL)
}

func shortHistoricalHash(rawURL string) string {
	sum := sha1.Sum([]byte(rawURL))
	return hex.EncodeToString(sum[:8])
}

func sanitizeHistoricalScope(scope string) string {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		return "global"
	}
	var builder strings.Builder
	for _, r := range scope {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			builder.WriteRune(r)
			continue
		}
		builder.WriteByte('_')
	}
	result := strings.Trim(builder.String(), "_")
	if result == "" {
		return "global"
	}
	return result
}
