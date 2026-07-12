package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"lingxi/internal/routerdiscovery/backward"
)

// eventFile is one discovered fixture input file, scoped to a single
// research-only family directory.
type eventFile struct {
	Family  string
	AbsPath string
	RelPath string
	Size    int64
	ModTime time.Time
}

// discoverEventFiles walks rootDir/<family>/<file> looking for fixture event
// files. It is intentionally bounded by maxFiles (which the caller has
// already validated is positive) and never recurses beyond one family level
// deep, and never consults anything resembling a real records/ history
// pipeline: this command only ever reads whatever small, explicit
// --input-dir the caller points it at.
func discoverEventFiles(rootDir string, families []string, maxFiles int) ([]eventFile, error) {
	familySet := map[string]struct{}{}
	for _, f := range families {
		f = strings.TrimSpace(f)
		if f != "" {
			familySet[f] = struct{}{}
		}
	}
	entries, err := os.ReadDir(rootDir)
	if err != nil {
		return nil, err
	}
	var availableFamilies []string
	present := map[string]struct{}{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := strings.TrimSpace(entry.Name())
		if name == "" || strings.HasPrefix(name, ".") {
			continue
		}
		if len(familySet) > 0 {
			if _, ok := familySet[name]; !ok {
				continue
			}
		}
		availableFamilies = append(availableFamilies, name)
		present[name] = struct{}{}
	}
	if len(familySet) > 0 {
		for family := range familySet {
			if _, ok := present[family]; !ok {
				return nil, fmt.Errorf("router-discovery: family not found: %s", family)
			}
		}
	}
	sort.Strings(availableFamilies)

	var files []eventFile
	for _, family := range availableFamilies {
		dir := filepath.Join(rootDir, family)
		children, err := os.ReadDir(dir)
		if err != nil {
			return nil, err
		}
		sort.Slice(children, func(i, j int) bool { return children[i].Name() < children[j].Name() })
		for _, child := range children {
			if child.IsDir() || strings.HasPrefix(child.Name(), ".") {
				continue
			}
			info, err := child.Info()
			if err != nil {
				return nil, err
			}
			absPath := filepath.Join(dir, child.Name())
			files = append(files, eventFile{
				Family:  family,
				AbsPath: absPath,
				RelPath: filepath.Join(family, child.Name()),
				Size:    info.Size(),
				ModTime: info.ModTime().UTC(),
			})
			if len(files) >= maxFiles {
				return files, nil
			}
		}
	}
	if len(files) == 0 {
		return nil, errors.New("router-discovery: no event files found under selected families")
	}
	return files, nil
}

type scanConfig struct {
	Files         []eventFile
	MaxLines      int
	ProgressEvery int
	Progress      io.Writer
}

type scanStats struct {
	FilesTotal int
	FilesDone  int
	LinesRead  int
	Truncated  bool
}

// loadEvents reads backward.Event JSONL records from Files, in deterministic
// order, stopping once cfg.MaxLines total lines have been read across every
// file (cfg.MaxLines is validated positive by the caller — there is no
// "unlimited" path through this function).
func loadEvents(cfg scanConfig) ([]backward.Event, scanStats, error) {
	stats := scanStats{FilesTotal: len(cfg.Files)}
	var events []backward.Event
	lineTotal := 0
	for _, f := range cfg.Files {
		fh, err := os.Open(f.AbsPath)
		if err != nil {
			return nil, stats, err
		}
		scanner := bufio.NewScanner(fh)
		scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
		lineInFile := 0
		for scanner.Scan() {
			raw := strings.TrimSpace(scanner.Text())
			lineInFile++
			if raw == "" {
				continue
			}
			var e backward.Event
			if err := json.Unmarshal([]byte(raw), &e); err != nil {
				fh.Close()
				return nil, stats, fmt.Errorf("router-discovery: %s line %d: %w", f.RelPath, lineInFile, err)
			}
			events = append(events, e)
			lineTotal++
			stats.LinesRead = lineTotal
			if cfg.Progress != nil && cfg.ProgressEvery > 0 && lineTotal%cfg.ProgressEvery == 0 {
				fmt.Fprintf(cfg.Progress, "{\"stage\":\"scan\",\"file\":%q,\"files_done\":%d,\"files_total\":%d,\"lines\":%d}\n",
					f.RelPath, stats.FilesDone, stats.FilesTotal, lineTotal)
			}
			if cfg.MaxLines > 0 && lineTotal >= cfg.MaxLines {
				scanErr := scanner.Err()
				fh.Close()
				stats.Truncated = true
				return events, stats, scanErr
			}
		}
		scanErr := scanner.Err()
		fh.Close()
		if scanErr != nil {
			return nil, stats, scanErr
		}
		stats.FilesDone++
	}
	return events, stats, nil
}
