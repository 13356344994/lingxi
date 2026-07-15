// binance-order-history-pilot is a one-shot, non-trading validation target.
// It is intentionally offline until the documented endpoint contract has been
// independently reviewed and recorded.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"lingxi/internal/historydownload/binancereadonly"
)

const (
	privateRoot = "/root/.lingxi-private/historydownload/binance-order-async"
	rootCap     = int64(2 << 30)
)

var (
	pilotStart = binancereadonly.PilotStart
	pilotEnd   = binancereadonly.PilotEnd
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "binance-order-history-pilot:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("binance-order-history-pilot", flag.ContinueOnError)
	connect := flags.Bool("connect", false, "reserved; network remains disabled pending independent review")
	secret := flags.String("secret", "", "existing private YAML containing top-level binance.api and binance.secret")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("arguments are not accepted")
	}
	if !pilotStart.Before(pilotEnd) || pilotEnd.Sub(pilotStart) != binancereadonly.MaxWindow {
		return fmt.Errorf("fixed pilot window integrity failure")
	}
	if *secret != "" {
		// Parse only to fail locally on cross-venue/flat credentials; values are
		// neither printed nor persisted.
		if _, _, err := binancereadonly.LoadCredentials(*secret); err != nil {
			return err
		}
	}
	if *connect {
		result, err := binancereadonly.RunOneShot(context.Background(), binancereadonly.OrchestratorConfig{
			Root: privateRoot, DownloadHostVerified: binancereadonly.OfficialDownloadHostVerified,
		})
		if err != nil {
			return err
		}
		fmt.Printf("{\"connected\":false,\"stop_status\":%q,\"quota_consumed\":false}\n", result.StopStatus)
		return nil
	}
	fmt.Printf("{\"offline\":true,\"endpoint\":%q,\"status_endpoint\":%q,\"root\":%q,\"root_cap_bytes\":%d,\"window_start\":%q,\"window_end\":%q,\"receipt_quota_side_effect\":%q}\n",
		binancereadonly.OrderAsyncEndpoint, binancereadonly.OrderAsyncStatusEndpoint, privateRoot, rootCap,
		pilotStart.Format(time.RFC3339), pilotEnd.Format(time.RFC3339), "ASYNC_ORDER_HISTORY_JOB_REQUESTED")
	return nil
}
