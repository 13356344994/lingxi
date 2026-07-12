// router-g5-exam-boundary verifies public G5 exam boundary state: WF/mechanism
// eligibility plus public multi-paper commitments. It never reads hidden
// manifests, fixture bytes, or runs seal/unseal/grade.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"lingxi/internal/routerg5/examboundary"
	"lingxi/internal/routerg5/protocol"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return usage()
	}
	switch args[0] {
	case "verify":
		return verify(args[1:])
	default:
		return usage()
	}
}

func verify(args []string) error {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	wfPath := fs.String("wf-report", "", "public g5_wf_report.json path")
	mechPath := fs.String("mechanism-report", "", "public g5_policy_mechanism_report.json path")
	commitmentsPath := fs.String("commitments", "", "public commitment JSON path")
	statePath := fs.String("state", "", "optional public gauntlet state path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	for _, path := range []string{*wfPath, *mechPath, *commitmentsPath, *statePath} {
		if strings.Contains(strings.ToLower(path), "hidden") {
			return examboundary.ErrHiddenManifestRead
		}
	}
	report, err := examboundary.VerifyBoundary(examboundary.VerifyBoundaryInput{
		WFReportPath:        *wfPath,
		MechanismReportPath: *mechPath,
		CommitmentsPath:     *commitmentsPath,
		StatePath:           *statePath,
	})
	if err != nil {
		return err
	}
	raw, err := json.Marshal(report)
	if err != nil {
		return err
	}
	fmt.Println(string(raw))
	if report.Status == protocol.ExamUnavailableStatus || report.Status == protocol.ExamBoundaryNoGoStatus {
		return nil
	}
	if !report.Available {
		return fmt.Errorf("boundary not available")
	}
	return nil
}

func usage() error {
	return fmt.Errorf("usage: router-g5-exam-boundary verify [--wf-report <wf.json>] [--mechanism-report <mech.json>] [--commitments <public.json>] [--state <gauntlet.json>]; never reads hidden manifests")
}
