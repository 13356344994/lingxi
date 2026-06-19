package app

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const crossVenueCycleLogFileName = "cycle_explains.jsonl"

func (e *CrossVenueExecutor) cycleLogPath() string {
	return filepath.Join(crossVenueRecordDir(e.recordDir), crossVenueCycleLogFileName)
}

func (e *CrossVenueExecutor) appendCycleExplain(explain CrossVenueCycleExplain) {
	if e == nil || strings.TrimSpace(explain.DecisionCycleID) == "" {
		return
	}
	data, err := json.Marshal(explain)
	if err != nil {
		return
	}
	path := e.cycleLogPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(data, '\n'))
}

func (e *CrossVenueExecutor) loadCycleExplains(since time.Time) ([]CrossVenueCycleExplain, error) {
	if e == nil {
		return nil, nil
	}
	f, err := os.Open(e.cycleLogPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	out := make([]CrossVenueCycleExplain, 0)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var rec CrossVenueCycleExplain
		if json.Unmarshal([]byte(line), &rec) != nil {
			continue
		}
		if !since.IsZero() && rec.DecisionTime.Before(since) {
			continue
		}
		out = append(out, rec)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
