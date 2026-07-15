package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"lingxi/internal/datadrivenv3/neutralrunner"
)

func main() {
	root := flag.String("root", ".", "repository root")
	limit := flag.Int("limit", 1000, "eligible discovery examples")
	output := flag.String("output", "", "atomic tournament output directory")
	production := flag.Bool("production", false, "publish the 100000-example discovery tournament under sealed")
	flag.Parse()
	if *output == "" {
		if *production {
			*output = filepath.Join(*root, "research/firstbattle/data_driven_router_v3/sealed/discovery-tournament-100000")
		} else {
			*output = filepath.Join(*root, "research/firstbattle/data_driven_router_v3/canary", fmt.Sprintf("tournament-%d", *limit))
		}
	}
	manifest, err := neutralrunner.RunTournament(context.Background(), neutralrunner.TournamentConfig{
		Root: *root, Output: *output, Limit: *limit, Production: *production, Progress: os.Stdout,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "tournament failed:", err)
		os.Exit(1)
	}
	raw, _ := json.Marshal(manifest)
	fmt.Printf("stage=final manifest=%s\n", raw)
}
