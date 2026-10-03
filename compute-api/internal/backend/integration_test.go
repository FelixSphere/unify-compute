package backend

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/FelixSphere/unify-compute/compute-api/internal/store"
)

// TestRealAPIServer runs the client and the backend against a real SkyPilot
// API server. It skips unless SKYPILOT_API_SERVER_URL is set
// (SKYPILOT_SERVICE_ACCOUNT_TOKEN if the server has auth).
//
// It must never provision anything. The read-only half (status, queue, down
// and logs of a cluster that does not exist) runs on any server. The launch
// half runs only if the server reports no enabled cloud, so the launch can
// only fail; on a server with credentials it is skipped, because there the
// same call would buy a GPU.
//
// Locally: pip install the pinned fork, HOME=$(mktemp -d) sky api start, then
// SKYPILOT_API_SERVER_URL=http://127.0.0.1:46580 go test ./internal/backend -run RealAPIServer -v
func TestRealAPIServer(t *testing.T) {
	url := os.Getenv("SKYPILOT_API_SERVER_URL")
	if url == "" {
		t.Skip("SKYPILOT_API_SERVER_URL not set; skipping test against a real SkyPilot API server")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	c := &Client{BaseURL: url, Token: os.Getenv("SKYPILOT_SERVICE_ACCOUNT_TOKEN"), UserHash: "unifycmp-it"}

	var suffix [5]byte
	_, _ = rand.Read(suffix[:])
	jobID := "job_" + "01J8Q6ZK3R7Y9W2X4V6T" + hex.EncodeToString(suffix[:])[:6]
	cluster := ClusterName(jobID, 1)

	t.Run("status of a missing cluster is empty", func(t *testing.T) {
		recs, err := c.Status(ctx, cluster)
		if err != nil || len(recs) != 0 {
			t.Fatalf("recs=%v err=%v", recs, err)
		}
	})
	t.Run("queue, down and logs of a missing cluster are ClusterDoesNotExist", func(t *testing.T) {
		_, qerr := c.Queue(ctx, cluster)
		id, err := c.Down(ctx, cluster)
		if err != nil {
			t.Fatal(err)
		}
		derr := c.Get(ctx, id, nil)
		lerr := c.Logs(ctx, cluster, nil, io.Discard)
		for name, err := range map[string]error{"queue": qerr, "down": derr, "logs": lerr} {
			var re *RequestError
			if !errors.As(err, &re) || re.Type != "ClusterDoesNotExist" {
				t.Errorf("%s: %v", name, err)
			}
		}
	})

	t.Run("launch with no cloud enabled fails from provisioning", func(t *testing.T) {
		resp, err := c.do(ctx, http.MethodGet, "/enabled_clouds", nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		id := resp.Header.Get(requestIDHdr)
		resp.Body.Close()
		var clouds []string
		if err := c.Get(ctx, id, &clouds); err != nil {
			t.Fatal(err)
		}
		if len(clouds) != 0 {
			t.Skipf("server has clouds enabled (%v); a launch here would provision for real", clouds)
		}

		h := &harness{t: t, store: newMemStore(jobID), sink: &recSink{}}
		j := exampleJob()
		j.ID = jobID
		b := New(c, h.store, h.sink, Config{Logf: t.Logf})
		if err := b.Launch(ctx, j); err != nil {
			t.Fatal(err)
		}
		for {
			if err := b.Step(ctx, jobID); err != nil && !errors.Is(err, ErrUnknownJob) {
				t.Logf("step: %v", err)
			}
			if _, err := b.Status(jobID); errors.Is(err, ErrUnknownJob) {
				break
			}
			select {
			case <-ctx.Done():
				t.Fatal("attempt did not finish")
			case <-time.After(time.Second):
			}
		}
		st, jobErr := h.store.get(jobID)
		if st != store.StatusFailed || jobErr == nil || jobErr.Code != CodeProvisionFailed {
			t.Fatalf("status %s error %+v", st, jobErr)
		}
		h.job = j
		h.wantKinds(EventProvisioning, EventEnded, EventClusterDown)
		if end, _ := h.sink.last(EventEnded); end.UpAt != nil || !end.Failure.Retryable {
			t.Fatalf("%+v", end)
		}
	})
}
