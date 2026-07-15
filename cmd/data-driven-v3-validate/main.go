// Command data-driven-v3-validate is the independent GPT-5.6 Sol validation
// owner for the sealed 100k discovery-tournament policies. It
// never refits, never reads discovery_label_bundle.jsonl.gz content, and
// never opens router_holdout. See internal/datadrivenv3/pathvalidation for
// the full replay/gate/verdict implementation.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"lingxi/internal/datadrivenv3/pathvalidation"
)

func main() {
	root := flag.String("root", ".", "repository root")
	output := flag.String("output", "", "atomic validation output directory")
	limit := flag.Int("limit", 0, "cap prepared rows per partition (0 = no limit)")
	flag.Parse()

	if *output == "" {
		stamp := time.Now().UTC().Format("20060102T150405Z")
		*output = filepath.Join(*root, "research/firstbattle/data_driven_router_v3/validation", "run-"+stamp)
	}

	report, err := pathvalidation.Run(context.Background(), pathvalidation.Config{
		Root:     *root,
		Progress: os.Stdout,
		Limit:    *limit,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "validation failed:", err)
		os.Exit(1)
	}
	if err := pathvalidation.WriteAtomicReport(*output, report); err != nil {
		fmt.Fprintln(os.Stderr, "atomic output failed:", err)
		os.Exit(1)
	}

	summary := struct {
		OutputDir            string `json:"output_dir"`
		SealedBundleHash     string `json:"sealed_bundle_byte_hash"`
		DatasetManifestHash  string `json:"dataset_manifest_hash"`
		RouterHoldoutBlinded bool   `json:"router_holdout_unblinded"`
		TradeAllowed         bool   `json:"trade_allowed"`
		ValidationFamily     int    `json:"validation_family_size"`
		LeafWFFamily         int    `json:"leaf_wf_family_size"`
		Paths                int    `json:"total_candidate_paths"`
		Validated            int    `json:"validated_path_count"`
		WFConfirmed          int    `json:"wf_confirmed_count"`
		InactivityFailures   int    `json:"inactivity_failure_count"`
	}{
		OutputDir: *output, SealedBundleHash: report.SealedBundleByteHash,
		DatasetManifestHash: report.DatasetManifestHash, RouterHoldoutBlinded: report.RouterHoldoutUnblinded,
		TradeAllowed: report.TradeAllowed, ValidationFamily: report.ValidationFamilySize, LeafWFFamily: report.LeafWFFamilySize,
	}
	for _, owner := range report.Owners {
		for _, p := range owner.Paths {
			summary.Paths++
			if p.Verdict == pathvalidation.VerdictValidatedPath {
				summary.Validated++
				if p.WFPassed {
					summary.WFConfirmed++
				}
			}
			if p.InactivityFailure {
				summary.InactivityFailures++
			}
		}
	}
	raw, _ := json.Marshal(summary)
	fmt.Printf("stage=final summary=%s\n", raw)
}
