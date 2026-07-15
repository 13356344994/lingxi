// okx-report-center-inspect validates one local manual Report Center CSV.
// It never connects to OKX and never reads repository records or contracts.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"lingxi/internal/historydownload/reportcenter"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "okx-report-center-inspect:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("okx-report-center-inspect", flag.ContinueOnError)
	file := flags.String("file", "", "CSV basename directly inside private inbox")
	mapping := flags.String("mapping", "", "mapping JSON basename directly inside private mappings")
	cap := flags.Int64("root-cap", 4<<30, "private root cap in bytes (maximum 4 GiB)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *file == "" || filepath.Base(*file) != *file || *mapping == "" || filepath.Base(*mapping) != *mapping {
		return fmt.Errorf("--file and --mapping basenames are required")
	}
	receipt, err := reportcenter.Inspect(reportcenter.Config{
		Root: reportcenter.DefaultRoot, Inbox: reportcenter.DefaultInbox, CSVPath: filepath.Join(reportcenter.DefaultInbox, *file),
		MappingPath: filepath.Join(reportcenter.DefaultMappings, *mapping), RootCap: *cap,
	})
	// Receipts deliberately contain no rows, raw IDs, or field values.
	encoded, marshalErr := json.MarshalIndent(receipt, "", "  ")
	if marshalErr == nil {
		fmt.Println(string(encoded))
	}
	return err
}
