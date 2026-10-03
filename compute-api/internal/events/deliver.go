package events

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/FelixSphere/unify-compute/compute-api/internal/console"
	"github.com/FelixSphere/unify-compute/compute-api/internal/store"
)

const (
	HeaderEventID   = "X-Compute-Event-Id"
	HeaderEventType = "X-Compute-Event-Type"
	HeaderWebhookID = "X-Compute-Webhook-Id"
	HeaderAttempt   = "X-Compute-Delivery-Attempt"

	// MaxAttempts is the give-up rule: after this many failed attempts, about
	// 20.5 hours of backoff, a delivery is marked failed.
	MaxAttempts = 12

	AttemptTimeout = 10 * time.Second

	backoffBase = time.Minute
	backoffCap  = 6 * time.Hour
	// The lease outlives the attempt timeout so a slow but successful attempt
	// is recorded before anyone else may claim the delivery.
	lease = AttemptTimeout + 50*time.Second
)

// Backoff is the wait after failed attempt n (1-based): 1 min doubling, capped
// at 6 h.
func Backoff(n int) time.Duration {
	d := backoffBase
	for i := 1; i < n && d < backoffCap; i++ {
		d *= 2
	}
	return min(d, backoffCap)
}

// Worker sends due webhook deliveries. Several workers may share one
// database; claims do not overlap.
type Worker struct {
	Store *store.Store
	HTTP  *http.Client
	Now   func() time.Time
	Log   *slog.Logger
	Batch int
}

func NewWorker(st *store.Store, log *slog.Logger) *Worker {
	return &Worker{
		Store: st,
		HTTP: &http.Client{
			Timeout: AttemptTimeout,
			// A redirect is a failed attempt: following one would send a
			// signed body somewhere the customer did not register.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		Now:   time.Now,
		Log:   log,
		Batch: 50,
	}
}

// Run calls RunOnce every interval until ctx ends.
func (w *Worker) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if _, err := w.RunOnce(ctx); err != nil && ctx.Err() == nil {
			w.Log.Error("webhook delivery", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// RunOnce claims the deliveries due now, attempts each once, and records the
// outcome. It returns how many it attempted.
func (w *Worker) RunOnce(ctx context.Context) (int, error) {
	now := w.Now()
	ds, err := w.Store.ClaimDeliveries(ctx, now, lease, w.Batch)
	if err != nil {
		return 0, err
	}
	for _, d := range ds {
		if err := w.attempt(ctx, d); err != nil {
			return len(ds), err
		}
	}
	return len(ds), nil
}

func (w *Worker) attempt(ctx context.Context, d store.Delivery) error {
	status, sendErr := w.send(ctx, d)
	now := w.Now()
	if sendErr == nil {
		return w.Store.MarkDelivered(ctx, d.ID, d.Attempt, now, status)
	}
	var retryAt *time.Time
	if d.Attempt < MaxAttempts {
		t := now.Add(Backoff(d.Attempt))
		retryAt = &t
	}
	var st *int
	if status != 0 {
		st = &status
	}
	w.Log.Warn("webhook attempt failed", "webhook", d.WebhookID, "event", d.Event.ID,
		"attempt", d.Attempt, "err", sendErr, "gave_up", retryAt == nil)
	return w.Store.MarkAttemptFailed(ctx, d.ID, d.Attempt, st, sendErr.Error(), retryAt)
}

// send returns the receiver's status (0 without a response) and nil only for
// a 2xx.
func (w *Worker) send(ctx context.Context, d store.Delivery) (int, error) {
	body, err := json.Marshal(ObjectOf(d.Event))
	if err != nil {
		return 0, err
	}
	u, err := url.Parse(d.URL)
	if err != nil {
		return 0, err
	}
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	ts := w.Now().Unix()

	ctx, cancel := context.WithTimeout(ctx, AttemptTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.URL, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "UnifyAI-Compute-Webhooks/1")
	req.Header.Set(HeaderEventID, d.Event.ID)
	req.Header.Set(HeaderEventType, d.Event.Type)
	req.Header.Set(HeaderWebhookID, d.WebhookID)
	req.Header.Set(HeaderAttempt, strconv.Itoa(d.Attempt))
	req.Header.Set(console.HeaderTimestamp, strconv.FormatInt(ts, 10))
	req.Header.Set(console.HeaderSignature, console.Sign([]byte(d.Secret), http.MethodPost, path, ts, body))

	resp, err := w.HTTP.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return resp.StatusCode, fmt.Errorf("receiver answered %d", resp.StatusCode)
	}
	return resp.StatusCode, nil
}
