// okx-funding-archive-download retrieves only the approved public OKX module=3
// daily funding archive and never accesses trades, books, L2, or private APIs.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"lingxi/internal/datadrivenv3"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "okx-funding-archive-download:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("okx-funding-archive-download", flag.ContinueOnError)
	root := flags.String("root", ".", "repository root")
	connect := flags.Bool("connect", false, "perform approved GET-only download")
	if err := flags.Parse(args); err != nil {
		return err
	}
	abs, err := filepath.Abs(*root)
	if err != nil {
		return err
	}
	if !*connect {
		fmt.Fprintf(os.Stdout, "{\"offline\":true,\"module\":\"3\",\"date_range\":\"2023-12-31..2026-07-09\",\"output\":%q}\n", filepath.Join(abs, datadrivenv3.FundingArchiveDirectory))
		return nil
	}
	runtime.GOMAXPROCS(1)
	start, _ := time.Parse("2006-01-02", "2023-12-31")
	end, _ := time.Parse("2006-01-02", "2026-07-09")
	_, err = datadrivenv3.DownloadFundingArchive(context.Background(), datadrivenv3.FundingArchiveConfig{
		Root: abs, Start: start, End: end, Progress: os.Stderr,
	})
	return err
}
