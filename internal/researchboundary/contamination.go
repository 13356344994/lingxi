// Package researchboundary records which historical data may be used for research
// and which commitments were sealed before any researcher could inspect them.
package researchboundary

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type ExposureStatus string

const (
	StatusResearchOnly ExposureStatus = "RESEARCH_ONLY"
	StatusSealed       ExposureStatus = "SEALED"
	StatusUnproven     ExposureStatus = "UNPROVEN"
)

type UsageEvidence struct {
	Family    string `json:"family"`
	Source    string `json:"source"`
	Reason    string `json:"reason"`
	BytesRead int64  `json:"bytes_read"`
}

type SealedCommitment struct {
	ID            string    `json:"id"`
	DataHash      string    `json:"data_hash"`
	ChallengeHash string    `json:"challenge_hash"`
	ProtocolHash  string    `json:"protocol_hash"`
	CommittedAt   time.Time `json:"committed_at"`
}

type FamilyExposure struct {
	Family   string          `json:"family"`
	Status   ExposureStatus  `json:"status"`
	Evidence []UsageEvidence `json:"evidence,omitempty"`
	Reason   string          `json:"reason"`
}

type Ledger struct {
	SchemaVersion     string             `json:"schema_version"`
	GeneratedAt       time.Time          `json:"generated_at"`
	CatalogDataHash   string             `json:"catalog_data_hash"`
	CatalogSchemaHash string             `json:"catalog_schema_hash"`
	Families          []FamilyExposure   `json:"families"`
	Commitments       []SealedCommitment `json:"sealed_commitments,omitempty"`
}

type ScanConfig struct {
	Roots           []string
	Families        []string
	MaxFiles        int
	MaxBytesPerFile int64
}

func ScanUsage(cfg ScanConfig) ([]UsageEvidence, error) {
	if cfg.MaxBytesPerFile <= 0 {
		cfg.MaxBytesPerFile = 4 * 1024 * 1024
	}
	families := normalizedFamilies(cfg.Families)
	var files []string
	for _, root := range cfg.Roots {
		matches, err := filepath.Glob(root)
		if err != nil {
			return nil, err
		}
		files = append(files, matches...)
	}
	sort.Strings(files)
	if cfg.MaxFiles > 0 && len(files) > cfg.MaxFiles {
		files = files[:cfg.MaxFiles]
	}
	var evidence []UsageEvidence
	for _, path := range files {
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		if info.IsDir() {
			continue
		}
		found, bytesRead, err := scanFile(path, families, cfg.MaxBytesPerFile)
		if err != nil {
			return nil, err
		}
		for _, family := range found {
			evidence = append(evidence, UsageEvidence{
				Family: family, Source: path, Reason: "referenced_by_existing_research_artifact", BytesRead: bytesRead,
			})
		}
	}
	sort.Slice(evidence, func(i, j int) bool {
		if evidence[i].Family == evidence[j].Family {
			return evidence[i].Source < evidence[j].Source
		}
		return evidence[i].Family < evidence[j].Family
	})
	return evidence, nil
}

func BuildLedger(families []string, evidence []UsageEvidence, commitments []SealedCommitment, now time.Time) (Ledger, error) {
	if now.IsZero() {
		now = time.Now()
	}
	byFamily := map[string][]UsageEvidence{}
	for _, item := range evidence {
		family := strings.TrimSpace(item.Family)
		if family == "" || strings.TrimSpace(item.Source) == "" {
			return Ledger{}, fmt.Errorf("researchboundary: usage evidence requires family and source")
		}
		byFamily[family] = append(byFamily[family], item)
	}
	seenCommitment := map[string]bool{}
	for _, commitment := range commitments {
		if strings.TrimSpace(commitment.ID) == "" || strings.TrimSpace(commitment.DataHash) == "" ||
			strings.TrimSpace(commitment.ChallengeHash) == "" || strings.TrimSpace(commitment.ProtocolHash) == "" ||
			commitment.CommittedAt.IsZero() {
			return Ledger{}, fmt.Errorf("researchboundary: incomplete sealed commitment")
		}
		key := commitment.DataHash + "|" + commitment.ChallengeHash
		if seenCommitment[key] {
			return Ledger{}, fmt.Errorf("researchboundary: duplicate sealed commitment")
		}
		seenCommitment[key] = true
	}
	ledger := Ledger{SchemaVersion: "lingxi.history-contamination.v1", GeneratedAt: now.UTC()}
	for _, family := range normalizedFamilies(families) {
		items := append([]UsageEvidence(nil), byFamily[family]...)
		status := StatusResearchOnly
		reason := "no_pre_research_seal_proof_defaults_to_research_only"
		if len(items) > 0 {
			reason = "existing_research_artifact_references_family"
		}
		ledger.Families = append(ledger.Families, FamilyExposure{
			Family: family, Status: status, Evidence: items, Reason: reason,
		})
	}
	ledger.Commitments = append([]SealedCommitment(nil), commitments...)
	sort.Slice(ledger.Commitments, func(i, j int) bool { return ledger.Commitments[i].ID < ledger.Commitments[j].ID })
	return ledger, nil
}

func scanFile(path string, families []string, maxBytes int64) ([]string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer file.Close()
	reader := bufio.NewReader(io.LimitReader(file, maxBytes))
	seen := map[string]bool{}
	var total int64
	for {
		line, err := reader.ReadString('\n')
		total += int64(len(line))
		lower := strings.ToLower(line)
		for _, family := range families {
			if strings.Contains(lower, strings.ToLower(family)) {
				seen[family] = true
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, total, err
		}
	}
	out := make([]string, 0, len(seen))
	for family := range seen {
		out = append(out, family)
	}
	sort.Strings(out)
	return out, total, nil
}

func normalizedFamilies(families []string) []string {
	seen := map[string]bool{}
	for _, family := range families {
		if family = strings.TrimSpace(family); family != "" {
			seen[family] = true
		}
	}
	out := make([]string, 0, len(seen))
	for family := range seen {
		out = append(out, family)
	}
	sort.Strings(out)
	return out
}
