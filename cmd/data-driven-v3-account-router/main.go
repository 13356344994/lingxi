// Command data-driven-v3-account-router performs the preregistered
// validation selection and one leaf_wf confirmation for Terra leaf-05.
// It refuses all partitions other than validation and leaf_wf.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"lingxi/internal/datadrivenv3/accountrouter"
)

func main() {
	root := flag.String("root", ".", "repository root")
	output := flag.String("output", "", "fresh atomic output directory")
	flag.Parse()
	if *output == "" {
		*output = filepath.Join(*root, "research/firstbattle/data_driven_router_v3/account_router", "run-"+time.Now().UTC().Format("20060102T150405Z"))
	}
	report, err := accountrouter.Run(accountrouter.Config{Root: *root, Progress: os.Stdout})
	if err != nil {
		fmt.Fprintln(os.Stderr, "account router failed:", err)
		os.Exit(1)
	}
	report, err = accountrouter.WriteAtomic(*output, report)
	if err != nil {
		fmt.Fprintln(os.Stderr, "account router output failed:", err)
		os.Exit(1)
	}
	fmt.Printf("stage=final verdict=%s selected_fraction=%v output=%s manifest_hash=%s\n", report.Verdict, report.SelectedFraction, *output, report.RouterCandidateManifestHash)
}
