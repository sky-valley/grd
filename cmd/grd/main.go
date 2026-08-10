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

	"github.com/sky-valley/grd/internal/controlhttp"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer) int {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		writeUsage(stdout)
		return 0
	}
	if len(args) == 0 || args[0] != "intent" {
		writeUsage(stderr)
		return 2
	}
	flags := flag.NewFlagSet("grd intent", flag.ContinueOnError)
	flags.SetOutput(stderr)
	server := flags.String("server", "", "absolute URL of a running grds control endpoint")
	if err := flags.Parse(args[1:]); err != nil {
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

func writeUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: grd intent --server <url>")
}
