package artifacts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/FelixSphere/unify-compute/compute-api/internal/awsv4"
	"github.com/FelixSphere/unify-compute/compute-api/internal/awsv4/awsv4test"
	"github.com/FelixSphere/unify-compute/compute-api/internal/backend"
)

const (
	jobA      = "job_01J8Q6ZK3R7Y9W2X4V6T8S0A1B"
	jobB      = "job_01J8Q6ZK3R7Y9W2X4V6T8S0A1C"
	writerARN = "arn:aws:iam::123456789012:role/unify-compute-staging-artifacts-writer"
)

var secret = []byte("0123456789abcdef0123456789abcdef")

func newService(t *testing.T) (*Service, *awsv4test.Fake) {
	t.Helper()
	f := awsv4test.New(t, "unify-compute-staging-artifacts-123456789012")
	s, err := New(Config{
		Bucket: f.Bucket, WriterRoleARN: writerARN, PublicURL: "https://staging.compute.unifyai.us",
		TokenSecret: secret, Creds: awsv4.Static(f.Instance), Client: f.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return s, f
}

func hexSum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func TestTokenIsBoundToJobOwnerAndExpiry(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	tok := mintToken(secret, "owner-a", jobA, now.Add(time.Hour))
	if owner, err := verifyToken(secret, jobA, tok, now); err != nil || owner != "owner-a" {
		t.Fatalf("%q %v", owner, err)
	}
	bad := map[string]func() (string, error){
		"other job":    func() (string, error) { return verifyToken(secret, jobB, tok, now) },
		"expired":      func() (string, error) { return verifyToken(secret, jobA, tok, now.Add(time.Hour)) },
		"other secret": func() (string, error) { return verifyToken([]byte(strings.Repeat("x", 32)), jobA, tok, now) },
		"tampered": func() (string, error) {
			return verifyToken(secret, jobA, strings.Replace(tok, ".", ".e", 1), now)
		},
		"empty": func() (string, error) { return verifyToken(secret, jobA, "", now) },
	}
	for name, f := range bad {
		if _, err := f(); !errors.Is(err, ErrBadToken) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestOutputMountsRefuseRootRelativeAndOverlap(t *testing.T) {
	for _, outs := range [][]string{{"/"}, {"out"}, {"/mnt/out", "/mnt/out/sub"}, {"/mnt/out/", "/mnt/out"}} {
		var o []backend.Output
		for _, m := range outs {
			o = append(o, backend.Output{Mount: m})
		}
		if _, err := OutputMounts(o); err == nil {
			t.Errorf("%v accepted", outs)
		}
	}
	ms, err := OutputMounts([]backend.Output{{Mount: "/mnt/out/"}, {Mount: "/mnt/outer"}})
	if err != nil || ms[0] != "/mnt/out" || ms[1] != "/mnt/outer" {
		t.Fatalf("%v %v", ms, err)
	}
}

func TestOutputStepEnvAndScript(t *testing.T) {
	s, _ := newService(t)
	now := time.Unix(1_800_000_000, 0)
	s.cfg.Now = func() time.Time { return now }
	j := backend.Job{ID: jobA, Owner: "owner-a", Spec: backend.Spec{Outputs: []backend.Output{{Mount: "/mnt/out"}}, MaxRuntimeSeconds: 3600}}
	envs, run, err := s.OutputStep(j, "python run.py")
	if err != nil {
		t.Fatal(err)
	}
	if envs[EnvURL] != "https://staging.compute.unifyai.us/compute/v1/internal/jobs/"+jobA+"/artifacts" {
		t.Fatalf("url %q", envs[EnvURL])
	}
	owner, err := verifyToken(secret, jobA, envs[EnvToken], now.Add(time.Hour+UploadMargin-time.Second))
	if err != nil || owner != "owner-a" {
		t.Fatalf("token: %v", err)
	}
	if _, err := verifyToken(secret, jobA, envs[EnvToken], now.Add(time.Hour+UploadMargin)); err == nil {
		t.Fatal("token outlives max_runtime + margin")
	}
	if !strings.HasSuffix(run, "trap __uc_upload EXIT\npython run.py\n") || !strings.HasPrefix(run, "mkdir -p -- '/mnt/out'\n") {
		t.Fatalf("run:\n%s", run)
	}
	if _, _, err := s.OutputStep(backend.Job{ID: jobA, Spec: j.Spec}, "x"); !errors.Is(err, ErrNoOwner) {
		t.Fatalf("no owner: %v", err)
	}
}

// The writer session handed to S3 is narrowed to exactly this job's prefix,
// and its URL only writes the file it was signed for with the body it was
// signed for.
func TestPresignUploadIsolation(t *testing.T) {
	s, f := newService(t)
	ctx := context.Background()
	body := []byte(`{"id":1,"text":"ok"}` + "\n")
	up, err := s.PresignUpload(ctx, jobA, []string{"/mnt/out"}, "/mnt/out/results.jsonl", int64(len(body)), hexSum(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Assumes) != 1 {
		t.Fatalf("assumes %+v", f.Assumes)
	}
	a := f.Assumes[0]
	if a.RoleARN != writerARN || a.SessionName != jobA || a.Duration != time.Hour {
		t.Fatalf("assume %+v", a)
	}
	var pol struct {
		Statement []struct {
			Action   []string
			Resource string
		}
	}
	_ = json.Unmarshal([]byte(a.Policy), &pol)
	if len(pol.Statement) != 1 || pol.Statement[0].Resource != "arn:aws:s3:::"+f.Bucket+"/jobs/"+jobA+"/*" {
		t.Fatalf("session policy %s", a.Policy)
	}

	put := func(u, sum string, b []byte) int {
		req, _ := http.NewRequest(http.MethodPut, u, strings.NewReader(string(b)))
		req.Header.Set("X-Amz-Checksum-Sha256", sum)
		resp, err := f.Server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := put(up.URL, up.ChecksumSHA256, []byte("something else")); code == http.StatusOK {
		t.Fatal("a different body was accepted")
	}
	if code := put(strings.Replace(up.URL, jobA, jobB, 1), up.ChecksumSHA256, body); code == http.StatusOK {
		t.Fatal("the URL was reusable for another job's key")
	}
	if code := put(up.URL, up.ChecksumSHA256, body); code != http.StatusOK {
		t.Fatalf("put %d", code)
	}
	if _, ok := f.Object("jobs/" + jobA + "/mnt/out/results.jsonl"); !ok {
		t.Fatalf("keys %v", f.Keys())
	}

	for name, p := range map[string]string{
		"outside the mounts": "/etc/passwd",
		"mount prefix only":  "/mnt/outer/x",
		"dot-dot":            "/mnt/out/../../etc/passwd",
		"relative":           "mnt/out/x",
		"the mount itself":   "/mnt/out",
	} {
		if _, err := s.PresignUpload(ctx, jobA, []string{"/mnt/out"}, p, 1, hexSum(nil)); !errors.Is(err, ErrBadPath) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := s.PresignUpload(ctx, jobA, []string{"/mnt/out"}, "/mnt/out/big", MaxFileSize+1, hexSum(nil)); !errors.Is(err, ErrTooLarge) {
		t.Errorf("too large: %v", err)
	}
	if _, err := s.PresignUpload(ctx, jobA, []string{"/mnt/out"}, "/mnt/out/x", 1, "ABC"); !errors.Is(err, ErrBadSum) {
		t.Errorf("bad sum: %v", err)
	}
	if _, err := s.PresignUpload(ctx, "job_1/../x", []string{"/mnt/out"}, "/mnt/out/x", 1, hexSum(nil)); !errors.Is(err, ErrBadPath) {
		t.Errorf("bad job id: %v", err)
	}
	if len(f.Assumes) != 1 {
		t.Fatalf("session not reused: %d assumes", len(f.Assumes))
	}
}

func TestWriterSessionIsRenewedBeforeItRunsOut(t *testing.T) {
	s, f := newService(t)
	now := time.Now().Truncate(time.Second) // STS expirations have second precision
	s.cfg.Now = func() time.Time { return now }
	f.Now = s.cfg.Now
	ctx := context.Background()
	if _, err := s.PresignUpload(ctx, jobA, []string{"/o"}, "/o/a", 0, hexSum(nil)); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour - sessionMinLeft + time.Second)
	up, err := s.PresignUpload(ctx, jobA, []string{"/o"}, "/o/a", 0, hexSum(nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Assumes) != 2 {
		t.Fatalf("%d assumes", len(f.Assumes))
	}
	if d := up.ExpiresAt.Sub(now); d != URLValidity-time.Minute {
		t.Fatalf("validity %v", d)
	}
}

func TestCompleteWritesManifestFromS3AndListSignsDownloads(t *testing.T) {
	s, f := newService(t)
	ctx := context.Background()
	if l, err := s.List(ctx, jobA); err != nil || l.Complete || len(l.Files) != 0 {
		t.Fatalf("before upload: %+v %v", l, err)
	}
	files := map[string][]byte{"/mnt/out/a.jsonl": []byte("a\n"), "/mnt/out/sub/b c.bin": {0, 1, 2}}
	for p, b := range files {
		up, err := s.PresignUpload(ctx, jobA, []string{"/mnt/out"}, p, int64(len(b)), hexSum(b))
		if err != nil {
			t.Fatal(err)
		}
		req, _ := http.NewRequest(http.MethodPut, up.URL, strings.NewReader(string(b)))
		req.Header.Set("X-Amz-Checksum-Sha256", up.ChecksumSHA256)
		resp, err := f.Server.Client().Do(req)
		if err != nil || resp.StatusCode != 200 {
			t.Fatalf("put %s: %v %v", p, resp, err)
		}
		resp.Body.Close()
	}
	f.Put("jobs/"+jobB+"/mnt/out/other.txt", []byte("not yours"))

	m, err := s.Complete(ctx, jobA)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Files) != 2 || m.Truncated || m.Files[0].Path != "/mnt/out/a.jsonl" || m.Files[1].Path != "/mnt/out/sub/b c.bin" {
		t.Fatalf("manifest %+v", m)
	}
	for _, fl := range m.Files {
		if fl.SHA256 != hexSum(files[fl.Path]) || fl.Size != int64(len(files[fl.Path])) || fl.Key != "jobs/"+jobA+fl.Path {
			t.Errorf("file %+v", fl)
		}
	}
	raw, ok := f.Object("jobs/" + jobA + "/" + ManifestName)
	if !ok || !strings.Contains(string(raw.Body), `"sha256":"`+hexSum(files["/mnt/out/a.jsonl"])+`"`) {
		t.Fatalf("manifest object %q", raw.Body)
	}

	l, err := s.List(ctx, jobA)
	if err != nil || !l.Complete || len(l.Files) != 2 {
		t.Fatalf("list %+v %v", l, err)
	}
	for _, d := range l.Files {
		resp, err := f.Server.Client().Get(d.URL)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || string(b) != string(files[d.Path]) {
			t.Errorf("download %s: %d %q", d.Path, resp.StatusCode, b)
		}
		if !strings.Contains(d.URL, "X-Amz-Expires=3600") {
			t.Errorf("url %s", d.URL)
		}
	}
}

// A download URL dies with the credentials that signed it, so an instance
// key close to rotation shortens the advertised expiry instead of lying.
func TestDownloadValidityFollowsSignerExpiry(t *testing.T) {
	s, f := newService(t)
	now := time.Now()
	s.cfg.Now = func() time.Time { return now }
	c := f.Instance
	c.Expires = now.Add(30 * time.Minute)
	s.cfg.Creds = awsv4.Static(c)
	f.Put("jobs/"+jobA+"/"+ManifestName, []byte(`{"files":[{"path":"/o/a","key":"jobs/`+jobA+`/o/a","size":1,"sha256":""},{"path":"/x","key":"jobs/`+jobB+`/x"}]}`))
	l, err := s.List(context.Background(), jobA)
	if err != nil || len(l.Files) != 1 {
		t.Fatalf("%+v %v", l, err)
	}
	if got := l.Files[0].ExpiresAt.Sub(now); got != 29*time.Minute || !strings.Contains(l.Files[0].URL, "X-Amz-Expires=1740") {
		t.Fatalf("expiry %v %s", got, l.Files[0].URL)
	}
}
