// firstbattle-gauntlet is an offline commitment-only test harness. It does
// not read records, exchange data, or production configuration.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"lingxi/internal/exam"
	"lingxi/internal/gauntlet"
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
	case "init-virtual":
		return initVirtual(args[1:])
	case "unseal-next":
		return unsealNext(args[1:])
	case "grade":
		return grade(args[1:])
	case "crash":
		return crash(args[1:])
	case "result":
		return result(args[1:])
	default:
		return usage()
	}
}

func initVirtual(args []string) error {
	fs := flag.NewFlagSet("init-virtual", flag.ContinueOnError)
	statePath := fs.String("state", "", "explicit gauntlet state path")
	generation := fs.String("generation", "", "new generation id")
	firstQueue := fs.Int("first-queue", 1, "next untouched virtual queue")
	family := fs.String("family", "default", "test family")
	alphaBudget := fs.Int("alpha-budget", 2, "family multiple-testing budget")
	manifest := fs.String("manifest-hash", "virtual-manifest", "virtual immutable manifest hash")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *firstQueue <= 0 || *alphaBudget < 2 {
		return errors.New("first-queue must be positive and alpha-budget must fund standard plus golden")
	}
	state, err := load(*statePath)
	if err != nil {
		return err
	}
	tickets := []gauntlet.Ticket{
		{ID: fmt.Sprintf("virtual-%d-standard", *firstQueue), Queue: *firstQueue, Kind: gauntlet.KindStandard, Family: *family, AlphaSpend: 1, ChallengeHash: fmt.Sprintf("virtual-challenge-%d", *firstQueue), ProtocolHash: "virtual-protocol", DataHash: fmt.Sprintf("virtual-data-%d", *firstQueue)},
		{ID: fmt.Sprintf("virtual-%d-golden", *firstQueue+1), Queue: *firstQueue + 1, Kind: gauntlet.KindGolden, Family: *family, AlphaSpend: 1, ChallengeHash: fmt.Sprintf("virtual-challenge-%d", *firstQueue+1), ProtocolHash: "virtual-protocol", DataHash: fmt.Sprintf("virtual-data-%d", *firstQueue+1)},
	}
	state, err = gauntlet.CreateGeneration(state, gauntlet.FixedAccess{}, gauntlet.RoleCustodian, *generation, tickets, map[string]int{*family: *alphaBudget}, *manifest, time.Now().UTC())
	if err != nil {
		return err
	}
	return save(*statePath, state)
}

func unsealNext(args []string) error {
	fs := flag.NewFlagSet("unseal-next", flag.ContinueOnError)
	statePath := fs.String("state", "", "explicit gauntlet state path")
	generation := fs.String("generation", "", "generation id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	state, err := load(*statePath)
	if err != nil {
		return err
	}
	state, _, err = gauntlet.UnsealNext(state, gauntlet.FixedAccess{}, gauntlet.RoleExaminer, *generation, time.Now().UTC())
	if err != nil {
		return err
	}
	if err := save(*statePath, state); err != nil {
		return err
	}
	fmt.Println(`{"status":"unsealed_and_consumable"}`)
	return nil
}

func grade(args []string) error {
	fs := flag.NewFlagSet("grade", flag.ContinueOnError)
	statePath := fs.String("state", "", "explicit gauntlet state path")
	generation := fs.String("generation", "", "generation id")
	pass := fs.Bool("pass", false, "coarse pass verdict")
	reason := fs.String("reason", "", "PASS, ALPHA_BUDGET, FAMILY_MULTIPLE_TESTING, RISK_GATE, ROBUSTNESS_GATE, EXECUTION_GATE, or CRASH")
	keyEnv := fs.String("hmac-key-env", "LINGXI_GAUNTLET_TEST_HMAC_KEY", "test HMAC key environment variable")
	if err := fs.Parse(args); err != nil {
		return err
	}
	key := os.Getenv(*keyEnv)
	if len(key) < 32 {
		return fmt.Errorf("test HMAC key in %s must be at least 32 bytes", *keyEnv)
	}
	state, err := load(*statePath)
	if err != nil {
		return err
	}
	state, output, err := gauntlet.Grade(context.Background(), state, gauntlet.FixedAccess{}, gauntlet.RoleExaminer, *generation, *pass, *reason, time.Now().UTC(), exam.HMACSigner{KeyID: "virtual-test", Key: []byte(key)})
	if err != nil {
		return err
	}
	if err := save(*statePath, state); err != nil {
		return err
	}
	return printJSON(output)
}

func crash(args []string) error {
	fs := flag.NewFlagSet("crash", flag.ContinueOnError)
	statePath := fs.String("state", "", "explicit gauntlet state path")
	generation := fs.String("generation", "", "generation id")
	keyEnv := fs.String("hmac-key-env", "LINGXI_GAUNTLET_TEST_HMAC_KEY", "test HMAC key environment variable")
	if err := fs.Parse(args); err != nil {
		return err
	}
	key := os.Getenv(*keyEnv)
	if len(key) < 32 {
		return fmt.Errorf("test HMAC key in %s must be at least 32 bytes", *keyEnv)
	}
	state, err := load(*statePath)
	if err != nil {
		return err
	}
	state, output, err := gauntlet.Crash(context.Background(), state, gauntlet.FixedAccess{}, gauntlet.RoleExaminer, *generation, time.Now().UTC(), exam.HMACSigner{KeyID: "virtual-test", Key: []byte(key)})
	if err != nil {
		return err
	}
	if err := save(*statePath, state); err != nil {
		return err
	}
	return printJSON(output)
}

func result(args []string) error {
	fs := flag.NewFlagSet("result", flag.ContinueOnError)
	statePath := fs.String("state", "", "explicit gauntlet state path")
	generation := fs.String("generation", "", "generation id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	state, err := load(*statePath)
	if err != nil {
		return err
	}
	output, err := gauntlet.ResearchReceipt(state, gauntlet.FixedAccess{}, gauntlet.RoleResearcher, *generation)
	if err != nil {
		return err
	}
	return printJSON(output)
}

func load(path string) (gauntlet.State, error) {
	if strings.TrimSpace(path) == "" {
		return gauntlet.State{}, errors.New("explicit --state path is required")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return gauntlet.State{}, nil
		}
		return gauntlet.State{}, err
	}
	var state gauntlet.State
	return state, json.Unmarshal(b, &state)
}

func save(path string, state gauntlet.State) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("explicit --state path is required")
	}
	b, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func printJSON(value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	fmt.Println(string(b))
	return nil
}

func usage() error {
	return errors.New("usage: firstbattle-gauntlet init-virtual|unseal-next|grade|crash|result --state <isolated path>; virtual commitments only, no records or production config")
}
