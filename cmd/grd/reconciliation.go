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
)

func runChange(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer) int {
	flags := flag.NewFlagSet("grd change", flag.ContinueOnError)
	flags.SetOutput(stderr)
	server := flags.String("server", "", "absolute URL of a running grds control endpoint")
	changeID := flags.String("id", "", "exact Change id")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 || strings.TrimSpace(*server) == "" || strings.TrimSpace(*changeID) == "" {
		fmt.Fprintln(stderr, "grd change: --server and --id are required; positional arguments are not accepted")
		return 2
	}
	fact, err := (controlhttp.Client{Server: *server}).Change(ctx, *changeID)
	if err != nil {
		fmt.Fprintf(stderr, "grd change: %v\n", err)
		return 1
	}
	if err := json.NewEncoder(stdout).Encode(fact); err != nil {
		fmt.Fprintf(stderr, "grd change: write output: %v\n", err)
		return 1
	}
	return 0
}

func runAmend(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer) int {
	return runMutation[controlhttp.AmendmentRequest](ctx, "grd amend", args, stdin, stdout, stderr, func(client controlhttp.Client, key string, request controlhttp.AmendmentRequest) (any, error) {
		return client.Amend(ctx, key, request)
	})
}

func runRebaseHeld(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer) int {
	return runMutation[controlhttp.HeldVersionRebaseRequest](ctx, "grd rebase-held", args, stdin, stdout, stderr, func(client controlhttp.Client, key string, request controlhttp.HeldVersionRebaseRequest) (any, error) {
		return client.RebaseHeldVersion(ctx, key, request)
	})
}

func runReconcileDependent(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer) int {
	return runMutation[controlhttp.DependentReconciliationRequest](ctx, "grd reconcile-dependent", args, stdin, stdout, stderr, func(client controlhttp.Client, key string, request controlhttp.DependentReconciliationRequest) (any, error) {
		return client.ReconcileDependent(ctx, key, request)
	})
}

func runRecordConflict(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer) int {
	return runMutation[controlhttp.ReconciliationConflictRequest](ctx, "grd record-conflict", args, stdin, stdout, stderr, func(client controlhttp.Client, key string, request controlhttp.ReconciliationConflictRequest) (any, error) {
		return client.RecordReconciliationConflict(ctx, key, request)
	})
}

func runResolveConflict(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer) int {
	return runMutation[controlhttp.ReconciliationResolutionRequest](ctx, "grd resolve-conflict", args, stdin, stdout, stderr, func(client controlhttp.Client, key string, request controlhttp.ReconciliationResolutionRequest) (any, error) {
		return client.ResolveReconciliationConflict(ctx, key, request)
	})
}

func runMutation[T any](ctx context.Context, name string, args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer, execute func(controlhttp.Client, string, T) (any, error)) int {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	server := flags.String("server", "", "absolute URL of a running grds control endpoint")
	idempotencyKey := flags.String("idempotency-key", "", "stable identity for safe retries")
	input := flags.String("input", "", "versioned operation JSON file, or - for stdin")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 || strings.TrimSpace(*server) == "" || strings.TrimSpace(*idempotencyKey) == "" || *input == "" {
		fmt.Fprintf(stderr, "%s: --server, --idempotency-key, and --input are required; positional arguments are not accepted\n", name)
		return 2
	}
	request, err := readJSONInput[T](stdin, *input)
	if err != nil {
		fmt.Fprintf(stderr, "%s: read input: %v\n", name, err)
		return 2
	}
	receipt, err := execute(controlhttp.Client{Server: *server}, *idempotencyKey, request)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return 1
	}
	if err := json.NewEncoder(stdout).Encode(receipt); err != nil {
		fmt.Fprintf(stderr, "%s: write output: %v\n", name, err)
		return 1
	}
	return 0
}
