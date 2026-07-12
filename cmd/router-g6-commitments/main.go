// router-g6-commitments imports and verifies public commitment JSON and optional
// gauntlet state. It never reads hidden manifests or sealed exam material.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"lingxi/internal/routerg6/examboundary"
	"lingxi/internal/routerg6/protocol"
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
	commitmentsPath := fs.String("commitments", "", "public commitment JSON path")
	statePath := fs.String("state", "", "optional public gauntlet state path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.Contains(strings.ToLower(*commitmentsPath), "hidden") ||
		strings.Contains(strings.ToLower(*statePath), "hidden") {
		return examboundary.ErrHiddenManifestRead
	}
	report, err := examboundary.Verify(*commitmentsPath, *statePath)
	if err != nil {
		return err
	}
	b, err := json.Marshal(report)
	if err != nil {
		return err
	}
	fmt.Println(string(b))
	if report.Status == protocol.ExamUnavailableStatus {
		return nil
	}
	if !report.Available {
		return errors.New("commitments not available")
	}
	return nil
}

func usage() error {
	return errors.New("usage: router-g6-commitments verify [--commitments <public.json>] [--state <gauntlet.json>]; never reads hidden manifests")
}
