package scorecard

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"lingxi/internal/exam"
)

const StatusNoGo = "NO_GO"

type SettlementMetrics struct {
	Rows      int     `json:"rows"`
	NetUSDT   float64 `json:"net_usdt"`
	LongRows  int     `json:"long_rows"`
	ShortRows int     `json:"short_rows"`
}

type MakerMetrics struct {
	Attempts   int `json:"attempts"`
	Filled     int `json:"filled"`
	FastFilled int `json:"fast_filled_le_1s"`
}

type ShadowMetrics struct {
	Rows             int `json:"rows"`
	AcceptedTheses   int `json:"accepted_theses"`
	ForceFlatOODRows int `json:"force_flat_ood_rows"`
}

type ExamMetrics struct {
	Total          int `json:"total"`
	ConsumedPassed int `json:"consumed_passed"`
	SignedPassed   int `json:"signed_passed"`
}

type Report struct {
	GeneratedAt time.Time         `json:"generated_at"`
	Status      string            `json:"status"`
	Reasons     []string          `json:"reasons"`
	Warnings    []string          `json:"warnings,omitempty"`
	Settlements SettlementMetrics `json:"settlements"`
	Maker       MakerMetrics      `json:"maker"`
	Shadow      ShadowMetrics     `json:"shadow"`
	Exams       ExamMetrics       `json:"exams"`
}

type Options struct {
	RecordDir string
	ExamState string
	MaxLines  int
	Progress  io.Writer
}

func Build(opts Options) (Report, error) {
	report := Report{GeneratedAt: time.Now().UTC(), Status: StatusNoGo}
	recordDir := strings.TrimSpace(opts.RecordDir)
	if recordDir == "" {
		recordDir = "records"
	}
	if err := scanJSONL(filepath.Join(recordDir, "okx_directional", "settlements.jsonl"), opts, func(raw []byte) error {
		var row struct {
			Side         string  `json:"side"`
			RealizedUSDT float64 `json:"realized_usdt"`
		}
		if err := json.Unmarshal(raw, &row); err != nil {
			return err
		}
		report.Settlements.Rows++
		report.Settlements.NetUSDT += row.RealizedUSDT
		switch row.Side {
		case "long":
			report.Settlements.LongRows++
		case "short":
			report.Settlements.ShortRows++
		}
		return nil
	}); err != nil && !os.IsNotExist(err) {
		return Report{}, err
	}
	if err := scanJSONL(filepath.Join(recordDir, "okx_directional", "fast_maker_attempts.jsonl"), opts, func(raw []byte) error {
		var row struct {
			Filled bool `json:"maker_filled"`
			WaitMS int  `json:"maker_wait_ms"`
		}
		if err := json.Unmarshal(raw, &row); err != nil {
			return err
		}
		report.Maker.Attempts++
		if row.Filled {
			report.Maker.Filled++
			if row.WaitMS <= 1000 {
				report.Maker.FastFilled++
			}
		}
		return nil
	}); err != nil && !os.IsNotExist(err) {
		return Report{}, err
	}
	if err := scanJSONL(filepath.Join(recordDir, "okx_directional", "firstbattle_mechanism_shadow.jsonl"), opts, func(raw []byte) error {
		var row struct {
			OOD struct {
				ForceFlat bool `json:"ForceFlat"`
			} `json:"ood"`
			Verdicts []struct {
				Accepted bool `json:"accepted"`
			} `json:"verdicts"`
		}
		if err := json.Unmarshal(raw, &row); err != nil {
			return err
		}
		report.Shadow.Rows++
		if row.OOD.ForceFlat {
			report.Shadow.ForceFlatOODRows++
		}
		for _, verdict := range row.Verdicts {
			if verdict.Accepted {
				report.Shadow.AcceptedTheses++
			}
		}
		return nil
	}); err != nil && !os.IsNotExist(err) {
		return Report{}, err
	}

	if strings.TrimSpace(opts.ExamState) != "" {
		registry, err := exam.LoadRegistry(opts.ExamState)
		if err != nil {
			return Report{}, err
		}
		report.Exams.Total = len(registry.Exams)
		for _, e := range registry.Exams {
			if e.Status == exam.StatusConsumedPassed {
				report.Exams.ConsumedPassed++
				if e.Receipt != nil && e.Receipt.Signed {
					report.Exams.SignedPassed++
				}
			}
		}
	}

	if report.Exams.SignedPassed == 0 {
		report.Reasons = append(report.Reasons, "no_signed_sealed_exam_pass")
	}
	if report.Shadow.Rows == 0 {
		report.Reasons = append(report.Reasons, "no_firstbattle_shadow_rows")
	} else if report.Shadow.AcceptedTheses == 0 {
		report.Reasons = append(report.Reasons, "no_accepted_mechanism_thesis")
	}
	if report.Settlements.NetUSDT <= 0 {
		report.Warnings = append(report.Warnings, "legacy_executable_net_not_positive")
	}
	if len(report.Reasons) == 0 {
		// 即使观测门全部通过，真钱仍需用户单独批准；scorecard 只给 READY_FOR_REVIEW。
		report.Status = "READY_FOR_REVIEW"
	}
	return report, nil
}

func scanJSONL(path string, opts Options, consume func([]byte) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		if opts.MaxLines > 0 && line > opts.MaxLines {
			break
		}
		if err := consume(scanner.Bytes()); err != nil {
			return fmt.Errorf("%s line %d: %w", path, line, err)
		}
		if opts.Progress != nil && line%100000 == 0 {
			fmt.Fprintf(opts.Progress, "%s processed=%d\n", filepath.Base(path), line)
		}
	}
	return scanner.Err()
}
