package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"
)

// BackfillRecentFirstBattleShadow 从文件尾部按块读取最近 N 条旧 executable shadow，
// 不扫描整份 GB 级文件。它只用于验证新机制链和生成初始 NO-GO scorecard，不赋予考试资格。
func BackfillRecentFirstBattleShadow(recordDir, inputPath string, lines int, truncate bool, progress io.Writer) (int, error) {
	if lines <= 0 {
		return 0, nil
	}
	rawLines, err := readLastNonEmptyLines(inputPath, lines)
	if err != nil {
		return 0, err
	}
	outPath := filepath.Join(recordDir, "okx_directional", firstBattleShadowFile)
	if truncate {
		if err := os.Remove(outPath); err != nil && !os.IsNotExist(err) {
			return 0, err
		}
	}
	e := &OKXDirectionalEngine{
		venueName: "okx", recordDir: filepath.Join(recordDir, "okx_directional"),
		logger: log.New(io.Discard, "", 0), nowFn: time.Now,
	}
	written := 0
	for _, raw := range rawLines {
		var ev fastExecShadowEvent
		if err := json.Unmarshal(raw, &ev); err != nil {
			continue
		}
		if ev.Venue == "" {
			ev.Venue = "okx"
		}
		recordFirstBattleShadow(e, &ev)
		written++
		if progress != nil && written%100 == 0 {
			fmt.Fprintf(progress, "firstbattle backfill written=%d/%d\n", written, len(rawLines))
		}
	}
	return written, nil
}

func readLastNonEmptyLines(path string, limit int) ([][]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return nil, err
	}
	const blockSize int64 = 256 * 1024
	var data []byte
	offset := stat.Size()
	for offset > 0 {
		readSize := blockSize
		if offset < readSize {
			readSize = offset
		}
		offset -= readSize
		block := make([]byte, readSize)
		if _, err := f.ReadAt(block, offset); err != nil {
			return nil, err
		}
		data = append(block, data...)
		if bytes.Count(data, []byte{'\n'}) > limit && offset > 0 {
			break
		}
	}
	parts := bytes.Split(data, []byte{'\n'})
	out := make([][]byte, 0, limit)
	for i := len(parts) - 1; i >= 0 && len(out) < limit; i-- {
		line := bytes.TrimSpace(parts[i])
		if len(line) == 0 {
			continue
		}
		copyLine := append([]byte(nil), line...)
		out = append(out, copyLine)
	}
	for left, right := 0, len(out)-1; left < right; left, right = left+1, right-1 {
		out[left], out[right] = out[right], out[left]
	}
	return out, nil
}
