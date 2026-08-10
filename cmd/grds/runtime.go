package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sky-valley/grd/internal/evaluation"
	"github.com/sky-valley/grd/internal/evaluatorexec"
	"github.com/sky-valley/grd/internal/gitengine"
	"github.com/sky-valley/grd/internal/gitreader"
	"github.com/sky-valley/grd/internal/intent"
	"github.com/sky-valley/grd/internal/intentservice"
	"github.com/sky-valley/grd/internal/ledgerfs"
)

type singleRepositoryConfig struct {
	RepositoryID string
	GitDir       string
	LedgerPath   string
	TrunkRef     string
	Evaluator    evaluatorexec.Config
	Runner       evaluation.RunnerOptions
}

type singleRepositoryRuntime struct {
	service *intentservice.Service
	runner  *evaluation.PendingRunner
	ledger  *ledgerfs.Ledger
}

func openSingleRepository(ctx context.Context, config singleRepositoryConfig) (*singleRepositoryRuntime, error) {
	if err := validateSingleRepositoryConfig(config); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	evaluator, err := evaluatorexec.New(config.Evaluator)
	if err != nil {
		return nil, fmt.Errorf("configure evaluator: %w", err)
	}
	engine, err := gitengine.Open(ctx, config.GitDir, config.TrunkRef)
	if err != nil {
		return nil, fmt.Errorf("open Git engine: %w", err)
	}
	initial, err := engine.Current(ctx)
	if err != nil {
		return nil, fmt.Errorf("read Git trunk: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(config.LedgerPath), 0o700); err != nil {
		return nil, fmt.Errorf("create ledger directory: %w", err)
	}
	ledger, err := ledgerfs.Open(config.LedgerPath)
	if err != nil {
		return nil, fmt.Errorf("open decision ledger: %w", err)
	}
	closeOnError := func(openErr error) (*singleRepositoryRuntime, error) {
		return nil, errors.Join(openErr, ledger.Close())
	}
	repository, err := intent.OpenRepository(ctx, initial, ledger, engine, engine)
	if err != nil {
		return closeOnError(fmt.Errorf("open repository intent: %w", err))
	}
	if conflict, found := repository.ProjectionConflict(); found {
		return closeOnError(fmt.Errorf("reconcile repository intent: %w", &conflict))
	}
	projected, err := engine.Current(ctx)
	if err != nil {
		return closeOnError(fmt.Errorf("verify Git trunk: %w", err))
	}
	if projected != repository.CurrentIntent().Content {
		return closeOnError(errors.New("Git trunk diverges from accepted repository Intent"))
	}
	owned := &ownedRepository{
		id:         config.RepositoryID,
		gitDir:     config.GitDir,
		repository: repository,
	}
	service := intentservice.New(owned)
	content, err := gitreader.NewSource(owned)
	if err != nil {
		return closeOnError(fmt.Errorf("open Git reader: %w", err))
	}
	processor, err := evaluation.NewPolicyProcessorFactory(evaluator).Build(service, content)
	if err != nil {
		return closeOnError(fmt.Errorf("build evaluation processor: %w", err))
	}
	runner := evaluation.NewPendingRunner(
		&singleRepositoryPendingSource{repositoryID: config.RepositoryID, service: service},
		evaluation.NewMemoryLeases(nil),
		processor,
		config.Runner,
	)
	return &singleRepositoryRuntime{service: service, runner: runner, ledger: ledger}, nil
}

func validateSingleRepositoryConfig(config singleRepositoryConfig) error {
	if config.RepositoryID == "" || config.RepositoryID != strings.TrimSpace(config.RepositoryID) || strings.ContainsAny(config.RepositoryID, "\r\n") {
		return errors.New("repository id must be canonical one-line text")
	}
	if strings.TrimSpace(config.GitDir) == "" {
		return errors.New("Git directory is required")
	}
	if config.GitDir != strings.TrimSpace(config.GitDir) {
		return errors.New("Git directory must not have surrounding whitespace")
	}
	if strings.TrimSpace(config.LedgerPath) == "" {
		return errors.New("ledger path is required")
	}
	if strings.TrimSpace(config.TrunkRef) == "" {
		return errors.New("trunk ref is required")
	}
	return nil
}

func (runtime *singleRepositoryRuntime) Service() *intentservice.Service {
	return runtime.service
}

func (runtime *singleRepositoryRuntime) Run(ctx context.Context) {
	runtime.runner.Run(ctx)
}

func (runtime *singleRepositoryRuntime) Close() error {
	return runtime.ledger.Close()
}

type ownedRepository struct {
	id         string
	gitDir     string
	repository *intent.Repository
}

func (owned *ownedRepository) Resolve(ctx context.Context, repositoryID string) (intentservice.Repository, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if repositoryID != owned.id {
		return nil, intentservice.ErrRepositoryNotFound
	}
	return owned.repository, nil
}

func (owned *ownedRepository) Bootstrap(ctx context.Context, repositoryID string, content intent.ContentRef) (intent.Revision, error) {
	repository, err := owned.Resolve(ctx, repositoryID)
	if err != nil {
		return intent.Revision{}, err
	}
	current := repository.CurrentIntent()
	if current.Content == content {
		return current, nil
	}
	return intent.Revision{}, intentservice.ErrRepositoryAlreadyInitialized
}

func (owned *ownedRepository) GitDir(ctx context.Context, repositoryID string) (string, error) {
	if _, err := owned.Resolve(ctx, repositoryID); err != nil {
		return "", err
	}
	return owned.gitDir, nil
}

type singleRepositoryPendingSource struct {
	repositoryID string
	service      *intentservice.Service
}

func (source *singleRepositoryPendingSource) ListPending(ctx context.Context, after string, limit int) (evaluation.PendingPage, error) {
	page, err := source.service.RunnableEvaluations(ctx, source.repositoryID, intent.PendingEvaluationQuery{
		After: intent.VersionID(after),
		Limit: limit,
	})
	if err != nil {
		return evaluation.PendingPage{}, err
	}
	items := make([]evaluation.WorkItem, len(page.Versions))
	for index, version := range page.Versions {
		items[index] = evaluation.WorkItem{RepoID: source.repositoryID, VersionID: version.ID}
	}
	return evaluation.PendingPage{Items: items, NextCursor: string(page.NextCursor)}, nil
}
