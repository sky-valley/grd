package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/sky-valley/grd/internal/controlhttp"
	"github.com/sky-valley/grd/internal/gitworkspace"
)

func runSubmit(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer) int {
	flags := flag.NewFlagSet("grd submit", flag.ContinueOnError)
	flags.SetOutput(stderr)
	server := flags.String("server", "", "absolute URL of a running grds control endpoint")
	workdir := flags.String("workdir", ".", "Git working tree to submit")
	idempotencyKey := flags.String("idempotency-key", "", "optional stable identity for safe retries")
	var dependencies stringValues
	flags.Var(&dependencies, "dependency", "admitted parent Version id; repeatable")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 || strings.TrimSpace(*server) == "" || strings.TrimSpace(*workdir) == "" {
		fmt.Fprintln(stderr, "grd submit: --server and a non-empty --workdir are required; positional arguments are not accepted")
		return 2
	}
	client := controlhttp.Client{Server: *server}
	receipt, err := gitworkspace.Submit(ctx, client, gitworkspace.SubmitRequest{
		Workdir:        *workdir,
		IdempotencyKey: *idempotencyKey,
		Dependencies:   dependencies,
	})
	if err != nil {
		fmt.Fprintf(stderr, "grd submit: %v\n", err)
		return 1
	}
	if err := json.NewEncoder(stdout).Encode(receipt); err != nil {
		fmt.Fprintf(stderr, "grd submit: write output: %v\n", err)
		return 1
	}
	return 0
}

func runStatus(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer) int {
	flags := flag.NewFlagSet("grd status", flag.ContinueOnError)
	flags.SetOutput(stderr)
	server := flags.String("server", "", "absolute URL of a running grds control endpoint")
	workdir := flags.String("workdir", ".", "Git working tree to inspect")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 || strings.TrimSpace(*server) == "" || strings.TrimSpace(*workdir) == "" {
		fmt.Fprintln(stderr, "grd status: --server and a non-empty --workdir are required; positional arguments are not accepted")
		return 2
	}
	client := controlhttp.Client{Server: *server}
	fact, err := gitworkspace.Status(ctx, client, *workdir)
	if err != nil {
		fmt.Fprintf(stderr, "grd status: %v\n", err)
		return 1
	}
	if err := json.NewEncoder(stdout).Encode(fact); err != nil {
		fmt.Fprintf(stderr, "grd status: write output: %v\n", err)
		return 1
	}
	return 0
}

func runSync(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer) int {
	flags := flag.NewFlagSet("grd sync", flag.ContinueOnError)
	flags.SetOutput(stderr)
	server := flags.String("server", "", "absolute URL of a running grds control endpoint")
	workdir := flags.String("workdir", ".", "Git working tree to reconcile")
	version := flags.String("version", "", "submitted Version to reconcile; inferred from Git ancestry when omitted")
	idempotencyKey := flags.String("idempotency-key", "", "optional stable identity for a held-Version rebase")
	rationale := flags.String("rationale", "", "rationale for a recorded held-Version rebase")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 || strings.TrimSpace(*server) == "" || strings.TrimSpace(*workdir) == "" {
		fmt.Fprintln(stderr, "grd sync: --server and a non-empty --workdir are required; positional arguments are not accepted")
		return 2
	}
	client := controlhttp.Client{Server: *server}
	fact, err := gitworkspace.Sync(ctx, client, gitworkspace.SyncRequest{Workdir: *workdir, VersionID: *version, IdempotencyKey: *idempotencyKey, Rationale: *rationale})
	if err != nil {
		fmt.Fprintf(stderr, "grd sync: %v\n", err)
		return 1
	}
	if err := json.NewEncoder(stdout).Encode(fact); err != nil {
		fmt.Fprintf(stderr, "grd sync: write output: %v\n", err)
		return 1
	}
	return 0
}
