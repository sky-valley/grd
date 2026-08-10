package main

import (
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
	"time"

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
	case "requirements":
		return runRequirements(ctx, args[1:], stdout, stderr)
	case "respond":
		return runRespond(ctx, args[1:], stdin, stdout, stderr)
	case "history":
		return runHistory(ctx, args[1:], stdout, stderr)
	case "watch":
		return runWatch(ctx, args[1:], stdout, stderr)
	case "submit":
		return runSubmit(ctx, args[1:], stdout, stderr)
	case "status":
		return runStatus(ctx, args[1:], stdout, stderr)
	case "sync":
		return runSync(ctx, args[1:], stdout, stderr)
	case "change":
		return runChange(ctx, args[1:], stdout, stderr)
	case "amend":
		return runAmend(ctx, args[1:], stdin, stdout, stderr)
	case "rebase-held":
		return runRebaseHeld(ctx, args[1:], stdin, stdout, stderr)
	case "reconcile-dependent":
		return runReconcileDependent(ctx, args[1:], stdin, stdout, stderr)
	case "record-conflict":
		return runRecordConflict(ctx, args[1:], stdin, stdout, stderr)
	case "resolve-conflict":
		return runResolveConflict(ctx, args[1:], stdin, stdout, stderr)
	default:
		writeUsage(stderr)
		return 2
	}
}

func runHistory(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer) int {
	flags := flag.NewFlagSet("grd history", flag.ContinueOnError)
	flags.SetOutput(stderr)
	server := flags.String("server", "", "absolute URL of a running grds control endpoint")
	cursor := flags.String("cursor", "", "opaque history cursor")
	limit := flags.Int("limit", 50, "maximum facts to return")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 || strings.TrimSpace(*server) == "" || *limit < 1 || *limit > 100 {
		fmt.Fprintln(stderr, "grd history: --server is required, --limit must be between one and 100, and positional arguments are not accepted")
		return 2
	}
	page, err := (controlhttp.Client{Server: *server}).History(ctx, *cursor, *limit)
	if err != nil {
		fmt.Fprintf(stderr, "grd history: %v\n", err)
		return 1
	}
	if err := json.NewEncoder(stdout).Encode(page); err != nil {
		fmt.Fprintf(stderr, "grd history: write output: %v\n", err)
		return 1
	}
	return 0
}

func runWatch(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer) int {
	flags := flag.NewFlagSet("grd watch", flag.ContinueOnError)
	flags.SetOutput(stderr)
	server := flags.String("server", "", "absolute URL of a running grds control endpoint")
	cursor := flags.String("after", "", "opaque cursor of the last observed fact")
	limit := flags.Int("limit", 100, "maximum facts to fetch per request")
	pollInterval := flags.Duration("poll-interval", time.Second, "delay after reaching the current end of history")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 || strings.TrimSpace(*server) == "" || *limit < 1 || *limit > 100 || *pollInterval <= 0 {
		fmt.Fprintln(stderr, "grd watch: --server is required, --limit must be between one and 100, --poll-interval must be positive, and positional arguments are not accepted")
		return 2
	}
	client := controlhttp.Client{Server: *server}
	next := *cursor
	encoder := json.NewEncoder(stdout)
	for {
		page, err := client.History(ctx, next, *limit)
		if err != nil {
			if ctx.Err() != nil {
				return 0
			}
			fmt.Fprintf(stderr, "grd watch: %v\n", err)
			return 1
		}
		for _, fact := range page.Facts {
			envelope := controlhttp.HistoryFactEnvelope{Schema: controlhttp.HistoryFactSchema, Repository: page.Repository, Fact: fact}
			if err := encoder.Encode(envelope); err != nil {
				fmt.Fprintf(stderr, "grd watch: write output: %v\n", err)
				return 1
			}
			next = fact.Cursor
		}
		if page.NextCursor != "" {
			next = page.NextCursor
			continue
		}
		timer := time.NewTimer(*pollInterval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return 0
		case <-timer.C:
		}
	}
}

func runRequirements(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer) int {
	flags := flag.NewFlagSet("grd requirements", flag.ContinueOnError)
	flags.SetOutput(stderr)
	server := flags.String("server", "", "absolute URL of a running grds control endpoint")
	cursor := flags.String("cursor", "", "opaque cursor from a previous Requirement page")
	limit := flags.Int("limit", 50, "maximum Requirements to return")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 || strings.TrimSpace(*server) == "" || *limit < 1 || *limit > 100 {
		fmt.Fprintln(stderr, "grd requirements: --server is required, --limit must be between one and 100, and positional arguments are not accepted")
		return 2
	}
	page, err := (controlhttp.Client{Server: *server}).Requirements(ctx, *cursor, *limit)
	if err != nil {
		fmt.Fprintf(stderr, "grd requirements: %v\n", err)
		return 1
	}
	if err := json.NewEncoder(stdout).Encode(page); err != nil {
		fmt.Fprintf(stderr, "grd requirements: write output: %v\n", err)
		return 1
	}
	return 0
}

func runRespond(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer) int {
	flags := flag.NewFlagSet("grd respond", flag.ContinueOnError)
	flags.SetOutput(stderr)
	server := flags.String("server", "", "absolute URL of a running grds control endpoint")
	idempotencyKey := flags.String("idempotency-key", "", "stable identity for safe Response retries")
	input := flags.String("input", "", "Requirement Response JSON file, or - for stdin")
	version := flags.String("version", "", "Version carrying the Requirement")
	policy := flags.String("policy", "", "policy identifying the Requirement")
	decision := flags.String("decision", "", "satisfied or revision_requested")
	rationale := flags.String("rationale", "", "durable rationale for the Response")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 || strings.TrimSpace(*server) == "" || strings.TrimSpace(*idempotencyKey) == "" {
		fmt.Fprintln(stderr, "grd respond: --server and --idempotency-key are required; positional arguments are not accepted")
		return 2
	}
	var request controlhttp.RequirementResponseRequest
	if *input != "" {
		if *version != "" || *policy != "" || *decision != "" || *rationale != "" {
			fmt.Fprintln(stderr, "grd respond: --input cannot be combined with Response content flags")
			return 2
		}
		var err error
		request, err = readJSONInput[controlhttp.RequirementResponseRequest](stdin, *input)
		if err != nil {
			fmt.Fprintf(stderr, "grd respond: read input: %v\n", err)
			return 2
		}
	} else {
		if strings.TrimSpace(*version) == "" || strings.TrimSpace(*policy) == "" || strings.TrimSpace(*decision) == "" || strings.TrimSpace(*rationale) == "" {
			fmt.Fprintln(stderr, "grd respond: --version, --policy, --decision, and --rationale are required without --input")
			return 2
		}
		request = controlhttp.RequirementResponseRequest{
			Schema:    controlhttp.RequirementResponseSchema,
			Version:   *version,
			Policy:    *policy,
			Decision:  *decision,
			Rationale: *rationale,
		}
	}
	receipt, err := (controlhttp.Client{Server: *server}).RespondRequirement(ctx, *idempotencyKey, request)
	if err != nil {
		fmt.Fprintf(stderr, "grd respond: %v\n", err)
		return 1
	}
	if err := json.NewEncoder(stdout).Encode(receipt); err != nil {
		fmt.Fprintf(stderr, "grd respond: write output: %v\n", err)
		return 1
	}
	return 0
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
	return readJSONInput[controlhttp.ProposalRequest](stdin, path)
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
	fmt.Fprintln(w, "       grd requirements --server <url> [--cursor <cursor>] [--limit <n>]")
	fmt.Fprintln(w, "       grd respond --server <url> --idempotency-key <key> --version <version> --policy <policy> --decision <decision> --rationale <text>")
	fmt.Fprintln(w, "       grd respond --server <url> --idempotency-key <key> --input <-|path>")
	fmt.Fprintln(w, "       grd history --server <url> [--cursor <cursor>] [--limit <n>]")
	fmt.Fprintln(w, "       grd watch --server <url> [--after <cursor>] [--poll-interval <duration>]")
	fmt.Fprintln(w, "       grd submit --server <url> [--workdir <path>] [--idempotency-key <key>] [--dependency <version>]...")
	fmt.Fprintln(w, "       grd status --server <url> [--workdir <path>]")
	fmt.Fprintln(w, "       grd sync --server <url> [--workdir <path>] [--version <version>] [--rationale <text>]")
	fmt.Fprintln(w, "       grd change --server <url> --id <change>")
	fmt.Fprintln(w, "       grd <amend|rebase-held|reconcile-dependent|record-conflict|resolve-conflict> --server <url> --idempotency-key <key> --input <-|path>")
}
