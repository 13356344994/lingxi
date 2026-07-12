package history

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"lingxi/internal/datatruth"
)

const (
	checkpointVersion = 1
	inputPrefixBytes  = 64 * 1024
)

type inputFile struct {
	Family     string    `json:"family"`
	AbsPath    string    `json:"abs_path"`
	RelPath    string    `json:"rel_path"`
	Size       int64     `json:"size"`
	ModTime    time.Time `json:"mod_time"`
	PrefixSize int64     `json:"prefix_size"`
	PrefixHash string    `json:"prefix_hash"`
}

type checkpointState struct {
	Version       int               `json:"version"`
	ConfigHash    string            `json:"config_hash"`
	RootDir       string            `json:"root_dir"`
	Files         []inputFile       `json:"files"`
	NextFileIndex int               `json:"next_file_index"`
	ByteOffset    int64             `json:"byte_offset"`
	LineOffset    int64             `json:"line_offset"`
	LineChain     string            `json:"line_chain"`
	CurrentFields []string          `json:"current_fields,omitempty"`
	CurrentMin    time.Time         `json:"current_min_time,omitempty"`
	CurrentMax    time.Time         `json:"current_max_time,omitempty"`
	TotalLines    int64             `json:"total_lines"`
	Completed     []FileStats       `json:"completed"`
	SeenFileHash  map[string]string `json:"seen_file_hash"`
	Done          bool              `json:"done"`
	Catalog       HistoryCatalog    `json:"catalog"`
	SavedAt       time.Time         `json:"saved_at"`
}

type doneMarker struct {
	SchemaVersion string    `json:"schema_version"`
	ConfigHash    string    `json:"config_hash"`
	DataHash      string    `json:"data_hash"`
	SchemaHash    string    `json:"schema_hash"`
	SavedAt       time.Time `json:"saved_at"`
}

func saveCheckpoint(path string, state checkpointState) error {
	state.SavedAt = time.Now().UTC()
	return saveJSONAtomic(path, state)
}

func loadCheckpoint(path string) (checkpointState, bool, error) {
	var state checkpointState
	if err := readJSON(path, &state); err != nil {
		if os.IsNotExist(err) {
			return checkpointState{}, false, nil
		}
		return checkpointState{}, false, err
	}
	return state, true, nil
}

func checkpointDonePath(checkpointPath string) string {
	return checkpointPath + ".done"
}

func saveDoneMarker(path string, catalog HistoryCatalog, configHash string) error {
	return saveJSONAtomic(path, doneMarker{
		SchemaVersion: CatalogSchemaVersion,
		ConfigHash:    configHash,
		DataHash:      catalog.DataHash,
		SchemaHash:    catalog.SchemaHash,
		SavedAt:       time.Now().UTC(),
	})
}

func verifyDoneMarker(path string, catalog HistoryCatalog, configHash string) error {
	var marker doneMarker
	if err := readJSON(path, &marker); err != nil {
		return err
	}
	switch {
	case marker.SchemaVersion != CatalogSchemaVersion:
		return fmt.Errorf("history: done marker schema mismatch: %q", marker.SchemaVersion)
	case marker.ConfigHash != configHash:
		return errors.New("history: done marker config hash mismatch")
	case marker.DataHash != catalog.DataHash:
		return errors.New("history: done marker data hash mismatch")
	case marker.SchemaHash != catalog.SchemaHash:
		return errors.New("history: done marker schema hash mismatch")
	default:
		return nil
	}
}

func readJSON(path string, out any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

func saveJSONAtomic(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func discoverInputFiles(rootDir string, families []string, maxFiles int) ([]inputFile, []string, error) {
	familySet := map[string]struct{}{}
	if len(families) > 0 {
		for _, family := range families {
			family = strings.TrimSpace(family)
			if family == "" {
				continue
			}
			familySet[family] = struct{}{}
		}
	}
	entries, err := os.ReadDir(rootDir)
	if err != nil {
		return nil, nil, err
	}
	availableFamilies := make([]string, 0, len(entries))
	presentFamilies := map[string]struct{}{}
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
		presentFamilies[name] = struct{}{}
	}
	if len(familySet) > 0 {
		for family := range familySet {
			if _, ok := presentFamilies[family]; !ok {
				return nil, nil, fmt.Errorf("history: family not found: %s", family)
			}
		}
	}
	sort.Strings(availableFamilies)
	files := make([]inputFile, 0)
	seenRel := map[string]struct{}{}
	for _, family := range availableFamilies {
		familyDir := filepath.Join(rootDir, family)
		children, err := os.ReadDir(familyDir)
		if err != nil {
			return nil, nil, err
		}
		sort.Slice(children, func(i, j int) bool { return children[i].Name() < children[j].Name() })
		for _, child := range children {
			if child.IsDir() {
				continue
			}
			name := child.Name()
			if strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".done") {
				continue
			}
			absPath := filepath.Join(familyDir, name)
			relPath := filepath.Join(family, name)
			if _, err := os.Stat(absPath + ".done"); err != nil {
				if os.IsNotExist(err) {
					return nil, nil, fmt.Errorf("history: source file is not sealed by downloader .done marker: %s", relPath)
				}
				return nil, nil, err
			}
			if _, exists := seenRel[relPath]; exists {
				return nil, nil, fmt.Errorf("history: duplicate file path %s", relPath)
			}
			seenRel[relPath] = struct{}{}
			file, err := buildInputFile(family, absPath, relPath)
			if err != nil {
				return nil, nil, err
			}
			files = append(files, file)
			if maxFiles > 0 && len(files) >= maxFiles {
				return files, availableFamilies, nil
			}
		}
	}
	if len(files) == 0 {
		return nil, nil, errors.New("history: no files found under selected families")
	}
	return files, availableFamilies, nil
}

func buildInputFile(family, absPath, relPath string) (inputFile, error) {
	info, err := os.Stat(absPath)
	if err != nil {
		return inputFile{}, err
	}
	if info.IsDir() {
		return inputFile{}, fmt.Errorf("history: expected regular file, got directory %s", absPath)
	}
	prefixSize := info.Size()
	if prefixSize > inputPrefixBytes {
		prefixSize = inputPrefixBytes
	}
	prefixHash, err := hashPrefix(absPath, prefixSize)
	if err != nil {
		return inputFile{}, err
	}
	return inputFile{
		Family:     family,
		AbsPath:    absPath,
		RelPath:    relPath,
		Size:       info.Size(),
		ModTime:    info.ModTime().UTC(),
		PrefixSize: prefixSize,
		PrefixHash: prefixHash,
	}, nil
}

func hashPrefix(path string, bytesToHash int64) (string, error) {
	if bytesToHash < 0 {
		return "", errors.New("history: invalid prefix size")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if bytesToHash > 0 {
		if _, err := io.CopyN(hash, file, bytesToHash); err != nil && err != io.EOF {
			return "", err
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func hashFile(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	hash := sha256.New()
	var total int64
	buf := make([]byte, 4*1024*1024)
	for {
		n, readErr := file.Read(buf)
		if n > 0 {
			if _, err := hash.Write(buf[:n]); err != nil {
				return "", 0, err
			}
			total += int64(n)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return "", 0, readErr
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), total, nil
}

func hashLineChain(path string, byteOffset int64) (string, error) {
	if byteOffset == 0 {
		return "", nil
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	reader := bufio.NewReaderSize(file, 256*1024)
	var consumed int64
	chain := ""
	for consumed < byteOffset {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return "", fmt.Errorf("history: cannot verify line chain at %d: %w", consumed, err)
		}
		consumed += int64(len(line))
		if consumed > byteOffset {
			return "", errors.New("history: checkpoint byte offset is not at line boundary")
		}
		chain = extendChain(chain, line)
	}
	return chain, nil
}

func extendChain(previous string, line []byte) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte(previous))
	_, _ = hash.Write(line)
	return hex.EncodeToString(hash.Sum(nil))
}

func fingerprintConfig(rootDir string, families []string, maxFiles int) (string, error) {
	cpFamilies := append([]string(nil), families...)
	sort.Strings(cpFamilies)
	payload := struct {
		SchemaVersion string   `json:"schema_version"`
		RootDir       string   `json:"root_dir"`
		Families      []string `json:"families"`
		MaxFiles      int      `json:"max_files"`
	}{
		SchemaVersion: CatalogSchemaVersion,
		RootDir:       rootDir,
		Families:      cpFamilies,
		MaxFiles:      maxFiles,
	}
	hash, _, err := datatruth.HashJSON(payload)
	return hash, err
}

func verifyInputBinding(state checkpointState, files []inputFile, configHash string) error {
	if state.Version != checkpointVersion {
		return fmt.Errorf("history: checkpoint version changed: got %d", state.Version)
	}
	if state.ConfigHash != configHash {
		return errors.New("history: input or config changed")
	}
	if len(state.Files) != len(files) {
		return errors.New("history: selected files changed")
	}
	for i := range files {
		before := state.Files[i]
		now := files[i]
		if before.RelPath != now.RelPath || before.AbsPath != now.AbsPath || before.Family != now.Family {
			return fmt.Errorf("history: file ordering changed at index %d", i)
		}
		if now.Size < before.Size {
			return fmt.Errorf("history: file truncated: %s", now.RelPath)
		}
		if now.Size != before.Size {
			return fmt.Errorf("history: file size changed: %s", now.RelPath)
		}
		if before.PrefixHash != now.PrefixHash || before.PrefixSize != now.PrefixSize {
			return fmt.Errorf("history: file prefix changed: %s", now.RelPath)
		}
	}
	if state.NextFileIndex < 0 || state.NextFileIndex > len(files) {
		return errors.New("history: invalid checkpoint file index")
	}
	if state.NextFileIndex < len(files) {
		current := files[state.NextFileIndex]
		if state.ByteOffset > current.Size {
			return fmt.Errorf("history: checkpoint offset beyond file size: %s", current.RelPath)
		}
		lineChain, err := hashLineChain(current.AbsPath, state.ByteOffset)
		if err != nil {
			return err
		}
		if lineChain != state.LineChain {
			return fmt.Errorf("history: consumed content changed: %s", current.RelPath)
		}
	}
	return nil
}
