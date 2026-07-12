package exclusionmap

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"lingxi/internal/datatruth"
	historydata "lingxi/internal/datatruth/history"
	g10announcements "lingxi/internal/routerg10/announcements"
	g10constitution "lingxi/internal/routerg10/constitution"
	g10protocolv2 "lingxi/internal/routerg10/protocolv2"
	"lingxi/internal/routerg10/residualmap"
	"lingxi/internal/routerg6/reverseevent"
)

// LoadMapBundles reads verified G6 event-map output directories.
func LoadMapBundles(dirs []string) ([]reverseevent.MapBundle, map[string]string, map[string]string, error) {
	bundles := make([]reverseevent.MapBundle, 0, len(dirs))
	manifestHashes := map[string]string{}
	artifactHashes := map[string]string{}
	for _, dir := range dirs {
		bundle, err := reverseevent.LoadMapBundle(dir)
		if err != nil {
			return nil, nil, nil, err
		}
		bundles = append(bundles, bundle)
		manifestHashes[bundle.Manifest.ProfileID] = bundle.Manifest.ManifestHash
		for name, hash := range bundle.Manifest.ArtifactHashes {
			artifactHashes[bundle.Manifest.ProfileID+"/"+name] = hash
		}
	}
	return bundles, manifestHashes, artifactHashes, nil
}

// LoadAnnouncementEnvelope reads and verifies G10 announcement outputs.
func LoadAnnouncementEnvelope(dir string) (AnnouncementEnvelope, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return AnnouncementEnvelope{}, err
	}
	manifestPath := filepath.Join(abs, g10announcements.OutputManifestFile)
	manifest, err := g10announcements.LoadManifest(manifestPath)
	if err != nil {
		return AnnouncementEnvelope{}, fmt.Errorf("announcements manifest: %w", err)
	}
	if manifest.ProtocolHash != g10protocolv2.G10RouterProtocolHash {
		return AnnouncementEnvelope{}, errors.New("announcement manifest protocol mismatch")
	}
	if err := g10announcements.VerifyOutputArtifacts(abs, manifest); err != nil {
		return AnnouncementEnvelope{}, err
	}
	tapePath := filepath.Join(abs, g10announcements.OutputAnnouncementsFile)
	raw, err := os.ReadFile(tapePath)
	if err != nil {
		return AnnouncementEnvelope{}, err
	}
	var items []g10announcements.Announcement
	lines := splitJSONL(raw)
	for _, line := range lines {
		if len(line) == 0 {
			continue
		}
		var a g10announcements.Announcement
		if err := json.Unmarshal(line, &a); err != nil {
			return AnnouncementEnvelope{}, err
		}
		items = append(items, a)
	}
	gotTapeHash, err := g10announcements.HashTape(items)
	if err != nil {
		return AnnouncementEnvelope{}, err
	}
	if gotTapeHash != manifest.TapeHash {
		return AnnouncementEnvelope{}, errors.New("announcement tape hash mismatch")
	}
	env := AnnouncementEnvelope{
		TapeHash:     manifest.TapeHash,
		ManifestHash: manifest.ManifestHash,
		Incomplete:   manifest.Incomplete,
	}
	for _, a := range items {
		env.Announcements = append(env.Announcements, announcementRecord{
			ID:          a.ID,
			Symbol:      a.Symbol,
			Kind:        string(a.Kind),
			PublishedAt: a.PublishedAt,
			FirstSeenAt: a.FirstSeenAt,
			EffectiveAt: a.EffectiveAt,
			SourceHash:  a.SourceHash,
		})
	}
	return env, nil
}

// PITItems exports PIT announcements for downstream evaluation packages.
func (e AnnouncementEnvelope) PITItems() []g10announcements.Announcement {
	out := make([]g10announcements.Announcement, 0, len(e.Announcements))
	for _, rec := range e.Announcements {
		out = append(out, g10announcements.Announcement{
			ID: rec.ID, Symbol: rec.Symbol, Kind: g10announcements.Kind(rec.Kind),
			PublishedAt: rec.PublishedAt, FirstSeenAt: rec.FirstSeenAt,
			EffectiveAt: rec.EffectiveAt, SourceHash: rec.SourceHash,
		})
	}
	return out
}

func splitJSONL(raw []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i := 0; i < len(raw); i++ {
		if raw[i] == '\n' {
			line := trimSpace(raw[start:i])
			if len(line) > 0 {
				lines = append(lines, line)
			}
			start = i + 1
		}
	}
	if start < len(raw) {
		line := trimSpace(raw[start:])
		if len(line) > 0 {
			lines = append(lines, line)
		}
	}
	return lines
}

func trimSpace(b []byte) []byte {
	for len(b) > 0 && (b[0] == ' ' || b[0] == '\t' || b[0] == '\r') {
		b = b[1:]
	}
	for len(b) > 0 && (b[len(b)-1] == ' ' || b[len(b)-1] == '\t' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}

// ConstitutionHash seals the frozen personal constitution snapshot.
func ConstitutionHash() (string, error) {
	return g10constitution.Hash()
}

// CollectWorkItems enumerates triggered events from frozen G6 maps.
func CollectWorkItems(
	bundles []reverseevent.MapBundle,
	catalog historydata.HistoryCatalog,
	historyRoot string,
	maxFiles, maxLines int,
) ([]EventWorkItem, error) {
	items, err := residualmap.CollectTriggeredEventWorkItems(bundles, catalog, historyRoot, maxFiles, maxLines)
	if err != nil {
		return nil, err
	}
	out := make([]EventWorkItem, len(items))
	for i, item := range items {
		out[i] = EventWorkItem{
			ProfileID:    item.ProfileID,
			ManifestHash: item.ManifestHash,
			ShockCluster: item.ShockCluster,
			Event:        item.Event,
		}
	}
	return out, nil
}

// ConfigFingerprint hashes run configuration for checkpoint binding.
func ConfigFingerprint(cfg RunConfig) (string, error) {
	hash, _, err := datatruth.HashJSON(cfg)
	return hash, err
}

func (c RunConfig) Validate() error {
	if c.HistoryRoot == "" || c.OutputDir == "" {
		return errors.New("exclusionmap: history_root and output_dir are required")
	}
	if len(c.MapDirs) == 0 {
		return errors.New("exclusionmap: at least one map output directory is required")
	}
	if c.AnnouncementDir == "" {
		return errors.New("exclusionmap: announcement_dir is required")
	}
	if c.MaxFiles <= 0 || c.MaxLines <= 0 {
		return errors.New("exclusionmap: max_files and max_lines must be positive")
	}
	if c.CandidateLimit <= 0 {
		return errors.New("exclusionmap: candidate_limit must be positive")
	}
	if c.ProgressEvery <= 0 {
		return errors.New("exclusionmap: progress_every must be positive")
	}
	return nil
}
