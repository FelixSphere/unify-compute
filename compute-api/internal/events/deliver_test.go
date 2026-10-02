package events

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/FelixSphere/unify-compute/compute-api/internal/store"
	"github.com/FelixSphere/unify-compute/compute-api/internal/store/storetest"
)

type received struct {
	header http.Header
	body   []byte
}

// receiver answers with statuses in order, repeating the last one, and keeps
// every request it saw.
type receiver struct {
	mu       sync.Mutex
	statuses []int
	got      []received
}

func (rc *receiver) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	rc.mu.Lock()
	rc.got = append(rc.got, received{header: r.Header.Clone(), body: body})
	status := rc.statuses[min(len(rc.got), len(rc.statuses))-1]
	rc.mu.Unlock()
	w.WriteHeader(status)
}

func (rc *receiver) requests() []received {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return append([]received(nil), rc.got...)
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

type fixture struct {
	st     *store.Store
	w      *Worker
	clk    *clock
	secret string
	job    store.Job
}

const hookPath = "/hooks/compute"

func newFixture(t *testing.T, url string) *fixture {
	t.Helper()
	st := storetest.New(t)
	ctx := context.Background()
	key := int64(7)
	job, _, err := st.Create(ctx, store.NewJob{Owner: "owner-a", TokenID: &key, SKU: "a10g-24gb-x1",
		Spec: json.RawMessage(`{"max_runtime_seconds":3600}`)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	secret := NewSecret()
	if _, err := st.CreateWebhook(ctx, store.NewWebhook{Owner: "owner-a", TokenID: &key, URL: url,
		Secret: secret, EventTypes: TypeStrings(DefaultWebhookTypes)}); err != nil {
		t.Fatal(err)
	}
	// Deliveries come due at the event's database timestamp; start the fake
	// clock safely after it.
	clk := &clock{t: time.Now().Add(time.Minute).Truncate(time.Second)}
	w := NewWorker(st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	w.Now = clk.now
	return &fixture{st: st, w: w, clk: clk, secret: secret, job: job}
}

func (f *fixture) run(t *testing.T) int {
	t.Helper()
	n, err := f.w.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	return n
}

func (f *fixture) delivery(t *testing.T, eventID string) store.DeliveryState {
	t.Helper()
	ds, err := f.st.DeliveriesOf(context.Background(), eventID)
	if err != nil || len(ds) != 1 {
		t.Fatalf("deliveries of %s: %+v %v", eventID, ds, err)
	}
	return ds[0]
}

// verify recomputes the signature the way a receiver following the spec
// would, without going through console.Sign, so a change to either side of
// the canonical string shows up here.
func verify(t *testing.T, secret string, r received) {
	t.Helper()
	ts := r.header.Get("X-Compute-Timestamp")
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("POST\n" + hookPath + "\n" + ts + "\n"))
	mac.Write(r.body)
	want := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(r.header.Get("X-Compute-Signature"))) {
		t.Errorf("signature %q does not verify (want %q)", r.header.Get("X-Compute-Signature"), want)
	}
	mac = hmac.New(sha256.New, []byte("whsec_not-the-secret-0123456789abcdef"))
	mac.Write([]byte("POST\n" + hookPath + "\n" + ts + "\n"))
	mac.Write(r.body)
	if hex.EncodeToString(mac.Sum(nil)) == r.header.Get("X-Compute-Signature") {
		t.Error("signature verifies under the wrong secret")
	}
}

func TestDeliveryIsSignedWithTheBridgeScheme(t *testing.T) {
	rc := &receiver{statuses: []int{204}}
	srv := httptest.NewServer(rc)
	defer srv.Close()
	f := newFixture(t, srv.URL+hookPath)

	ev, err := Append(context.Background(), f.st, f.job.ID, JobFailed,
		FailedData{Error: store.JobError{Code: "image_pull_failed", Message: "manifest unknown"}, Retryable: false})
	if err != nil {
		t.Fatal(err)
	}
	if n := f.run(t); n != 1 {
		t.Fatalf("attempted %d, want 1", n)
	}
	got := rc.requests()
	if len(got) != 1 {
		t.Fatalf("receiver saw %d requests", len(got))
	}
	r := got[0]
	verify(t, f.secret, r)
	if ts := r.header.Get("X-Compute-Timestamp"); ts != strconv.FormatInt(f.clk.t.Unix(), 10) {
		t.Errorf("timestamp %s, want the worker clock %d", ts, f.clk.t.Unix())
	}
	for h, want := range map[string]string{
		"Content-Type":               "application/json",
		"X-Compute-Event-Id":         ev.ID,
		"X-Compute-Event-Type":       "job.failed",
		"X-Compute-Delivery-Attempt": "1",
	} {
		if r.header.Get(h) != want {
			t.Errorf("%s = %q, want %q", h, r.header.Get(h), want)
		}
	}

	var obj struct {
		ID, Object, Type string
		CreatedAt        int64 `json:"created_at"`
		Data             struct {
			Error     store.JobError
			Retryable *bool
		}
	}
	if err := json.Unmarshal(r.body, &obj); err != nil {
		t.Fatal(err)
	}
	if obj.ID != ev.ID || obj.Object != "compute.event" || obj.Type != "job.failed" ||
		obj.CreatedAt != ev.CreatedAt.Unix() || obj.Data.Error.Code != "image_pull_failed" ||
		obj.Data.Retryable == nil || *obj.Data.Retryable {
		t.Errorf("body %s", r.body)
	}
	var withJob map[string]any
	_ = json.Unmarshal(r.body, &withJob)
	if withJob["job_id"] != f.job.ID {
		t.Errorf("job_id %v, want %s", withJob["job_id"], f.job.ID)
	}

	if d := f.delivery(t, ev.ID); d.Status != "delivered" || d.Attempts != 1 || d.LastStatus == nil || *d.LastStatus != 204 {
		t.Errorf("delivery %+v", d)
	}
	f.clk.advance(24 * time.Hour)
	if n := f.run(t); n != 0 || len(rc.requests()) != 1 {
		t.Fatalf("a delivered event was sent again (%d attempted)", n)
	}
}

// The core at-least-once case: the receiver fails, then succeeds. It sees
// the event exactly once more per failure, always with the same id and body,
// and nothing after the success.
func TestRetryWithBackoffKeepsTheEventID(t *testing.T) {
	rc := &receiver{statuses: []int{500, 503, 200}}
	srv := httptest.NewServer(rc)
	defer srv.Close()
	f := newFixture(t, srv.URL+hookPath)
	ev, err := Append(context.Background(), f.st, f.job.ID, JobSucceeded, nil)
	if err != nil {
		t.Fatal(err)
	}

	f.run(t)
	if d := f.delivery(t, ev.ID); d.Status != "pending" || d.Attempts != 1 || *d.LastStatus != 500 ||
		!d.NextAttemptAt.Equal(f.clk.t.Add(time.Minute)) {
		t.Fatalf("after attempt 1: %+v", d)
	}
	f.clk.advance(59 * time.Second)
	if n := f.run(t); n != 0 {
		t.Fatal("retried a second before the backoff elapsed")
	}
	f.clk.advance(time.Second)
	f.run(t)
	if d := f.delivery(t, ev.ID); d.Status != "pending" || d.Attempts != 2 ||
		!d.NextAttemptAt.Equal(f.clk.t.Add(2*time.Minute)) {
		t.Fatalf("after attempt 2: %+v", d)
	}
	f.clk.advance(2 * time.Minute)
	f.run(t)
	if d := f.delivery(t, ev.ID); d.Status != "delivered" || d.Attempts != 3 || d.DeliveredAt == nil {
		t.Fatalf("after attempt 3: %+v", d)
	}
	f.clk.advance(7 * 24 * time.Hour)
	f.run(t)

	got := rc.requests()
	if len(got) != 3 {
		t.Fatalf("receiver saw %d requests, want 3", len(got))
	}
	for i, r := range got {
		if r.header.Get("X-Compute-Event-Id") != ev.ID {
			t.Errorf("attempt %d event id %q, want %q", i+1, r.header.Get("X-Compute-Event-Id"), ev.ID)
		}
		if r.header.Get("X-Compute-Delivery-Attempt") != strconv.Itoa(i+1) {
			t.Errorf("attempt header %q on attempt %d", r.header.Get("X-Compute-Delivery-Attempt"), i+1)
		}
		if string(r.body) != string(got[0].body) {
			t.Errorf("attempt %d body differs:\n%s\n%s", i+1, r.body, got[0].body)
		}
		verify(t, f.secret, r)
	}
	if got[0].header.Get("X-Compute-Timestamp") == got[2].header.Get("X-Compute-Timestamp") {
		t.Error("retries reuse the first attempt's timestamp; each attempt must be signed fresh")
	}
}

func TestGiveUpAfterMaxAttempts(t *testing.T) {
	rc := &receiver{statuses: []int{500}}
	srv := httptest.NewServer(rc)
	defer srv.Close()
	f := newFixture(t, srv.URL+hookPath)
	ev, err := Append(context.Background(), f.st, f.job.ID, JobCancelled, CancelledData{Reason: "customer"})
	if err != nil {
		t.Fatal(err)
	}

	for attempt := 1; attempt <= MaxAttempts; attempt++ {
		if n := f.run(t); n != 1 {
			t.Fatalf("attempt %d: worker attempted %d", attempt, n)
		}
		d := f.delivery(t, ev.ID)
		if d.Attempts != attempt {
			t.Fatalf("attempts = %d, want %d", d.Attempts, attempt)
		}
		if attempt < MaxAttempts {
			if d.Status != "pending" {
				t.Fatalf("gave up after %d attempts, want %d", attempt, MaxAttempts)
			}
			f.clk.advance(Backoff(attempt))
		} else if d.Status != "failed" || *d.LastStatus != 500 || d.LastError == nil {
			t.Fatalf("after the last attempt: %+v", d)
		}
	}
	f.clk.advance(30 * 24 * time.Hour)
	if n := f.run(t); n != 0 {
		t.Fatalf("a given-up delivery was attempted again")
	}
	if got := len(rc.requests()); got != MaxAttempts {
		t.Fatalf("receiver saw %d requests, want %d", got, MaxAttempts)
	}
}

func TestRedirectIsAFailedAttemptAndNotFollowed(t *testing.T) {
	var followed int
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { followed++ }))
	defer elsewhere.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL, http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	f := newFixture(t, srv.URL+hookPath)
	ev, err := Append(context.Background(), f.st, f.job.ID, JobSucceeded, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.run(t)
	if followed != 0 {
		t.Fatal("the worker followed a redirect with a signed body")
	}
	if d := f.delivery(t, ev.ID); d.Status != "pending" || *d.LastStatus != http.StatusTemporaryRedirect {
		t.Fatalf("delivery %+v", d)
	}
}

func TestUnreachableReceiverIsRetried(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL + hookPath
	srv.Close()
	f := newFixture(t, url)
	ev, err := Append(context.Background(), f.st, f.job.ID, JobSucceeded, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.run(t)
	if d := f.delivery(t, ev.ID); d.Status != "pending" || d.LastStatus != nil || d.LastError == nil || d.Attempts != 1 {
		t.Fatalf("delivery %+v", d)
	}
}

func TestNonTerminalEventsAreNotDeliveredByDefault(t *testing.T) {
	rc := &receiver{statuses: []int{200}}
	srv := httptest.NewServer(rc)
	defer srv.Close()
	f := newFixture(t, srv.URL+hookPath)
	ctx := context.Background()
	if _, err := Append(ctx, f.st, f.job.ID, JobQueued, QueuedData{SKU: "a10g-24gb-x1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Append(ctx, f.st, f.job.ID, JobFailover, FailoverData{From: "aws", To: "gcp"}); err != nil {
		t.Fatal(err)
	}
	if n := f.run(t); n != 0 || len(rc.requests()) != 0 {
		t.Fatalf("non-terminal events delivered: %d", n)
	}
}

func TestAppendRefusesInvalidEvents(t *testing.T) {
	f := newFixture(t, "https://hooks.example"+hookPath)
	ctx := context.Background()
	if _, err := Append(ctx, f.st, f.job.ID, JobFailed, FailedData{}); err == nil {
		t.Error("job.failed without error.code was appended")
	}
	if _, err := Append(ctx, f.st, "job_00000000000000000000000000", JobSucceeded, nil); err != store.ErrNotFound {
		t.Errorf("event for a missing job: %v, want ErrNotFound", err)
	}
	page, err := f.st.ListEvents(ctx, "owner-a", f.job.ID, "", 0)
	if err != nil || len(page.Events) != 0 {
		t.Fatalf("refused events were written: %+v %v", page, err)
	}
}
