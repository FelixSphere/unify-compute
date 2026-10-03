package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/FelixSphere/unify-compute/compute-api/internal/artifacts"
	"github.com/FelixSphere/unify-compute/compute-api/internal/backend"
	"github.com/FelixSphere/unify-compute/compute-api/internal/store"
)

// JobReader is the owner-scoped job read the job routes share.
type JobReader interface {
	Get(ctx context.Context, owner, id string) (store.Job, error)
}

// ArtifactStore is what the artifact routes need from internal/artifacts.
type ArtifactStore interface {
	List(ctx context.Context, jobID string) (artifacts.Listing, error)
	VerifyToken(jobID, token string) (owner string, err error)
	PresignUpload(ctx context.Context, jobID string, mounts []string, path string, size int64, sha256 string) (artifacts.Upload, error)
	Complete(ctx context.Context, jobID string) (artifacts.Manifest, error)
}

func (s *Server) artifactsReady() bool {
	return s.Artifacts != nil && s.Jobs != nil
}

// owner resolves the caller's key, writing the 401/503 itself on failure.
// A key auth already verified has its owner on the request.
func (s *Server) owner(w http.ResponseWriter, r *http.Request) (string, bool) {
	if o := ownerFrom(r.Context()); o != "" {
		return o, true
	}
	key, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	owner, err := s.Owner(r.Context(), strings.TrimSpace(key))
	switch {
	case errors.Is(err, ErrKeyRejected):
		writeError(w, http.StatusUnauthorized, apiError{Type: "authentication_error", Code: "invalid_key", Message: "This key was not accepted."})
		return "", false
	case err != nil:
		writeError(w, http.StatusServiceUnavailable, apiError{Type: "console_unavailable", Code: "console_unavailable", Message: "The key could not be verified; retry shortly."})
		return "", false
	}
	return owner, true
}

func jobNotFound(w http.ResponseWriter, id string) {
	writeError(w, http.StatusNotFound, apiError{Type: "not_found", Code: "job_not_found", Message: fmt.Sprintf("no job %q", id)})
}

func storageUnavailable(w http.ResponseWriter) {
	writeError(w, http.StatusServiceUnavailable, apiError{Type: "storage_unavailable", Code: "storage_unavailable", Message: "Artifact storage could not be reached; retry shortly."})
}

type artifactView struct {
	Object    string `json:"object"`
	Path      string `json:"path"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
	URL       string `json:"url"`
	ExpiresAt int64  `json:"expires_at"`
}

// listArtifacts serves GET /compute/v1/jobs/{id}/artifacts (job-api.md).
// The arrears check before signing (ADR-0008, metering.md "Pre-check at URL
// issuance") needs the ledger (C5) and is not made yet.
func (s *Server) listArtifacts(w http.ResponseWriter, r *http.Request) {
	if !s.artifactsReady() || s.Owner == nil {
		s.notImplemented("job artifacts").ServeHTTP(w, r)
		return
	}
	owner, ok := s.owner(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if _, err := s.Jobs.Get(r.Context(), owner, id); errors.Is(err, store.ErrNotFound) {
		jobNotFound(w, id)
		return
	} else if err != nil {
		s.Log.Error("artifacts: job lookup", "job", id, "err", err)
		writeError(w, http.StatusInternalServerError, apiError{Type: "internal_error", Code: "internal_error", Message: "the job could not be read"})
		return
	}
	l, err := s.Artifacts.List(r.Context(), id)
	if err != nil {
		s.Log.Error("artifacts: list", "job", id, "err", err)
		storageUnavailable(w)
		return
	}
	data := make([]artifactView, len(l.Files))
	for i, f := range l.Files {
		data[i] = artifactView{Object: "compute.artifact", Path: f.Path, Size: f.Size, SHA256: f.SHA256, URL: f.URL, ExpiresAt: f.ExpiresAt.Unix()}
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data, "complete": l.Complete, "truncated": l.Truncated})
}

// uploadCaller authenticates a job's run step by its upload token and
// returns the job, which must not be terminal: the token dies with the job.
func (s *Server) uploadCaller(w http.ResponseWriter, r *http.Request) (store.Job, bool) {
	if !s.artifactsReady() {
		s.notImplemented("artifact upload").ServeHTTP(w, r)
		return store.Job{}, false
	}
	id := r.PathValue("id")
	tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	owner, err := s.Artifacts.VerifyToken(id, strings.TrimSpace(tok))
	if err != nil {
		writeError(w, http.StatusUnauthorized, apiError{Type: "authentication_error", Code: "invalid_upload_token", Message: "The upload token is invalid or expired."})
		return store.Job{}, false
	}
	job, err := s.Jobs.Get(r.Context(), owner, id)
	if errors.Is(err, store.ErrNotFound) {
		jobNotFound(w, id)
		return store.Job{}, false
	}
	if err != nil {
		s.Log.Error("artifacts: job lookup", "job", id, "err", err)
		writeError(w, http.StatusInternalServerError, apiError{Type: "internal_error", Code: "internal_error", Message: "the job could not be read"})
		return store.Job{}, false
	}
	if job.Status.Terminal() {
		writeError(w, http.StatusConflict, apiError{Type: "conflict", Code: "job_terminal", Message: fmt.Sprintf("job %s is %s; its outputs can no longer be uploaded", id, job.Status)})
		return store.Job{}, false
	}
	return job, true
}

// presignUpload serves the run step: one file in, one PUT URL out, as
// "<base64 sha256> <url>\n" so bash can read it without a JSON parser.
func (s *Server) presignUpload(w http.ResponseWriter, r *http.Request) {
	job, ok := s.uploadCaller(w, r)
	if !ok {
		return
	}
	var spec backend.Spec
	if err := json.Unmarshal(job.Spec, &spec); err != nil {
		writeError(w, http.StatusInternalServerError, apiError{Type: "internal_error", Code: "internal_error", Message: "the job spec could not be read"})
		return
	}
	mounts, err := artifacts.OutputMounts(spec.Outputs)
	if err != nil || len(mounts) == 0 {
		invalid(w, "no_outputs", "path", "this job declares no valid spec.outputs")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		invalid(w, "malformed_body", "", "expected a form with path, size and sha256")
		return
	}
	size, err := strconv.ParseInt(r.PostForm.Get("size"), 10, 64)
	if err != nil {
		invalid(w, "invalid_size", "size", "size must be an integer")
		return
	}
	up, err := s.Artifacts.PresignUpload(r.Context(), job.ID, mounts, r.PostForm.Get("path"), size, r.PostForm.Get("sha256"))
	switch {
	case errors.Is(err, artifacts.ErrBadPath):
		invalid(w, "invalid_path", "path", "path must be a file under one of spec.outputs")
		return
	case errors.Is(err, artifacts.ErrBadSum):
		invalid(w, "invalid_sha256", "sha256", err.Error())
		return
	case errors.Is(err, artifacts.ErrTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, apiError{Type: "invalid_request", Code: "file_too_large", Message: err.Error(), Param: "size"})
		return
	case err != nil:
		s.Log.Error("artifacts: presign upload", "job", job.ID, "err", err)
		storageUnavailable(w)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, "%s %s\n", up.ChecksumSHA256, up.URL)
}

func (s *Server) completeUpload(w http.ResponseWriter, r *http.Request) {
	job, ok := s.uploadCaller(w, r)
	if !ok {
		return
	}
	m, err := s.Artifacts.Complete(r.Context(), job.ID)
	if err != nil {
		s.Log.Error("artifacts: complete", "job", job.ID, "err", err)
		storageUnavailable(w)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "compute.artifact_manifest", "files": len(m.Files), "truncated": m.Truncated})
}
