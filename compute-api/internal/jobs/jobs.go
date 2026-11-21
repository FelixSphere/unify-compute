// Package jobs is the wiring between the pieces that run a compute job: the
// job store, the console hold and usage ledger, the SkyPilot backend and the
// event log (docs/spec/job-api.md, metering.md, backend.md, events.md). The
// HTTP handlers in package api call it; it knows nothing about HTTP.
//
// Three adapters live here, each the "for the wiring task" half of an
// interface another package defined:
//
//   - backendStore (backend.Store): every status change the backend makes is
//     written together with its job.* event, in one transaction;
//   - sink (backend.Sink, backend.UpObserver): lifecycle edges become ledger
//     Lifecycle calls, so GPU metering starts at cluster UP and stops at the
//     attempt's end;
//   - actions (ledger.Actions): the ledger's decision to stop a job is
//     carried out through the backend, and its notices become events.
package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/FelixSphere/unify-compute/compute-api/internal/backend"
	"github.com/FelixSphere/unify-compute/compute-api/internal/console"
	"github.com/FelixSphere/unify-compute/compute-api/internal/events"
	"github.com/FelixSphere/unify-compute/compute-api/internal/ledger"
	"github.com/FelixSphere/unify-compute/compute-api/internal/mailer"
	"github.com/FelixSphere/unify-compute/compute-api/internal/notify"
	"github.com/FelixSphere/unify-compute/compute-api/internal/quota"
	"github.com/FelixSphere/unify-compute/compute-api/internal/sku"
	"github.com/FelixSphere/unify-compute/compute-api/internal/store"
)

var (
	// ErrBackendUnavailable: this deployment has no SkyPilot API server, so
	// nothing can be launched (upstreams.md: 503 upstream_unavailable).
	ErrBackendUnavailable = errors.New("jobs: no SkyPilot API server is configured")
	// ErrConsoleUnavailable: the hold could not be placed because the console
	// did not answer usably. Nothing was persisted.
	ErrConsoleUnavailable = errors.New("jobs: console bridge unavailable")
	// ErrTerminal refuses a cancel of a job whose outcome is already decided.
	ErrTerminal = errors.New("jobs: job has already ended")
	// ErrUnrunnable is a spec the API accepted but the backend cannot
	// translate: a validation gap, not a customer error.
	ErrUnrunnable = errors.New("jobs: spec cannot be launched")
)

// Intervals of the background loops. LedgerTick is the settlement cadence
// (metering.md R1).
const (
	PollInterval     = 15 * time.Second
	LedgerTick       = ledger.SettleInterval
	DeliveryInterval = 5 * time.Second
	PurgeInterval    = time.Hour
)

type Config struct {
	Store   *store.Store
	Console *console.Client
	// Biller, when set, is the ledger's biller instead of Console: the
	// local biller (internal/billing) under ACCOUNTS_BACKEND=local. A biller
	// with a Sweep method has it run hourly, in every process.
	Biller ledger.Biller
	// Gate, when set, decides before quota and the hold whether the
	// submitting account may run jobs at all (accounts.md, "Job API
	// changes"). Its refusal is returned as is and nothing is persisted.
	Gate SubmitGate
	// SkyPilot is the API server client; nil leaves the service able to read
	// and cancel jobs but not to submit them.
	SkyPilot  *backend.Client
	Catalogue *sku.Catalogue
	// Quota, when set, is enforced inside the admission transaction.
	Quota *quota.Enforcer
	// Outputs uploads spec.outputs (internal/artifacts); nil refuses them.
	Outputs backend.OutputUploader
	// LogArchive reads a job's log after its cluster is gone (the run step
	// uploads it with the outputs). Nil serves only what the cluster has.
	LogArchive LogArchive
	// Elect makes the service drive jobs (backend loop, ledger tick, cancels)
	// only while it holds the store's worker lock, so two processes on one
	// database never both do. Without it the service always drives, which
	// is what a test that owns its database wants.
	Elect bool
	Now   func() time.Time
	Log   *slog.Logger
	// Mailer and Notifier, both set, email the account's owners when a job
	// succeeds, fails or is stopped for billing (notifications.md). Either
	// nil sends nothing.
	Mailer   *mailer.Mailer
	Notifier *notify.Notifier
}

// SubmitGate refuses a submit from an account that may not run jobs:
// suspended, closed, or compute not enabled (ADR-0011 D3).
type SubmitGate interface {
	CheckSubmit(ctx context.Context, accountID int) error
}

type sweeper interface {
	Sweep(ctx context.Context) (int, error)
}

// LogArchive is internal/artifacts' read of a job's archived log.
type LogArchive interface {
	Log(ctx context.Context, jobID string) ([]byte, error)
}

type Service struct {
	Store   *store.Store
	Ledger  *ledger.Engine
	Backend *backend.Backend
	Events  *events.Worker

	logArchive LogArchive

	catalogue *sku.Catalogue
	quota     *quota.Enforcer
	gate      SubmitGate
	sweeper   sweeper
	task      backend.TaskOptions
	now       func() time.Time
	log       *slog.Logger

	elect bool
	drive atomic.Bool
	lock  *store.WorkerLock
	// swept: this term's holder has looked for leftover clusters.
	swept bool

	mu sync.Mutex
	// ended holds each attempt's ended edge from Emit until the status write
	// it precedes, which needs its Failure and Placement for the event.
	ended map[string]backend.Event
	// stops are the ledger's requests to stop a job, carried out by the
	// backend loop. They cannot be carried out inside Actions.Stop: the
	// ledger calls it holding its lock, and stopping a job reports the end
	// back to the ledger.
	stops map[string]ledger.EndReason

	mail     *mailer.Mailer
	notifier *notify.Notifier
	mailWG   sync.WaitGroup
}

func New(cfg Config) *Service {
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	// Every input that is not an https:// URL would be copied with our own
	// cloud credentials (ADR-0005), so a deployment never launches one.
	task := backend.TaskOptions{Outputs: cfg.Outputs, HTTPSInputsOnly: true}
	s := &Service{
		Store: cfg.Store, catalogue: cfg.Catalogue, quota: cfg.Quota, task: task, now: now, log: cfg.Log,
		ended: map[string]backend.Event{}, stops: map[string]ledger.EndReason{}, elect: cfg.Elect,
		mail: cfg.Mailer, notifier: cfg.Notifier, logArchive: cfg.LogArchive,
	}
	s.drive.Store(!cfg.Elect)
	var biller ledger.Biller = cfg.Console
	if cfg.Biller != nil {
		biller = cfg.Biller
		s.sweeper, _ = cfg.Biller.(sweeper)
	}
	s.gate = cfg.Gate
	s.Ledger = ledger.New(cfg.Store.Pool(), biller, actions{s}, now)
	if cfg.SkyPilot != nil {
		s.Backend = backend.New(cfg.SkyPilot, backendStore{s}, sink{s}, backend.Config{
			Task: task, Now: now,
			Logf: func(format string, args ...any) { s.log.Warn(fmt.Sprintf(format, args...)) },
		})
	}
	s.Events = events.NewWorker(cfg.Store, cfg.Log)
	s.Events.Now = now
	return s
}

// --- submit --------------------------------------------------------------

// Submission is a validated, placed submit. Candidates is what
// upstream.Registry.Place resolved.
type Submission struct {
	Owner string
	// UserID is the billing entity the hold is placed against: the console
	// user, or the account id under ACCOUNTS_BACKEND=local.
	UserID      int
	TokenID     int
	SKU         sku.SKU
	Candidates  []sku.Provider
	Spec        backend.Spec
	Metadata    map[string]string
	Idempotency *store.Idempotency
	// TemplateID is the template the submit named, recorded on the job so a
	// listing can count its runs (marketplace.md). Empty for a plain spec.
	TemplateID string
}

// Submit runs job-api.md's submit flow after validation: quota and the
// console hold inside one admission transaction, the job persisted queued
// with its job.queued event, then handed to the backend. created is false on
// an idempotent replay, which returns the original job and touches nothing.
//
// A refused hold returns *ledger.InsufficientBalanceError and a quota
// refusal *quota.ExceededError; in both cases no job exists.
func (s *Service) Submit(ctx context.Context, sub Submission) (job store.Job, created bool, err error) {
	if s.gate != nil {
		if err := s.gate.CheckSubmit(ctx, sub.UserID); err != nil {
			return store.Job{}, false, err
		}
	}
	if s.Backend == nil {
		return store.Job{}, false, ErrBackendUnavailable
	}
	id := store.NewJobID(s.now())
	bj := backend.Job{ID: id, Attempt: 1, Spec: sub.Spec, SKU: sub.SKU, Candidates: sub.Candidates, Owner: sub.Owner}
	if err := s.CheckSpec(bj); err != nil {
		return store.Job{}, false, fmt.Errorf("%w: %w", ErrUnrunnable, err)
	}
	spec, err := json.Marshal(sub.Spec)
	if err != nil {
		return store.Job{}, false, err
	}
	queued, err := newEvent(events.JobQueued, events.QueuedData{SKU: sub.SKU.ID})
	if err != nil {
		return store.Job{}, false, err
	}
	candidates, err := json.Marshal(sub.Candidates)
	if err != nil {
		return store.Job{}, false, err
	}

	// touched is set once the console may hold something for this job id,
	// which a failure after that point must give back.
	touched := false
	place := func(ctx context.Context) error {
		touched = true
		_, err := s.Ledger.PlaceHold(ctx, ledger.Job{
			JobID: id, UserID: sub.UserID, TokenID: sub.TokenID, SKU: sub.SKU.ID,
			RateUSDPerHour: sub.SKU.PriceUSDPerHour, MaxRuntimeSeconds: int64(sub.Spec.MaxRuntimeSeconds),
			SubmittedAt: s.now(),
		})
		var refused *ledger.InsufficientBalanceError
		switch {
		case err == nil, errors.As(err, &refused):
			return err
		case consoleFailure(err):
			return fmt.Errorf("%w: %v", ErrConsoleUnavailable, err)
		}
		return err
	}
	nj := store.NewJob{
		ID: id, Owner: sub.Owner, SKU: sub.SKU.ID, Spec: spec, Metadata: sub.Metadata,
		Candidates: candidates, Events: []store.NewEvent{*queued},
	}
	// A dashboard submit has no key (TokenID 0): the job records none.
	if sub.TokenID != 0 {
		tokenID := int64(sub.TokenID)
		nj.TokenID = &tokenID
	}
	if sub.TemplateID != "" {
		nj.TemplateID = &sub.TemplateID
	}
	switch {
	case s.quota != nil && s.quota.Platform != nil:
		job, created, err = s.Store.CreateAdmittedPlatform(ctx, nj, sub.Idempotency, s.quota.AdmitPlatform(sub.Owner, sub.SKU, place))
	case s.quota != nil:
		job, created, err = s.Store.CreateAdmitted(ctx, nj, sub.Idempotency, s.quota.Admit(sub.Owner, sub.SKU, place))
	default:
		job, created, err = s.Store.CreateAdmitted(ctx, nj, sub.Idempotency, func(ctx context.Context, _ []store.SKUUsage) error { return place(ctx) })
	}
	if err != nil {
		var refused *ledger.InsufficientBalanceError
		if touched && !errors.As(err, &refused) {
			s.abandon(ctx, id)
		}
		return store.Job{}, false, err
	}
	if !created {
		return job, false, nil
	}

	// The job is accepted; from here a failure is the job's, not the
	// request's. Launch must not die with the customer's connection. A
	// process without the worker lock leaves the job queued for the holder,
	// whose Reconcile launches it (backend.md, "Restart").
	if s.driving() {
		if err := s.Backend.Launch(context.WithoutCancel(ctx), bj); err != nil && !errors.Is(err, backend.ErrDuplicate) {
			s.log.Error("launch after persist", "job", id, "err", err)
		}
	}
	job, err = s.Store.Get(ctx, sub.Owner, id)
	return job, true, err
}

// CheckSpec reports whether the backend can launch j as given, with this
// deployment's options: the same translation Launch does, so a spec that
// passes here cannot fail there. The errors are backend's (ErrNoImage,
// ErrInputSource, ErrOutputsUnsupported, ...).
func (s *Service) CheckSpec(j backend.Job) error {
	if j.ID == "" {
		j.ID = store.NewJobID(s.now())
	}
	if j.Attempt == 0 {
		j.Attempt = 1
	}
	_, err := backend.TaskFor(j, s.task)
	return err
}

func (s *Service) abandon(ctx context.Context, jobID string) {
	err := s.Ledger.Abandon(context.WithoutCancel(ctx), jobID)
	if err != nil && !errors.Is(err, ledger.ErrUnknownJob) {
		s.log.Error("abandon hold of unpersisted job; the ledger retries the release", "job", jobID, "err", err)
	}
}

// consoleFailure is whether err came from talking to the console rather
// than from our own database.
func consoleFailure(err error) bool {
	var ce *console.Error
	var ue *url.Error
	return errors.As(err, &ce) || errors.As(err, &ue) || errors.Is(err, console.ErrRequestTooLarge) ||
		errors.Is(err, context.DeadlineExceeded)
}

// --- reads and cancel ------------------------------------------------------

// Usage is the ledger's view of one job. A job with no hold (one written
// straight to the store, never submitted) reads as zero.
func (s *Service) Usage(ctx context.Context, j store.Job) (ledger.JobUsage, error) {
	u, err := s.Ledger.Usage(ctx, j.ID)
	if errors.Is(err, ledger.ErrUnknownJob) {
		var spec backend.Spec
		_ = json.Unmarshal(j.Spec, &spec)
		u = ledger.JobUsage{MaxRuntimeSeconds: int64(spec.MaxRuntimeSeconds)}
		if item, err := s.catalogue.Get(j.SKU); err == nil {
			u.RateUSDPerHour = item.PriceUSDPerHour
		}
		return u, nil
	}
	return u, err
}

// Cancel ends the job at acceptedAt, which is where GPU metering stops
// (metering.md B11), whatever the teardown then takes.
func (s *Service) Cancel(ctx context.Context, owner, id string, acceptedAt time.Time) (store.Job, error) {
	job, err := s.Store.Get(ctx, owner, id)
	if err != nil {
		return store.Job{}, err
	}
	if job.Status.Terminal() {
		return job, ErrTerminal
	}
	// Durable before anything else: if this process stops before the
	// cancel is carried out, or does not hold the worker lock, the holder's
	// Reconcile carries it out at acceptedAt.
	if err := s.Store.RequestCancel(ctx, id, acceptedAt); err != nil {
		return store.Job{}, err
	}
	if !s.driving() {
		return job, nil
	}
	if s.Backend != nil && s.Backend.Tracked(id) {
		err := s.Backend.Cancel(context.WithoutCancel(ctx), id, acceptedAt)
		switch {
		case errors.Is(err, backend.ErrAlreadyEnded):
			return job, ErrTerminal
		case err != nil:
			// The end is decided and in the backend's outbox; it becomes
			// durable on a later step.
			s.log.Warn("cancel: recorded, not yet durable", "job", id, "err", err)
		}
	} else if err := s.cancelLive(ctx, job, acceptedAt); err != nil {
		return store.Job{}, err
	}
	return s.Store.Get(ctx, owner, id)
}

// endUntracked cancels a job the backend is not running and SkyPilot has no
// cluster for: one whose launch never started, or one without a backend.
func (s *Service) endUntracked(ctx context.Context, id string, jobErr *store.JobError) error {
	j, err := s.Store.UpdateStatusWithEvent(ctx, id, store.StatusCancelled, jobErr, s.statusEvent(store.StatusCancelled))
	if err != nil && !errors.Is(err, store.ErrInvalidTransition) {
		return err
	}
	// Mailed whether or not the final settle answered now: a transition
	// is made once, and the ledger retries the settle on its own.
	ferr := s.finish(ctx, id)
	if err == nil {
		s.jobEnded(j)
	}
	return ferr
}

// Logs writes the job's output. A job not yet launched has none. A finished
// one is served from its archived log (the run step uploads it with the
// outputs), since its cluster is gone; a running one from its cluster, and
// from the archive when the cluster cannot be read.
func (s *Service) Logs(ctx context.Context, owner, id string, w interface{ Write([]byte) (int, error) }) error {
	job, err := s.Store.Get(ctx, owner, id)
	if err != nil {
		return err
	}
	switch {
	case job.Status == store.StatusQueued || job.Status == store.StatusProvisioning:
		return nil
	case job.Status.Terminal():
		return s.archivedLog(ctx, id, w, nil)
	case s.Backend == nil:
		return s.archivedLog(ctx, id, w, nil)
	}
	var live bytes.Buffer
	err = s.Backend.Logs(ctx, id, &live)
	if err == nil && live.Len() > 0 {
		_, err = w.Write(live.Bytes())
		return err
	}
	if errors.Is(err, backend.ErrUnknownJob) {
		err = nil
	}
	return s.archivedLog(ctx, id, w, err)
}

// archivedLog writes the archive, or returns fallback when there is none.
func (s *Service) archivedLog(ctx context.Context, id string, w interface{ Write([]byte) (int, error) }, fallback error) error {
	if s.logArchive == nil {
		return fallback
	}
	b, err := s.logArchive.Log(ctx, id)
	if err != nil {
		if fallback != nil {
			return fallback
		}
		return err
	}
	if len(b) == 0 {
		return fallback
	}
	_, err = w.Write(b)
	return err
}

// --- background work -------------------------------------------------------

// Step runs one round of the backend loop: every attempt observed once, then
// the ledger's pending stops carried out. Tests call it to drive time. A
// process that does not hold the worker lock does nothing.
func (s *Service) Step(ctx context.Context) {
	if !s.driving() {
		return
	}
	if s.Backend != nil {
		s.Backend.StepAll(ctx)
	}
	s.ProcessStops(ctx)
}

// Run starts the background loops and returns when ctx ends and every loop
// has stopped. Webhook delivery and the idempotency purge run in every
// process; the loops that drive jobs run only while this process holds the
// worker lock (Config.Elect), or always without an election.
func (s *Service) Run(ctx context.Context) {
	var wg sync.WaitGroup
	every := s.every(ctx, &wg)
	every("idempotency purge", PurgeInterval, func(ctx context.Context) error {
		_, err := s.Store.PurgeExpiredIdempotencyKeys(ctx)
		return err
	})
	if s.sweeper != nil {
		// Each sweep is per account under its row lock, so processes that
		// sweep at once only wait for each other.
		every("billing hold sweep", PurgeInterval, func(ctx context.Context) error {
			_, err := s.sweeper.Sweep(ctx)
			return err
		})
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.Events.Run(ctx, DeliveryInterval)
	}()
	if s.elect {
		s.runElected(ctx)
	} else {
		s.runWorkers(ctx)
	}
	wg.Wait()
	s.WaitMail()
}

// runWorkers runs the loops that drive jobs until ctx ends. Each backend
// round first takes up live jobs nobody here is tracking (submitted by a
// process without the lock, or left by an earlier holder) and carries out
// recorded cancels.
func (s *Service) runWorkers(ctx context.Context) {
	var wg sync.WaitGroup
	every := s.every(ctx, &wg)
	every("backend step", PollInterval, func(ctx context.Context) error {
		err := s.Reconcile(ctx)
		s.Step(ctx)
		return err
	})
	every("ledger tick", LedgerTick, s.Ledger.Tick)
	wg.Wait()
}

func (s *Service) every(ctx context.Context, wg *sync.WaitGroup) func(string, time.Duration, func(context.Context) error) {
	return func(name string, d time.Duration, f func(context.Context) error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			t := time.NewTicker(d)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
				}
				if err := f(ctx); err != nil && ctx.Err() == nil {
					s.log.Error(name, "err", err)
				}
			}
		}()
	}
}

// ProcessStops carries out the ledger's stop requests.
func (s *Service) ProcessStops(ctx context.Context) {
	s.mu.Lock()
	pending := make(map[string]ledger.EndReason, len(s.stops))
	for id, r := range s.stops {
		pending[id] = r
	}
	s.mu.Unlock()
	for id, reason := range pending {
		jobErr := &store.JobError{Code: string(reason), Message: stopMessages[reason]}
		var err error = backend.ErrUnknownJob
		if s.Backend != nil {
			err = s.Backend.Stop(ctx, id, s.now(), &backend.Failure{Code: jobErr.Code, Message: jobErr.Message})
		}
		switch {
		case errors.Is(err, backend.ErrUnknownJob):
			err = s.endUntracked(ctx, id, jobErr)
		case errors.Is(err, backend.ErrAlreadyEnded):
			err = nil
		case err != nil:
			s.log.Warn("stop: recorded, not yet durable", "job", id, "reason", reason, "err", err)
			err = nil
		}
		if err != nil {
			s.log.Error("stop job", "job", id, "reason", reason, "err", err)
			continue
		}
		s.mu.Lock()
		delete(s.stops, id)
		s.mu.Unlock()
	}
}

var stopMessages = map[ledger.EndReason]string{
	ledger.EndBalanceExhausted:   "Stopped because the Unify API balance could not cover more runtime. Usage up to the stop is charged; top up to run again.",
	ledger.EndBillingUnavailable: "Stopped because Unify API billing could not be reached for longer than the job's hold covered. Usage up to the stop is recorded and charged once billing answers.",
}

// finish closes the job's metering. The ledger commits its own state first
// and then talks to the console; a console failure after that commit is the
// ledger's to retry on its next tick, not a reason to hold the job back.
func (s *Service) finish(ctx context.Context, id string) error {
	err := s.Ledger.Finished(ctx, id)
	if err == nil || errors.Is(err, ledger.ErrUnknownJob) {
		return nil
	}
	if h, herr := s.Ledger.Hold(ctx, id); herr == nil && (h.State == "closing" || h.State == "closed") {
		s.log.Warn("final settle deferred to the ledger tick", "job", id, "err", err)
		return nil
	}
	return err
}

func newEvent(t events.Type, data any) (*store.NewEvent, error) {
	raw := json.RawMessage(`{}`)
	if data != nil {
		b, err := json.Marshal(data)
		if err != nil {
			return nil, err
		}
		raw = b
	}
	if err := events.Validate(t, raw); err != nil {
		return nil, err
	}
	return &store.NewEvent{Type: string(t), Data: raw}, nil
}
