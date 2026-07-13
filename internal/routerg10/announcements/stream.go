package announcements

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

type archiveHook func(line ArchiveLine, stats *streamStats)

func streamArchiveJSONL(
	path string,
	maxLines, skipLines int,
	handle func(raw []byte) error,
	hook archiveHook,
) (streamStats, error) {
	var stats streamStats
	if path == "" {
		return stats, nil
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return stats, nil
		}
		return stats, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	lineNo := 0
	for sc.Scan() {
		stats.Lines++
		lineNo++
		if lineNo <= skipLines {
			continue
		}
		if maxLines > 0 && stats.Decoded+stats.Malformed >= maxLines {
			stats.Truncated = true
			break
		}
		raw := sc.Bytes()
		if len(raw) == 0 {
			stats.Skipped++
			continue
		}
		var preview ArchiveLine
		if err := json.Unmarshal(raw, &preview); err != nil {
			stats.Malformed++
			continue
		}
		if hook != nil {
			hook(preview, &stats)
		}
		if err := handle(raw); err != nil {
			stats.Malformed++
			continue
		}
		if !IsExplicitCoverageReceipt(preview) {
			stats.Decoded++
		}
	}
	if err := sc.Err(); err != nil {
		return stats, fmt.Errorf("announcements: read %s: %w", path, err)
	}
	return stats, nil
}
