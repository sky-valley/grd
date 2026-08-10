package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/sky-valley/grd/internal/controlhttp"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer) int {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		writeUsage(stdout)
		return 0
	}
	if len(args) == 0 {
		writeUsage(stderr)
		return 2
	}
	switch args[0] {
	case "intent":
		return runIntent(ctx, args[1:], stdout, stderr)
	case "propose":
		return runPropose(ctx, args[1:], stdin, stdout, stderr)
	case "version":
		return runVersion(ctx, args[1:], stdout, stderr)
	default:
		writeUsage(stderr)
		return 2
	}
}

func runVersion(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer) int {
	flags := flag.NewFlagSet("grd version", flag.ContinueOnError)
	flags.SetOutput(stderr)
	server := flags.String("server", "", "absolute URL of a running grds control endpoint")
	versionID := flags.String("id", "", "exact Version id")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 || strings.TrimSpace(*server) == "" || strings.TrimSpace(*versionID) == "" {
		fmt.Fprintln(stderr, "grd version: --server and --id are required; positional arguments are not accepted")
		return 2
	}
	inspection, err := (controlhttp.Client{Server: *server}).Version(ctx, *versionID)
	if err != nil {
		fmt.Fprintf(stderr, "grd version: %v\n", err)
		return 1
	}
	if err := json.NewEncoder(stdout).Encode(inspection); err != nil {
		fmt.Fprintf(stderr, "grd version: write output: %v\n", err)
		return 1
	}
	return 0
}

func runIntent(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer) int {
	flags := flag.NewFlagSet("grd intent", flag.ContinueOnError)
	flags.SetOutput(stderr)
	server := flags.String("server", "", "absolute URL of a running grds control endpoint")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 || strings.TrimSpace(*server) == "" {
		fmt.Fprintln(stderr, "grd intent: --server is required and positional arguments are not accepted")
		return 2
	}
	fact, err := (controlhttp.Client{Server: *server}).Intent(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "grd intent: %v\n", err)
		return 1
	}
	if err := json.NewEncoder(stdout).Encode(fact); err != nil {
		fmt.Fprintf(stderr, "grd intent: write output: %v\n", err)
		return 1
	}
	return 0
}

func runPropose(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer) int {
	flags := flag.NewFlagSet("grd propose", flag.ContinueOnError)
	flags.SetOutput(stderr)
	server := flags.String("server", "", "absolute URL of a running grds control endpoint")
	baseIntent := flags.String("base-intent", "", "governing accepted Intent id")
	engine := flags.String("engine", "", "content engine name")
	revision := flags.String("revision", "", "exact content-engine revision")
	idempotencyKey := flags.String("idempotency-key", "", "stable identity for safe proposal retries")
	input := flags.String("input", "", "proposal JSON file, or - for stdin")
	var dependencies stringValues
	flags.Var(&dependencies, "dependency", "admitted parent Version id; repeatable")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 || strings.TrimSpace(*server) == "" || strings.TrimSpace(*idempotencyKey) == "" {
		fmt.Fprintln(stderr, "grd propose: --server and --idempotency-key are required; positional arguments are not accepted")
		return 2
	}
	var proposal controlhttp.ProposalRequest
	if *input != "" {
		if *baseIntent != "" || *engine != "" || *revision != "" || len(dependencies) != 0 {
			fmt.Fprintln(stderr, "grd propose: --input cannot be combined with proposal content flags")
			return 2
		}
		var err error
		proposal, err = readProposalInput(stdin, *input)
		if err != nil {
			fmt.Fprintf(stderr, "grd propose: read input: %v\n", err)
			return 2
		}
	} else {
		if strings.TrimSpace(*baseIntent) == "" || strings.TrimSpace(*engine) == "" || strings.TrimSpace(*revision) == "" {
			fmt.Fprintln(stderr, "grd propose: --base-intent, --engine, and --revision are required without --input")
			return 2
		}
		proposal = controlhttp.ProposalRequest{
			Schema:       controlhttp.ProposalSchema,
			BaseIntent:   *baseIntent,
			Content:      controlhttp.Content{Engine: *engine, Revision: *revision},
			Dependencies: dependencies,
		}
	}
	receipt, err := (controlhttp.Client{Server: *server}).Propose(ctx, *idempotencyKey, proposal)
	if err != nil {
		fmt.Fprintf(stderr, "grd propose: %v\n", err)
		return 1
	}
	if err := json.NewEncoder(stdout).Encode(receipt); err != nil {
		fmt.Fprintf(stderr, "grd propose: write output: %v\n", err)
		return 1
	}
	return 0
}

func readProposalInput(stdin io.Reader, path string) (controlhttp.ProposalRequest, error) {
	reader := stdin
	closeInput := func() error { return nil }
	if path != "-" {
		file, err := os.Open(path)
		if err != nil {
			return controlhttp.ProposalRequest{}, err
		}
		reader = file
		closeInput = file.Close
	}
	if reader == nil {
		_ = closeInput()
		return controlhttp.ProposalRequest{}, errors.New("stdin is unavailable")
	}
	encoded, err := io.ReadAll(io.LimitReader(reader, 64*1024+1))
	closeErr := closeInput()
	if err != nil || closeErr != nil {
		return controlhttp.ProposalRequest{}, errors.Join(err, closeErr)
	}
	if len(encoded) > 64*1024 {
		return controlhttp.ProposalRequest{}, errors.New("proposal input exceeds 64 KiB")
	}
	var proposal controlhttp.ProposalRequest
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&proposal); err != nil {
		return controlhttp.ProposalRequest{}, err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return controlhttp.ProposalRequest{}, errors.New("proposal input must contain one JSON value")
	}
	return proposal, nil
}

type stringValues []string

func (values *stringValues) String() string {
	return strings.Join(*values, ",")
}

func (values *stringValues) Set(value string) error {
	if value == "" {
		return errors.New("dependency must not be empty")
	}
	*values = append(*values, value)
	return nil
}

func writeUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: grd intent --server <url>")
	fmt.Fprintln(w, "       grd propose --server <url> --base-intent <id> --engine <name> --revision <revision> --idempotency-key <key> [--dependency <version>]...")
	fmt.Fprintln(w, "       grd propose --server <url> --idempotency-key <key> --input <-|path>")
	fmt.Fprintln(w, "       grd version --server <url> --id <version>")
}
