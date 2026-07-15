// p0-swap-data-fitness is an offline, outcome-blind projection measurer.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"lingxi/internal/historydownload"
	"lingxi/internal/historydownload/datafitness"
)

const measurementOutput = "research/firstbattle/p0_swap_data_fitness_measurement.json"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "p0-swap-data-fitness:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("p0-swap-data-fitness", flag.ContinueOnError)
	privateRoot := fs.String("private-root", historydownload.DefaultPrivateRawRoot, "private published historydownload root")
	publicRoot := fs.String("public-root", historydownload.DefaultPublicRawRoot, "public published historydownload root")
	output := fs.String("output", "", "optional fixed measurement output path; empty writes stdout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var target string
	if *output != "" {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		target, err = resolveMeasurementOutput(cwd, *output)
		if err != nil {
			return err
		}
	}
	result, err := datafitness.Measure(datafitness.Config{
		OrdersRoot:     filepath.Join(*privateRoot, "api", "v5", "trade", "orders-history-archive", "SWAP"),
		FillsRoot:      filepath.Join(*privateRoot, "api", "v5", "trade", "fills-history", "SWAP"),
		PublicDataRoot: *publicRoot,
	})
	if err != nil {
		return err
	}
	if *output == "" {
		return json.NewEncoder(os.Stdout).Encode(result)
	}
	return historydownload.WriteJSONAtomic(target, result, 0o644)
}

func resolveMeasurementOutput(cwd, requested string) (string, error) {
	anchor, err := repositoryRoot(cwd)
	if err != nil {
		return "", err
	}
	repo, _, err := historydownload.ResolveRepository(filepath.Join(anchor, "research", "firstbattle", "route_data_contracts.json"))
	if err != nil {
		return "", err
	}
	target := requested
	if !filepath.IsAbs(target) {
		target = filepath.Join(cwd, target)
	}
	target, err = canonicalPotential(target)
	if err != nil {
		return "", err
	}
	expected := filepath.Join(repo, filepath.FromSlash(measurementOutput))
	resolvedExpected, err := canonicalPotential(expected)
	if err != nil || resolvedExpected != expected || target != expected {
		return "", fmt.Errorf("--output must be exactly %s", measurementOutput)
	}
	return target, nil
}

func repositoryRoot(start string) (string, error) {
	current, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		goMod, readErr := os.ReadFile(filepath.Join(current, "go.mod"))
		_, gitErr := os.Stat(filepath.Join(current, ".git"))
		if readErr == nil && gitErr == nil && string(goMod) != "" && filepath.Base(current) != "" {
			if len(goMod) >= len("module lingxi\n") && string(goMod[:len("module lingxi\n")]) == "module lingxi\n" {
				return filepath.EvalSymlinks(current)
			}
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("repository root not found")
		}
		current = parent
	}
}

func canonicalPotential(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	var suffix []string
	for current := absolute; ; current = filepath.Dir(current) {
		if _, err := os.Lstat(current); err == nil {
			resolved, err := filepath.EvalSymlinks(current)
			if err != nil {
				return "", err
			}
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return filepath.Clean(resolved), nil
		} else if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("output has no existing ancestor")
		}
		suffix = append(suffix, filepath.Base(current))
	}
}
