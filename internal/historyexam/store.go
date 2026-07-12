package historyexam

import (
	"bytes"
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
	"lingxi/internal/gauntlet"
)

// HiddenPaper is examiner/custodian-side ONLY. It binds a public commitment
// (ChallengeHash / DataHash / ProtocolHash) to the hidden material it commits
// to: the exact time range, the synthetic fixture bytes, the frozen stratum,
// and the correlated clusters. None of the hidden fields may ever be exposed to
// a researcher or written to a researcher-visible log.
type HiddenPaper struct {
	ExamID     string        `json:"exam_id"`
	Generation string        `json:"generation"`
	Queue      int           `json:"queue"`
	Kind       gauntlet.Kind `json:"kind"`
	Family     string        `json:"family"`
	AlphaSpend int           `json:"alpha_spend"`

	// Hidden fields. Never projected to the public commitment.
	Stratum     StratumInput `json:"stratum"`
	ClusterIDs  []string     `json:"cluster_ids"`
	Start       time.Time    `json:"start"`
	End         time.Time    `json:"end"`
	Fixture     []byte       `json:"fixture,omitempty"`
	FixturePath string       `json:"fixture_path,omitempty"`
	FixtureSize int64        `json:"fixture_size"`

	// Public commitments.
	ChallengeHash string `json:"challenge_hash"`
	ProtocolHash  string `json:"protocol_hash"`
	DataHash      string `json:"data_hash"`
}

// Commitment projects a hidden paper to the public Ticket. This is the ONLY
// projection researchers may see: it drops the time range, fixture bytes,
// stratum, and cluster identities, retaining just the commitments.
func (p HiddenPaper) Commitment() gauntlet.Ticket {
	return gauntlet.Ticket{
		ID:            p.ExamID,
		Queue:         p.Queue,
		Kind:          p.Kind,
		Family:        p.Family,
		AlphaSpend:    p.AlphaSpend,
		ChallengeHash: p.ChallengeHash,
		ProtocolHash:  p.ProtocolHash,
		DataHash:      p.DataHash,
	}
}

// HiddenManifest is the sealed exam schedule held only by the examiner side. It
// is persisted with 0600 permissions and is never handed to a researcher role.
type HiddenManifest struct {
	Generation string        `json:"generation"`
	Partition  Partition     `json:"partition"`
	Papers     []HiddenPaper `json:"papers"`
}

// PaperSpec describes one hidden paper to build from a cluster set. The caller
// provides synthetic fixture bytes; this package never reads real records.
type PaperSpec struct {
	ExamID      string
	Queue       int
	Kind        gauntlet.Kind
	Family      string
	AlphaSpend  int
	Stratum     StratumInput
	ClusterIDs  []string
	Start       time.Time
	End         time.Time
	Fixture     []byte
	FixturePath string
	// Nonce separates challenge commitments even when two papers share a
	// stratum and time range, guaranteeing globally unique commitments.
	Nonce string
}

// NewHiddenPaper computes the public commitments from the hidden material.
// DataHash binds the exact fixture bytes; ChallengeHash binds the question
// definition (protocol, stratum, clusters, range, nonce). Because both are
// derived from unique inputs, generations never collide.
func NewHiddenPaper(protocolHash string, spec PaperSpec) (HiddenPaper, error) {
	if strings.TrimSpace(protocolHash) == "" {
		return HiddenPaper{}, errors.New("protocol hash is required")
	}
	if strings.TrimSpace(spec.ExamID) == "" {
		return HiddenPaper{}, errors.New("exam id is required")
	}
	if spec.Queue <= 0 {
		return HiddenPaper{}, errors.New("queue must be positive")
	}
	if spec.Kind != gauntlet.KindStandard && spec.Kind != gauntlet.KindGolden {
		return HiddenPaper{}, errors.New("kind must be standard or golden")
	}
	if strings.TrimSpace(spec.Family) == "" {
		return HiddenPaper{}, errors.New("family is required")
	}
	if spec.AlphaSpend <= 0 {
		return HiddenPaper{}, errors.New("alpha spend must be positive")
	}
	if (len(spec.Fixture) == 0) == (strings.TrimSpace(spec.FixturePath) == "") {
		return HiddenPaper{}, errors.New("exactly one of fixture bytes or fixture path is required")
	}
	if spec.FixturePath != "" {
		absolute, err := filepath.Abs(spec.FixturePath)
		if err != nil {
			return HiddenPaper{}, err
		}
		spec.FixturePath = absolute
	}
	if spec.Start.IsZero() || spec.End.IsZero() || !spec.Start.Before(spec.End) {
		return HiddenPaper{}, errors.New("paper requires start strictly before end")
	}
	if len(spec.ClusterIDs) == 0 {
		return HiddenPaper{}, errors.New("paper requires at least one cluster id")
	}
	stratumKey, err := spec.Stratum.Key()
	if err != nil {
		return HiddenPaper{}, err
	}
	clusters := append([]string(nil), spec.ClusterIDs...)
	sort.Strings(clusters)

	dataHash, fixtureSize, err := hashPaperSpec(spec)
	if err != nil {
		return HiddenPaper{}, err
	}
	challengeHash := datatruth.HashStrings(
		"protocol="+protocolHash,
		"stratum="+stratumKey,
		"clusters="+strings.Join(clusters, ","),
		"start="+spec.Start.UTC().Format(time.RFC3339Nano),
		"end="+spec.End.UTC().Format(time.RFC3339Nano),
		"nonce="+strings.TrimSpace(spec.Nonce),
	)
	return HiddenPaper{
		ExamID:        strings.TrimSpace(spec.ExamID),
		Queue:         spec.Queue,
		Kind:          spec.Kind,
		Family:        strings.TrimSpace(spec.Family),
		AlphaSpend:    spec.AlphaSpend,
		Stratum:       spec.Stratum,
		ClusterIDs:    clusters,
		Start:         spec.Start.UTC(),
		End:           spec.End.UTC(),
		Fixture:       append([]byte(nil), spec.Fixture...),
		FixturePath:   strings.TrimSpace(spec.FixturePath),
		FixtureSize:   fixtureSize,
		ChallengeHash: challengeHash,
		ProtocolHash:  protocolHash,
		DataHash:      dataHash,
	}, nil
}

// HashData is the canonical fixture digest used for DataHash and for the
// grader's data-integrity re-check.
func HashData(fixture []byte) string {
	sum := sha256.Sum256(fixture)
	return hex.EncodeToString(sum[:])
}

func hashPaperSpec(spec PaperSpec) (string, int64, error) {
	if len(spec.Fixture) > 0 {
		return HashData(spec.Fixture), int64(len(spec.Fixture)), nil
	}
	info, err := os.Stat(spec.FixturePath)
	if err != nil {
		return "", 0, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return "", 0, fmt.Errorf("hidden fixture must be a regular 0600 file")
	}
	file, err := os.Open(spec.FixturePath)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}

func verifyPaperData(p HiddenPaper) error {
	var spec PaperSpec
	spec.Fixture = p.Fixture
	spec.FixturePath = p.FixturePath
	hash, size, err := hashPaperSpec(spec)
	if err != nil {
		return err
	}
	if hash != p.DataHash || size != p.FixtureSize {
		return ErrFixtureCorrupt
	}
	return nil
}

func openPaperData(p HiddenPaper) (io.ReadCloser, error) {
	if len(p.Fixture) > 0 {
		return io.NopCloser(bytes.NewReader(p.Fixture)), nil
	}
	return os.Open(p.FixturePath)
}

// Commitments returns the public Ticket view of the whole manifest, sorted by
// queue. This is the only manifest projection a researcher-facing surface may
// emit; it carries no hidden ranges, paths, strata, or clusters.
func (m HiddenManifest) Commitments() []gauntlet.Ticket {
	out := make([]gauntlet.Ticket, 0, len(m.Papers))
	for _, p := range m.Papers {
		out = append(out, p.Commitment())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Queue < out[j].Queue })
	return out
}

func CommitmentManifestHash(tickets []gauntlet.Ticket) (string, error) {
	ordered := append([]gauntlet.Ticket(nil), tickets...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Queue == ordered[j].Queue {
			return ordered[i].ID < ordered[j].ID
		}
		return ordered[i].Queue < ordered[j].Queue
	})
	hash, _, err := datatruth.HashJSON(struct {
		SchemaVersion string            `json:"schema_version"`
		Tickets       []gauntlet.Ticket `json:"tickets"`
	}{
		SchemaVersion: "lingxi.historyexam.commitments.v1",
		Tickets:       ordered,
	})
	return hash, err
}

// CreateGeneration is the compliant history-exam creation path. Callers cannot
// inject an unrelated manifest hash; it is derived from the exact ordered ticket
// commitments before delegating to the generic gauntlet state machine.
func CreateGeneration(
	state gauntlet.State,
	access gauntlet.AccessController,
	id string,
	tickets []gauntlet.Ticket,
	budgets map[string]int,
	now time.Time,
) (gauntlet.State, error) {
	manifestHash, err := CommitmentManifestHash(tickets)
	if err != nil {
		return gauntlet.State{}, err
	}
	return gauntlet.CreateGeneration(
		state, access, gauntlet.RoleCustodian, id, tickets, budgets, manifestHash, now,
	)
}

// paperByExamID looks up the hidden paper for a running exam. It is unexported
// on purpose: there is no listing API for a researcher role.
func (m HiddenManifest) paperByExamID(examID string) (HiddenPaper, bool) {
	for _, p := range m.Papers {
		if p.ExamID == examID {
			return p, true
		}
	}
	return HiddenPaper{}, false
}

// Validate checks manifest internal consistency: it must be an exam manifest,
// carry papers, keep exam ids and commitments unique, and end with exactly one
// golden paper occupying the final queue.
func (m HiddenManifest) Validate() error {
	if strings.TrimSpace(m.Generation) == "" {
		return errors.New("hidden manifest requires a generation id")
	}
	if m.Partition != PartitionExam {
		return fmt.Errorf("hidden manifest must seal the exam partition, got %q", m.Partition)
	}
	if len(m.Papers) < 2 {
		return errors.New("a sealed exam needs at least one standard and one golden paper")
	}
	seenExam := make(map[string]bool)
	seenChallenge := make(map[string]bool)
	seenData := make(map[string]bool)
	golden := 0
	maxQueue := 0
	goldenQueue := 0
	for _, p := range m.Papers {
		if seenExam[p.ExamID] {
			return fmt.Errorf("duplicate exam id %q", p.ExamID)
		}
		if seenChallenge[p.ChallengeHash] {
			return fmt.Errorf("duplicate challenge hash for exam %q", p.ExamID)
		}
		if seenData[p.DataHash] {
			return fmt.Errorf("duplicate data hash for exam %q", p.ExamID)
		}
		if err := verifyPaperData(p); err != nil {
			return fmt.Errorf("data hash mismatch for exam %q", p.ExamID)
		}
		seenExam[p.ExamID] = true
		seenChallenge[p.ChallengeHash] = true
		seenData[p.DataHash] = true
		if p.Queue > maxQueue {
			maxQueue = p.Queue
		}
		if p.Kind == gauntlet.KindGolden {
			golden++
			goldenQueue = p.Queue
		}
	}
	if golden != 1 {
		return errors.New("exactly one golden paper is required")
	}
	if goldenQueue != maxQueue {
		return errors.New("golden paper must occupy the final queue")
	}
	return nil
}

// SaveHiddenManifest writes the manifest to a directory tree owned by the
// examiner side with 0700 dirs and a 0600 file. The manifest holds hidden
// ranges/paths, so the strict permissions are part of the boundary.
func SaveHiddenManifest(path string, m HiddenManifest) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("explicit hidden manifest path is required")
	}
	if err := m.Validate(); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	// Enforce 0600 even if a restrictive umask was not applied to the temp file.
	if err := os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

// LoadHiddenManifest reads an examiner-side manifest. It refuses to read a
// manifest whose permissions are looser than 0600, so a leaked, world-readable
// manifest fails closed rather than silently feeding the grader.
func LoadHiddenManifest(path string) (HiddenManifest, error) {
	if strings.TrimSpace(path) == "" {
		return HiddenManifest{}, errors.New("explicit hidden manifest path is required")
	}
	info, err := os.Stat(path)
	if err != nil {
		return HiddenManifest{}, err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return HiddenManifest{}, fmt.Errorf("hidden manifest %s must be 0600; refusing to read group/other-accessible file", path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return HiddenManifest{}, err
	}
	var m HiddenManifest
	if err := json.Unmarshal(b, &m); err != nil {
		return HiddenManifest{}, err
	}
	if err := m.Validate(); err != nil {
		return HiddenManifest{}, err
	}
	return m, nil
}
