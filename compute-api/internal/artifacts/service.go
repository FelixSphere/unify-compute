// Package artifacts stores job outputs under s3://<bucket>/jobs/<job_id>/
// and serves them by presigned URL (ADR-0005).
//
// Write path: the run step (OutputStep) asks compute-api for one presigned
// PUT per file, presenting the job's upload token. compute-api signs each
// URL with a session of the artifacts-writer role whose inline session
// policy allows PutObject on jobs/<job_id>/* only, and binds the file's
// SHA-256 into the signature, so S3 refuses any other body. When the run
// step is done it calls Complete, and compute-api writes
// jobs/<job_id>/_manifest.json from what S3 actually holds.
//
// Read path: List reads the manifest with the instance role and presigns a
// GET for each file, valid for at most an hour.
//
// The shared bucket is never mounted on a node and no node holds AWS
// credentials.
package artifacts

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/FelixSphere/unify-compute/compute-api/internal/awsv4"
)

const (
	// URLValidity is the longest a presigned URL lives (job-api.md).
	URLValidity = time.Hour
	// MaxFileSize is S3's single-PUT limit. Larger files need multipart
	// upload, which the run step does not do yet.
	MaxFileSize int64 = 5 << 30
	// ManifestName is the manifest's key under the job prefix. No output
	// file can have this key: every file key has its mount's path between
	// the prefix and the name, and a mount cannot be "/".
	ManifestName = "_manifest.json"
	// sessionDuration: AssumeRole from an instance role is role chaining,
	// which AWS caps at one hour.
	sessionDuration = time.Hour
	// A cached writer session is reused while it has this much left, so a
	// URL signed with it is good for at least this long.
	sessionMinLeft = 20 * time.Minute
)

var (
	ErrBadPath  = errors.New("artifacts: path is not a file under an output mount")
	ErrTooLarge = fmt.Errorf("artifacts: file is larger than %d bytes", MaxFileSize)
	ErrBadSum   = errors.New("artifacts: sha256 must be 64 lowercase hex characters")

	jobIDPattern = regexp.MustCompile(`^job_[0-9A-HJKMNP-TV-Z]{26}$`)
	sha256Hex    = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type Config struct {
	Bucket        string
	WriterRoleARN string
	// PublicURL is where a node reaches this compute-api, e.g.
	// https://staging.compute.unifyai.us.
	PublicURL   string
	TokenSecret []byte
	// Creds are the instance role's: they assume the writer role, list,
	// HEAD and read objects, and sign download URLs.
	Creds  awsv4.CredentialSource
	Client *awsv4.Client
	Now    func() time.Time
}

type Service struct {
	cfg Config

	mu       sync.Mutex
	sessions map[string]awsv4.Credentials // job id → writer session
}

func New(cfg Config) (*Service, error) {
	switch {
	case cfg.Bucket == "", cfg.WriterRoleARN == "", cfg.PublicURL == "", cfg.Creds == nil, cfg.Client == nil:
		return nil, errors.New("artifacts: bucket, writer role, public URL, credentials and client are all required")
	case len(cfg.TokenSecret) < 32:
		return nil, errors.New("artifacts: token secret must be at least 32 bytes")
	case !strings.HasPrefix(cfg.PublicURL, "https://") && !strings.HasPrefix(cfg.PublicURL, "http://127.0.0.1"):
		return nil, errors.New("artifacts: public URL must be https (upload tokens travel in it)")
	}
	return &Service{cfg: cfg, sessions: map[string]awsv4.Credentials{}}, nil
}

func (s *Service) now() time.Time {
	if s.cfg.Now != nil {
		return s.cfg.Now()
	}
	return time.Now()
}

func prefix(jobID string) string { return "jobs/" + jobID + "/" }

// VerifyToken returns the owner an upload token was minted for.
func (s *Service) VerifyToken(jobID, tok string) (string, error) {
	return verifyToken(s.cfg.TokenSecret, jobID, tok, s.now())
}

func sessionPolicy(bucket, jobID string) string {
	b, _ := json.Marshal(map[string]any{
		"Version": "2012-10-17",
		"Statement": []map[string]any{{
			"Effect":   "Allow",
			"Action":   []string{"s3:PutObject", "s3:AbortMultipartUpload"},
			"Resource": "arn:aws:s3:::" + bucket + "/" + prefix(jobID) + "*",
		}},
	})
	return string(b)
}

// writer returns a session of the writer role narrowed to jobID's prefix.
func (s *Service) writer(ctx context.Context, jobID string) (awsv4.Credentials, error) {
	now := s.now()
	s.mu.Lock()
	c, ok := s.sessions[jobID]
	s.mu.Unlock()
	if ok && c.Expires.Sub(now) >= sessionMinLeft {
		return c, nil
	}
	inst, err := s.cfg.Creds.Credentials(ctx)
	if err != nil {
		return awsv4.Credentials{}, err
	}
	c, err = s.cfg.Client.AssumeRole(ctx, inst, s.cfg.WriterRoleARN, jobID, sessionPolicy(s.cfg.Bucket, jobID), sessionDuration)
	if err != nil {
		return awsv4.Credentials{}, err
	}
	s.mu.Lock()
	for id, old := range s.sessions {
		if !now.Before(old.Expires) {
			delete(s.sessions, id)
		}
	}
	s.sessions[jobID] = c
	s.mu.Unlock()
	return c, nil
}

// validity is how long a URL signed with c may be offered for: an hour, or
// less if c expires sooner, since a presigned URL dies with its signer.
func (s *Service) validity(c awsv4.Credentials) time.Duration {
	d := URLValidity
	if !c.Expires.IsZero() {
		if left := c.Expires.Sub(s.now()) - time.Minute; left < d {
			d = left
		}
	}
	return d.Truncate(time.Second)
}

type Upload struct {
	URL string
	// ChecksumSHA256 is the base64 SHA-256 the PUT must send as
	// x-amz-checksum-sha256; it is part of the signature.
	ChecksumSHA256 string
	ExpiresAt      time.Time
}

// PresignUpload signs a PUT for one output file. filePath is the absolute
// path the job wrote, which must lie under one of mounts; its key is
// jobs/<job_id><filePath>.
func (s *Service) PresignUpload(ctx context.Context, jobID string, mounts []string, filePath string, size int64, sum string) (Upload, error) {
	if !jobIDPattern.MatchString(jobID) {
		return Upload{}, ErrBadPath
	}
	if !path.IsAbs(filePath) || path.Clean(filePath) != filePath || strings.ContainsRune(filePath, 0) {
		return Upload{}, ErrBadPath
	}
	under := false
	for _, m := range mounts {
		if strings.HasPrefix(filePath, m+"/") {
			under = true
		}
	}
	if !under {
		return Upload{}, ErrBadPath
	}
	if size < 0 || size > MaxFileSize {
		return Upload{}, ErrTooLarge
	}
	if !sha256Hex.MatchString(sum) {
		return Upload{}, ErrBadSum
	}
	raw, _ := hex.DecodeString(sum)
	b64 := base64.StdEncoding.EncodeToString(raw)
	c, err := s.writer(ctx, jobID)
	if err != nil {
		return Upload{}, err
	}
	d := s.validity(c)
	key := strings.TrimSuffix(prefix(jobID), "/") + filePath
	u := s.cfg.Client.Presign(c, http.MethodPut, s.cfg.Bucket, key, http.Header{"X-Amz-Checksum-Sha256": {b64}}, d)
	return Upload{URL: u, ChecksumSHA256: b64, ExpiresAt: s.now().Add(d)}, nil
}

type File struct {
	Path   string `json:"path"`
	Key    string `json:"key"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type Manifest struct {
	JobID     string    `json:"job_id"`
	CreatedAt time.Time `json:"created_at"`
	Files     []File    `json:"files"`
	// Truncated: the job wrote more than MaxFiles objects and the manifest
	// lists the first MaxFiles by key.
	Truncated bool `json:"truncated"`
}

// Complete writes jobs/<job_id>/_manifest.json from what is in the prefix,
// each file's size and SHA-256 as S3 reports them (S3 verified the SHA-256
// on upload). Calling it again rewrites the manifest.
func (s *Service) Complete(ctx context.Context, jobID string) (Manifest, error) {
	if !jobIDPattern.MatchString(jobID) {
		return Manifest{}, ErrBadPath
	}
	inst, err := s.cfg.Creds.Credentials(ctx)
	if err != nil {
		return Manifest{}, err
	}
	objs, truncated, err := s.cfg.Client.List(ctx, inst, s.cfg.Bucket, prefix(jobID), MaxFiles+1)
	if err != nil {
		return Manifest{}, err
	}
	var keys []string
	for _, o := range objs {
		if o.Key != prefix(jobID)+ManifestName {
			keys = append(keys, o.Key)
		}
	}
	sort.Strings(keys)
	if len(keys) > MaxFiles {
		keys, truncated = keys[:MaxFiles], true
	}
	files := make([]File, len(keys))
	errs := make([]error, len(keys))
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for i, k := range keys {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			size, b64, err := s.cfg.Client.HeadChecksum(ctx, inst, s.cfg.Bucket, k)
			if err != nil {
				errs[i] = err
				return
			}
			sum := ""
			if raw, err := base64.StdEncoding.DecodeString(b64); err == nil && len(raw) == 32 {
				sum = hex.EncodeToString(raw)
			}
			files[i] = File{Path: strings.TrimPrefix(k, strings.TrimSuffix(prefix(jobID), "/")), Key: k, Size: size, SHA256: sum}
		}()
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return Manifest{}, err
	}
	m := Manifest{JobID: jobID, CreatedAt: s.now().UTC(), Files: files, Truncated: truncated}
	body, _ := json.Marshal(m)
	w, err := s.writer(ctx, jobID)
	if err != nil {
		return Manifest{}, err
	}
	if err := s.cfg.Client.Put(ctx, w, s.cfg.Bucket, prefix(jobID)+ManifestName, "application/json", body); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

type Download struct {
	File
	URL       string
	ExpiresAt time.Time
}

type Listing struct {
	// Complete is false until the job's run step has written the manifest:
	// the job is still running, it ended without reaching its upload
	// (cancelled, preempted, max runtime), or retention has deleted it.
	Complete  bool
	Truncated bool
	Files     []Download
}

// List reads the job's manifest and signs a download URL for every file.
func (s *Service) List(ctx context.Context, jobID string) (Listing, error) {
	if !jobIDPattern.MatchString(jobID) {
		return Listing{}, ErrBadPath
	}
	inst, err := s.cfg.Creds.Credentials(ctx)
	if err != nil {
		return Listing{}, err
	}
	b, err := s.cfg.Client.Get(ctx, inst, s.cfg.Bucket, prefix(jobID)+ManifestName)
	if awsv4.IsNotFound(err) {
		return Listing{Files: []Download{}}, nil
	}
	if err != nil {
		return Listing{}, err
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return Listing{}, fmt.Errorf("artifacts: manifest of %s: %w", jobID, err)
	}
	d := s.validity(inst)
	exp := s.now().Add(d)
	out := Listing{Complete: true, Truncated: m.Truncated, Files: make([]Download, 0, len(m.Files))}
	for _, f := range m.Files {
		// compute-api wrote the manifest, but the key is what gets signed,
		// so it is held to the prefix all the same.
		if !strings.HasPrefix(f.Key, prefix(jobID)) {
			continue
		}
		out.Files = append(out.Files, Download{File: f, URL: s.cfg.Client.Presign(inst, http.MethodGet, s.cfg.Bucket, f.Key, nil, d), ExpiresAt: exp})
	}
	return out, nil
}
