package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"lingxi/internal/exam"
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
	case "draft":
		return draft(args[1:])
	case "seal":
		return seal(args[1:])
	case "start-next":
		return startNext(args[1:])
	case "consume":
		return consume(args[1:], false)
	case "crash":
		return consume(args[1:], true)
	case "status":
		return status(args[1:])
	default:
		return usage()
	}
}

func draft(args []string) error {
	fs := flag.NewFlagSet("draft", flag.ContinueOnError)
	state := fs.String("state", "", "explicit exam registry path")
	id := fs.String("id", "", "exam id")
	gen := fs.String("generation", "", "generation id")
	order := fs.Int("order", 0, "fixed execution order")
	challengeHash := fs.String("challenge-hash", "", "sealed challenge hash")
	protocolHash := fs.String("protocol-hash", "", "pre-registered protocol hash")
	dataHash := fs.String("data-hash", "", "data manifest hash")
	if err := fs.Parse(args); err != nil {
		return err
	}
	r, err := exam.LoadRegistry(*state)
	if err != nil {
		return err
	}
	e := exam.NewDraft(*id, *gen, *order, *challengeHash, *protocolHash, *dataHash, time.Now().UTC())
	return exam.SaveRegistry(*state, r.Upsert(e))
}

func seal(args []string) error {
	fs := flag.NewFlagSet("seal", flag.ContinueOnError)
	state := fs.String("state", "", "explicit exam registry path")
	id := fs.String("id", "", "exam id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	r, err := exam.LoadRegistry(*state)
	if err != nil {
		return err
	}
	e, ok := r.Get(*id)
	if !ok {
		return fmt.Errorf("exam %s not found", *id)
	}
	sealed, err := exam.Seal(e, time.Now().UTC())
	if err != nil {
		return err
	}
	return exam.SaveRegistry(*state, r.Upsert(sealed))
}

func startNext(args []string) error {
	fs := flag.NewFlagSet("start-next", flag.ContinueOnError)
	state := fs.String("state", "", "explicit exam registry path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	r, err := exam.LoadRegistry(*state)
	if err != nil {
		return err
	}
	updated, started, ok, err := r.StartNext(time.Now().UTC())
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("no sealed exam available")
	}
	if err := exam.SaveRegistry(*state, updated); err != nil {
		return err
	}
	return printJSON(started)
}

func consume(args []string, crashed bool) error {
	fs := flag.NewFlagSet("consume", flag.ContinueOnError)
	state := fs.String("state", "", "explicit exam registry path")
	id := fs.String("id", "", "exam id")
	passed := fs.Bool("passed", false, "verdict passed")
	reason := fs.String("reason", "", "verdict or crash reason")
	decisionID := fs.String("decision-id", "", "decision id")
	signingKeyEnv := fs.String("signing-key-env", "LINGXI_EXAM_SIGNING_KEY", "签名密钥环境变量名")
	keyID := fs.String("key-id", "local", "签名密钥标识")
	allowUnsigned := fs.Bool("allow-unsigned", false, "仅开发环境允许无签名receipt")
	if err := fs.Parse(args); err != nil {
		return err
	}
	r, err := exam.LoadRegistry(*state)
	if err != nil {
		return err
	}
	e, ok := r.Get(*id)
	if !ok {
		return fmt.Errorf("exam %s not found", *id)
	}
	var signer exam.ReceiptSigner
	if key := os.Getenv(*signingKeyEnv); key != "" {
		signer = exam.HMACSigner{KeyID: *keyID, Key: []byte(key)}
	} else if *allowUnsigned {
		signer = exam.UnsignedBoundarySigner{}
	} else {
		return fmt.Errorf("缺少密封考试签名密钥环境变量 %s", *signingKeyEnv)
	}
	var consumed exam.Exam
	if crashed {
		consumed, err = exam.Crash(context.Background(), e, *reason, time.Now().UTC(), signer)
	} else {
		consumed, err = exam.Consume(context.Background(), e, exam.Verdict{
			Passed:     *passed,
			Reason:     *reason,
			DecisionID: *decisionID,
		}, time.Now().UTC(), signer)
	}
	if err != nil {
		return err
	}
	if err := exam.SaveRegistry(*state, r.Upsert(consumed)); err != nil {
		return err
	}
	return printJSON(consumed.Receipt)
}

func status(args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	state := fs.String("state", "", "explicit exam registry path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	r, err := exam.LoadRegistry(*state)
	if err != nil {
		return err
	}
	return printJSON(r)
}

func printJSON(v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(b))
	return nil
}

func usage() error {
	return fmt.Errorf("usage: sealedexam draft|seal|start-next|consume|crash|status --state <path>; command never reads or writes production config")
}
