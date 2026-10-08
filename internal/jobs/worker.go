// Package jobs runs leased background jobs.
package jobs

import (
	"context"
	"errors"
	"math/rand"
	"sync"
	"time"

	"github.com/Untitled572/pixiv-curator/internal/domain"
)

const (
	defaultRenewInterval = 20 * time.Second
	defaultPollInterval  = time.Second
	defaultRetryBase     = 2 * time.Second
	maxRetryDelay        = 5 * time.Minute
)

// Repository is the persistence boundary required by Worker.
type Repository interface {
	Claim(context.Context, time.Time) (domain.Job, error)
	Renew(context.Context, domain.Job, time.Time) error
	Finish(context.Context, domain.Job, string, string, time.Time) error
}

type Handler func(context.Context, domain.Job) error

type ErrorClass string

const (
	Retry      ErrorClass = "retry"
	PausedAuth ErrorClass = "paused_auth"
	Failed     ErrorClass = "failed"
)

// JobError carries a stable classification and safe, stable code. Err should
// be a local error and must not contain credentials or raw upstream responses.
type JobError struct {
	Class      ErrorClass
	Code       string
	RetryAfter time.Duration
	Err        error
}

func (e *JobError) Error() string {
	if e.Code != "" {
		return string(e.Class) + ": " + e.Code
	}
	return string(e.Class)
}

func (e *JobError) Unwrap() error { return e.Err }

// Config permits deterministic short intervals in tests. Zero values retain
// the safe production defaults.
type Config struct {
	RenewInterval time.Duration
	PollInterval  time.Duration
	Now           func() time.Time
	Jitter        func(time.Duration) time.Duration
}

type Worker struct {
	repo     Repository
	handlers map[string]Handler
	config   Config
	rngMu    sync.Mutex
}

func NewWorker(repo Repository, handlers map[string]Handler, config ...Config) *Worker {
	c := Config{}
	if len(config) > 0 {
		c = config[0]
	}
	if c.RenewInterval <= 0 {
		c.RenewInterval = defaultRenewInterval
	}
	if c.PollInterval <= 0 {
		c.PollInterval = defaultPollInterval
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return &Worker{repo: repo, handlers: handlers, config: c}
}

// Run processes one job at a time until ctx is canceled. Claim errors are
// retried after a bounded poll delay, so an unavailable store cannot busy-loop.
func (w *Worker) Run(ctx context.Context) error {
	if w.repo == nil {
		return errors.New("jobs: nil repository")
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		job, err := w.repo.Claim(ctx, w.config.Now())
		if err == nil {
			w.runJob(ctx, job)
			continue
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			if ctx.Err() != nil {
				return ctx.Err()
			}
		}
		if !w.wait(ctx, w.config.PollInterval) {
			return ctx.Err()
		}
	}
}

func (w *Worker) runJob(parent context.Context, job domain.Job) {
	handler, ok := w.handlers[job.Kind]
	if !ok {
		_ = w.repo.Finish(parent, job, "failed", "UNSUPPORTED_JOB", w.config.Now())
		return
	}

	handlerCtx, cancel := context.WithCancel(parent)
	finished := make(chan error, 1)
	go func() { finished <- handler(handlerCtx, job) }()

	leaseLost := make(chan struct{})
	monitorDone := make(chan struct{})
	go func() {
		defer close(monitorDone)
		ticker := time.NewTicker(w.config.RenewInterval)
		defer ticker.Stop()
		for {
			select {
			case <-handlerCtx.Done():
				return
			case <-ticker.C:
				if err := w.repo.Renew(handlerCtx, job, w.config.Now()); err != nil {
					close(leaseLost)
					cancel()
					return
				}
			}
		}
	}()

	var handlerErr error
	select {
	case handlerErr = <-finished:
	case <-parent.Done():
		cancel()
		handlerErr = <-finished
	case <-leaseLost:
		cancel()
		handlerErr = <-finished
	}
	cancel()
	<-monitorDone

	// Shutdown leaves the lease to expire so another process can safely recover
	// it; explicit cancellation is persisted when the store reports it on claim.
	if parent.Err() != nil || isLeaseLost(leaseLost) {
		return
	}

	state, code, available := w.outcome(job, handlerErr)
	_ = w.repo.Finish(parent, job, state, code, available)
}

func isLeaseLost(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func (w *Worker) outcome(job domain.Job, err error) (string, string, time.Time) {
	now := w.config.Now()
	if err == nil {
		return "succeeded", "", now
	}
	var classified *JobError
	if !errors.As(err, &classified) {
		return "retry_wait", "HANDLER_ERROR", now.Add(w.retryDelay(job.Attempt, 0))
	}
	code := classified.Code
	if code == "" {
		switch classified.Class {
		case Retry:
			code = "RETRYABLE_ERROR"
		case PausedAuth:
			code = "AUTH_REQUIRED"
		case Failed:
			code = "JOB_FAILED"
		default:
			return "retry_wait", "HANDLER_ERROR", now.Add(w.retryDelay(job.Attempt, 0))
		}
	}
	switch classified.Class {
	case PausedAuth:
		return "paused_auth", code, now
	case Failed:
		return "failed", code, now
	case Retry:
		return "retry_wait", code, now.Add(w.retryDelay(job.Attempt, classified.RetryAfter))
	default:
		return "retry_wait", "HANDLER_ERROR", now.Add(w.retryDelay(job.Attempt, 0))
	}
}

func (w *Worker) retryDelay(attempt int, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 {
		return retryAfter
	}
	delay := defaultRetryBase
	for i := 1; i < attempt && delay < maxRetryDelay; i++ {
		delay *= 2
	}
	if delay > maxRetryDelay {
		delay = maxRetryDelay
	}
	if w.config.Jitter != nil {
		if jittered := w.config.Jitter(delay); jittered > 0 {
			return jittered
		}
		return time.Millisecond
	}
	w.rngMu.Lock()
	defer w.rngMu.Unlock()
	// Uniform +/- 20% jitter. The cap applies after jitter as well.
	factor := 0.8 + rand.Float64()*0.4
	jittered := time.Duration(float64(delay) * factor)
	if jittered > maxRetryDelay {
		return maxRetryDelay
	}
	return jittered
}

func (w *Worker) wait(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
