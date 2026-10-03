package backend

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/FelixSphere/unify-compute/compute-api/internal/backend/skypilottest"
)

// These tests replay responses recorded from a real SkyPilot 0.13.0 API
// server (commit b1431e5, no cloud credentials) to pin the client's decoding
// to the real wire format rather than to the fake's idea of it.

func recorded(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/skypilot-0.13.0/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func replay(t *testing.T, status int, name string) *Client {
	t.Helper()
	body := recorded(t, name)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return &Client{BaseURL: srv.URL, UserHash: "unifycmp"}
}

func TestRecordedLaunchFailureDecodes(t *testing.T) {
	err := replay(t, 500, "get_launch_failed.json").Get(context.Background(), "86d8da4e", nil)
	var re *RequestError
	if !errors.As(err, &re) {
		t.Fatalf("err = %v, want *RequestError", err)
	}
	if re.Type != "NoCloudAccessError" || re.RequestID != "86d8da4e-22d6-4d25-9eb7-157bc87cf563" {
		t.Fatalf("%+v", re)
	}
	if strings.Contains(re.Message, "\x1b") || !strings.Contains(re.Message, "Cloud access is not set up") {
		t.Fatalf("message not cleaned: %q", re.Message)
	}
	f := classifyLaunchError(err, false)
	if f.Code != CodeProvisionFailed || !f.Retryable {
		t.Fatalf("no credentials is our deployment's fault and retryable: %+v", f)
	}
}

func TestRecordedStatusOfMissingClusterIsEmpty(t *testing.T) {
	var recs []ClusterRecord
	if err := replay(t, 200, "get_status_empty.json").Get(context.Background(), "x", &recs); err != nil {
		t.Fatal(err)
	}
	if recs == nil || len(recs) != 0 {
		t.Fatalf("recs = %#v, want empty non-nil list", recs)
	}
}

func TestRecordedQueueAndDownOfMissingCluster(t *testing.T) {
	for _, name := range []string{"get_queue_cluster_missing.json", "get_down_cluster_missing.json"} {
		err := replay(t, 500, name).Get(context.Background(), "x", nil)
		var re *RequestError
		if !errors.As(err, &re) || re.Type != "ClusterDoesNotExist" {
			t.Fatalf("%s: err = %v", name, err)
		}
		if re.Message != "Cluster 'uc-01j8q6zk3r7y9w2x4v6t8s0a1b-1' does not exist." {
			t.Fatalf("%s: message %q", name, re.Message)
		}
	}
}

func TestRecordedRequestStates(t *testing.T) {
	c := replay(t, 200, "api_status_launch_pending.json")
	ri, err := c.RequestState(context.Background(), "86d8da4e-22d6-4d25-9eb7-157bc87cf563")
	if err != nil {
		t.Fatal(err)
	}
	if ri.Status != RequestPending || ri.Name != "sky.launch" || ri.Cluster != "uc-01j8q6zk3r7y9w2x4v6t8s0a1b-1" || ri.FinishedAt != nil {
		t.Fatalf("%+v", ri)
	}
	ri, err = replay(t, 200, "api_status_launch_failed.json").RequestState(context.Background(), "86d8da4e-22d6-4d25-9eb7-157bc87cf563")
	if err != nil || ri.Status != RequestFailed || ri.FinishedAt == nil {
		t.Fatalf("%+v %v", ri, err)
	}
}

// The recording holds three launches of one cluster (two refused tasks while
// this backend was being written, then the accepted one); the newest wins.
func TestRecordedLaunchRequestForPicksNewest(t *testing.T) {
	ri, ok, err := replay(t, 200, "api_status_by_cluster.json").LaunchRequestFor(context.Background(), "uc-01j8q6zk3r7y9w2x4v6t8s0a1b-1")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if ri.ID != "86d8da4e-22d6-4d25-9eb7-157bc87cf563" {
		t.Fatalf("picked %s", ri.ID)
	}
}

func keysOf(t *testing.T, raw []byte) []string {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// The fake must answer with the same fields the real server does, or tests
// against it prove nothing about the real wire.
func TestFakeMatchesRecordedShapes(t *testing.T) {
	fake := skypilottest.New()
	defer fake.Close()
	c := &Client{BaseURL: fake.URL, UserHash: "unifycmp"}
	ctx := context.Background()
	id, err := c.Down(ctx, "uc-missing-1")
	if err != nil {
		t.Fatal(err)
	}

	get := func(path string) (int, []byte) {
		resp, err := c.do(ctx, http.MethodGet, path, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var raw json.RawMessage
		_ = json.NewDecoder(resp.Body).Decode(&raw)
		return resp.StatusCode, raw
	}

	code, raw := get("/api/get?request_id=" + id)
	if code != 500 {
		t.Fatalf("fake /api/get of failed request: %d", code)
	}
	var fakeDetail, realDetail struct{ Detail json.RawMessage }
	_ = json.Unmarshal(raw, &fakeDetail)
	_ = json.Unmarshal(recorded(t, "get_down_cluster_missing.json"), &realDetail)
	if f, r := keysOf(t, fakeDetail.Detail), keysOf(t, realDetail.Detail); !reflect.DeepEqual(f, r) {
		t.Fatalf("/api/get detail fields\nfake %v\nreal %v", f, r)
	}

	_, raw = get("/api/status?all_status=true&request_ids=" + id)
	var fakeList, realList []json.RawMessage
	_ = json.Unmarshal(raw, &fakeList)
	_ = json.Unmarshal(recorded(t, "api_status_launch_failed.json"), &realList)
	if f, r := keysOf(t, fakeList[0]), keysOf(t, realList[0]); !reflect.DeepEqual(f, r) {
		t.Fatalf("/api/status fields\nfake %v\nreal %v", f, r)
	}
}
