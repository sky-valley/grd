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

	"github.com/sky-valley/grd/internal/evaluatorexec"
)

const hostReadySchema = "grd.host-ready/v1"

type hostReady struct {
	Schema     string      `json:"schema"`
	Repository string      `json:"repository"`
	Intent     string      `json:"intent"`
	Content    hostContent `json:"content"`
}

type hostContent struct {
	Engine   string `json:"engine"`
	Revision string `json:"revision"`
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr, os.LookupEnv); err != nil {
		fmt.Fprintf(os.Stderr, "grds: %v\n", err)
		os.Exit(1)
	}
}

func run(
	ctx context.Context,
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	lookupEnv func(string) (string, bool),
) (runErr error) {
	config, err := parseCommandConfig(args, stderr, lookupEnv)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	config.Runner.Report = func(err error) {
		fmt.Fprintf(stderr, "grds: %v\n", err)
	}
	runtime, err := openSingleRepository(ctx, config)
	if err != nil {
		return err
	}
	defer func() {
		runErr = errors.Join(runErr, runtime.Close())
	}()
	current, err := runtime.Service().CurrentIntent(ctx, config.RepositoryID)
	if err != nil {
		return fmt.Errorf("read ready state: %w", err)
	}
	if err := json.NewEncoder(stdout).Encode(hostReady{
		Schema:     hostReadySchema,
		Repository: config.RepositoryID,
		Intent:     string(current.ID),
		Content: hostContent{
			Engine:   current.Content.Engine,
			Revision: current.Content.Revision,
		},
	}); err != nil {
		return fmt.Errorf("write readiness receipt: %w", err)
	}
	runtime.Run(ctx)
	return nil
}

func parseCommandConfig(
	args []string,
	diagnostics io.Writer,
	lookupEnv func(string) (string, bool),
) (singleRepositoryConfig, error) {
	flags := flag.NewFlagSet("grds", flag.ContinueOnError)
	flags.SetOutput(diagnostics)
	var config singleRepositoryConfig
	var evaluatorEnvironment environmentNames
	flags.StringVar(&config.RepositoryID, "repository", "", "opaque repository id")
	flags.StringVar(&config.GitDir, "git-dir", "", "path to the repository Git directory")
	flags.StringVar(&config.LedgerPath, "ledger", "", "path to the append-only decision ledger")
	flags.StringVar(&config.TrunkRef, "trunk", "refs/heads/main", "accepted Git branch ref")
	flags.StringVar(&config.Evaluator.Executable, "evaluator", "", "external evaluator executable")
	flags.Var(&evaluatorEnvironment, "evaluator-env", "environment variable name to forward to the evaluator; repeatable")
	flags.IntVar(&config.Runner.Workers, "workers", 1, "concurrent evaluation workers")
	flags.DurationVar(&config.Runner.PollInterval, "poll-interval", time.Second, "pending evaluation poll interval")
	if err := flags.Parse(args); err != nil {
		return singleRepositoryConfig{}, err
	}
	if flags.NArg() != 0 {
		return singleRepositoryConfig{}, errors.New("grds does not accept positional arguments")
	}
	if strings.TrimSpace(config.Evaluator.Executable) == "" {
		return singleRepositoryConfig{}, errors.New("evaluator executable is required")
	}
	if config.Runner.Workers < 1 || config.Runner.Workers > 100 {
		return singleRepositoryConfig{}, errors.New("workers must be between one and 100")
	}
	if config.Runner.PollInterval <= 0 {
		return singleRepositoryConfig{}, errors.New("poll interval must be positive")
	}
	if err := validateSingleRepositoryConfig(config); err != nil {
		return singleRepositoryConfig{}, err
	}
	environment, err := evaluatorEnvironment.resolve(lookupEnv)
	if err != nil {
		return singleRepositoryConfig{}, err
	}
	config.Evaluator = evaluatorexec.Config{
		Executable:  config.Evaluator.Executable,
		Environment: environment,
	}
	return config, nil
}

type environmentNames []string

func (names *environmentNames) String() string {
	return strings.Join(*names, ",")
}

func (names *environmentNames) Set(value string) error {
	if value == "" || value != strings.TrimSpace(value) || strings.ContainsAny(value, "=\x00\r\n") {
		return errors.New("evaluator environment name must be canonical one-line text")
	}
	*names = append(*names, value)
	return nil
}

func (names environmentNames) resolve(lookupEnv func(string) (string, bool)) ([]string, error) {
	if lookupEnv == nil {
		return nil, errors.New("environment lookup is required")
	}
	resolved := make([]string, 0, len(names))
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		if _, duplicate := seen[name]; duplicate {
			return nil, fmt.Errorf("evaluator environment repeats %s", name)
		}
		seen[name] = struct{}{}
		value, found := lookupEnv(name)
		if !found {
			return nil, fmt.Errorf("evaluator environment %s is not set", name)
		}
		resolved = append(resolved, name+"="+value)
	}
	return resolved, nil
}
