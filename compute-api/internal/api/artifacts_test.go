package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/FelixSphere/unify-compute/compute-api/internal/artifacts"
	"github.com/FelixSphere/unify-compute/compute-api/internal/awsv4"
	"github.com/FelixSphere/unify-compute/compute-api/internal/awsv4/awsv4test"
	"github.com/FelixSphere/unify-compute/compute-api/internal/backend"
	"github.com/FelixSphere/unify-compute/compute-api/internal/store"
)

const artJob = "job_01J8Q6ZK3R7Y9W2X4V6T8S0A1B"

type memJobs struct {
	mu   sync.Mutex
	jobs map[string]store.Job
}

func (m *memJobs) Get(_ context.Context, owner, id string) (store.Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok || j.Owner != owner {
		return store.Job{}, store.ErrNotFound
	}
	return j, nil
}

func (m *memJobs) setStatus(id string, st store.Status) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j := m.jobs[id]
	j.Status = st
	m.jobs[id] = j
}

type artifactsHarness struct {
	srv    *Server
	http   *httptest.Server
	fake   *awsv4test.Fake
	art    *artifacts.Service
	jobs   *memJobs
	mount  string
	spec   backend.Spec
	public string
}

func newArtifactsHarness(t *testing.T) *artifactsHarness {
	t.Helper()
	h := &artifactsHarness{srv: newTestServer(t, "aws")}
	// Started only once srv is fully configured: setting its fields while the
	// server goroutines can read them is a data race. The public URL is known
	// before Start because the listener is already bound.
	h.http = httptest.NewUnstartedServer(h.srv)
	t.Cleanup(h.http.Close)
	h.fake = awsv4test.New(t, "unify-compute-staging-artifacts-123456789012")
	art, err := artifacts.New(artifacts.Config{
		Bucket: h.fake.Bucket, WriterRoleARN: "arn:aws:iam::123456789012:role/unify-compute-staging-artifacts-writer",
		PublicURL: "http://" + h.http.Listener.Addr().String(), TokenSecret: []byte(strings.Repeat("k", 32)),
		Creds: awsv4.Static(h.fake.Instance), Client: h.fake.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	h.art = art
	h.mount = filepath.Join(t.TempDir(), "out")
	h.spec = backend.Spec{Image: "vllm/vllm-openai:v0.6.0", Command: []string{"true"}, Outputs: []backend.Output{{Mount: h.mount}}, MaxRuntimeSeconds: 3600}
	raw, _ := json.Marshal(h.spec)
	h.jobs = &memJobs{jobs: map[string]store.Job{artJob: {ID: artJob, Owner: "owner-a", Status: store.StatusRunning, Spec: raw}}}
	h.srv.Jobs, h.srv.Artifacts = h.jobs, art
	h.srv.Owner = func(_ context.Context, key string) (string, error) {
		switch key {
		case "sk-owner-a":
			return "owner-a", nil
		case "sk-owner-b":
			return "owner-b", nil
		}
		return "", ErrKeyRejected
	}
	h.http.Start()
	return h
}

type artifactList struct {
	Object   string         `json:"object"`
	Data     []artifactView `json:"data"`
	Complete bool           `json:"complete"`
}

func (h *artifactsHarness) list(t *testing.T, key string) (int, artifactList) {
	t.Helper()
	rec := do(t, h.srv, http.MethodGet, "/compute/v1/jobs/"+artJob+"/artifacts", key, "")
	var l artifactList
	_ = json.Unmarshal(rec.Body.Bytes(), &l)
	return rec.Code, l
}

func TestArtifactRoutesAreNotImplementedWithoutAStore(t *testing.T) {
	s := newTestServer(t, "aws")
	if rec := do(t, s, http.MethodGet, "/compute/v1/jobs/"+artJob+"/artifacts", "", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("without key: %d", rec.Code)
	}
	for _, p := range []string{"/compute/v1/internal/jobs/" + artJob + "/artifacts/uploads", "/compute/v1/internal/jobs/" + artJob + "/artifacts/complete"} {
		if rec := do(t, s, http.MethodPost, p, "", ""); rec.Code != http.StatusNotImplemented {
			t.Errorf("%s: %d", p, rec.Code)
		}
	}
}

func TestArtifactListIsOwnerScoped(t *testing.T) {
	h := newArtifactsHarness(t)
	if code, _ := h.list(t, "sk-owner-b"); code != http.StatusNotFound {
		t.Fatalf("other owner: %d", code)
	}
	if code, _ := h.list(t, "sk-revoked"); code != http.StatusUnauthorized {
		t.Fatalf("rejected key: %d", code)
	}
	code, l := h.list(t, "sk-owner-a")
	if code != http.StatusOK || l.Object != "list" || l.Complete || len(l.Data) != 0 {
		t.Fatalf("before upload: %d %+v", code, l)
	}
}

func (h *artifactsHarness) post(t *testing.T, path, token string, form url.Values) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/compute/v1/internal/jobs/"+artJob+"/artifacts/"+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.srv.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func TestUploadRoutesRefuseBadTokensPathsAndEndedJobs(t *testing.T) {
	h := newArtifactsHarness(t)
	envs, _, err := h.art.OutputStep(backend.Job{ID: artJob, Owner: "owner-a", Spec: h.spec}, "true")
	if err != nil {
		t.Fatal(err)
	}
	tok := envs[artifacts.EnvToken]
	sum := sha256.Sum256(nil)
	form := func(p string) url.Values {
		return url.Values{"path": {p}, "size": {"0"}, "sha256": {hex.EncodeToString(sum[:])}}
	}
	if code, _ := h.post(t, "uploads", "", form(h.mount+"/a")); code != http.StatusUnauthorized {
		t.Errorf("no token: %d", code)
	}
	if code, _ := h.post(t, "uploads", "sk-owner-a", form(h.mount+"/a")); code != http.StatusUnauthorized {
		t.Errorf("a customer key is not an upload token: %d", code)
	}
	if code, body := h.post(t, "uploads", tok, form("/etc/passwd")); code != http.StatusBadRequest || !strings.Contains(body, "invalid_path") {
		t.Errorf("outside the mount: %d %s", code, body)
	}
	if code, body := h.post(t, "uploads", tok, url.Values{"path": {h.mount + "/big"}, "size": {"5368709121"}, "sha256": {hex.EncodeToString(sum[:])}}); code != http.StatusRequestEntityTooLarge {
		t.Errorf("too large: %d %s", code, body)
	}
	code, body := h.post(t, "uploads", tok, form(h.mount+"/a"))
	if code != http.StatusOK || len(strings.Fields(body)) != 2 {
		t.Fatalf("upload url: %d %q", code, body)
	}
	h.jobs.setStatus(artJob, store.StatusCancelled)
	if code, body := h.post(t, "uploads", tok, form(h.mount+"/a")); code != http.StatusConflict || !strings.Contains(body, "job_terminal") {
		t.Errorf("after the job ended: %d %s", code, body)
	}
	if code, _ := h.post(t, "complete", tok, nil); code != http.StatusConflict {
		t.Errorf("complete after the job ended: %d", code)
	}
}

// The real run script, run by bash against the real routes and the fake
// S3: the command fails, its outputs are still uploaded, the manifest is
// written, the step keeps the command's exit status, and the customer can
// download exactly what was written.
func TestRunScriptUploadsOutputsEvenWhenTheCommandFails(t *testing.T) {
	for _, tool := range []string{"bash", "curl", "sha256sum", "find"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not available", tool)
		}
	}
	h := newArtifactsHarness(t)
	cmd := `printf 'line 1\n' > "$OUT/results.jsonl"; mkdir -p "$OUT/sub dir"; printf '\001\002' > "$OUT/sub dir/it's.bin"; ln -s /etc/hosts "$OUT/link"; echo working; exit 3`
	envs, run, err := h.art.OutputStep(backend.Job{ID: artJob, Owner: "owner-a", Spec: h.spec}, "bash -c "+shq(cmd))
	if err != nil {
		t.Fatal(err)
	}
	c := exec.Command("bash", "-c", run)
	c.Env = append(os.Environ(), "OUT="+h.mount)
	for k, v := range envs {
		c.Env = append(c.Env, k+"="+v)
	}
	out, err := c.CombinedOutput()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 3 {
		t.Fatalf("exit: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "working") || !strings.Contains(string(out), "2 output file(s) processed, manifest written\n") {
		t.Fatalf("output:\n%s", out)
	}
	want := map[string]string{h.mount + "/results.jsonl": "line 1\n", h.mount + "/sub dir/it's.bin": "\x01\x02"}
	keys := h.fake.Keys()
	if len(keys) != 3 {
		t.Fatalf("objects %v", keys)
	}
	for p, body := range want {
		o, ok := h.fake.Object("jobs/" + artJob + p)
		if !ok || string(o.Body) != body || o.SHA256B64 == "" {
			t.Errorf("%s: %+v %v", p, o, ok)
		}
	}

	code, l := h.list(t, "sk-owner-a")
	if code != http.StatusOK || !l.Complete || len(l.Data) != 2 {
		t.Fatalf("list: %d %+v", code, l)
	}
	for _, a := range l.Data {
		resp, err := h.fake.Server.Client().Get(a.URL)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		sum := sha256.Sum256(b)
		if resp.StatusCode != 200 || string(b) != want[a.Path] || a.SHA256 != hex.EncodeToString(sum[:]) || a.Size != int64(len(b)) || a.Object != "compute.artifact" {
			t.Errorf("%+v: %d %q", a, resp.StatusCode, b)
		}
	}
}

// A successful command exits 0 through the trap, and a job that wrote
// nothing still gets an (empty) manifest, which is how "no outputs" differs
// from "the upload never ran".
func TestRunScriptWithNoOutputsWritesAnEmptyManifest(t *testing.T) {
	for _, tool := range []string{"bash", "curl", "sha256sum", "find"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not available", tool)
		}
	}
	h := newArtifactsHarness(t)
	envs, run, err := h.art.OutputStep(backend.Job{ID: artJob, Owner: "owner-a", Spec: h.spec}, "true")
	if err != nil {
		t.Fatal(err)
	}
	c := exec.Command("bash", "-c", run)
	for k, v := range envs {
		c.Env = append(c.Env, k+"="+v)
	}
	c.Env = append(c.Env, "PATH="+os.Getenv("PATH"))
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if fi, err := os.Stat(h.mount); err != nil || !fi.IsDir() {
		t.Fatalf("the mount was not created: %v", err)
	}
	code, l := h.list(t, "sk-owner-a")
	if code != http.StatusOK || !l.Complete || len(l.Data) != 0 {
		t.Fatalf("%d %+v", code, l)
	}
}

func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'" }
