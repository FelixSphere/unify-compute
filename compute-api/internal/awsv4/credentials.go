package awsv4

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

type CredentialSource interface {
	Credentials(ctx context.Context) (Credentials, error)
}

type Static Credentials

func (s Static) Credentials(context.Context) (Credentials, error) { return Credentials(s), nil }

// FromEnv reads AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY and
// AWS_SESSION_TOKEN, for running compute-api off EC2. ok is false when the
// key id is unset.
func FromEnv() (Static, bool) {
	id := os.Getenv("AWS_ACCESS_KEY_ID")
	if id == "" {
		return Static{}, false
	}
	return Static{AccessKeyID: id, SecretAccessKey: os.Getenv("AWS_SECRET_ACCESS_KEY"), SessionToken: os.Getenv("AWS_SESSION_TOKEN")}, true
}

// IMDS reads the instance role's credentials through IMDSv2 and caches them
// until five minutes before they expire. EC2 makes rotated credentials
// available at least five minutes before the old ones expire, so a refresh
// at that point always finds the new set.
//
// From a container this needs an http_put_response_hop_limit of at least 2.
// The hosted environments run with 1 and drop container traffic to IMDS
// (ADR-0005, amendment of 2026-10-03), so compute-api uses File there.
type IMDS struct {
	Endpoint string // default http://169.254.169.254
	Client   *http.Client
	Now      func() time.Time

	mu     sync.Mutex
	cached Credentials
}

const imdsRefreshBefore = 5 * time.Minute

func (m *IMDS) Credentials(ctx context.Context) (Credentials, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now
	if m.Now != nil {
		now = m.Now
	}
	if m.cached.AccessKeyID != "" && now().Add(imdsRefreshBefore).Before(m.cached.Expires) {
		return m.cached, nil
	}
	c, err := m.fetch(ctx)
	if err != nil {
		return Credentials{}, err
	}
	m.cached = c
	return c, nil
}

func (m *IMDS) fetch(ctx context.Context) (Credentials, error) {
	base := m.Endpoint
	if base == "" {
		base = "http://169.254.169.254"
	}
	client := m.Client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	get := func(method, path string, hdr map[string]string) ([]byte, error) {
		req, err := http.NewRequestWithContext(ctx, method, base+path, nil)
		if err != nil {
			return nil, err
		}
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("imds %s %s: %w", method, path, err)
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("imds %s %s: %s", method, path, resp.Status)
		}
		return b, nil
	}
	tok, err := get(http.MethodPut, "/latest/api/token", map[string]string{"X-aws-ec2-metadata-token-ttl-seconds": "300"})
	if err != nil {
		return Credentials{}, err
	}
	auth := map[string]string{"X-aws-ec2-metadata-token": string(tok)}
	role, err := get(http.MethodGet, "/latest/meta-data/iam/security-credentials/", auth)
	if err != nil {
		return Credentials{}, err
	}
	name := strings.TrimSpace(strings.SplitN(string(role), "\n", 2)[0])
	if name == "" {
		return Credentials{}, errors.New("imds: the instance has no role")
	}
	body, err := get(http.MethodGet, "/latest/meta-data/iam/security-credentials/"+name, auth)
	if err != nil {
		return Credentials{}, err
	}
	c, err := parseIMDSCredentials(body)
	if err != nil {
		return Credentials{}, fmt.Errorf("imds credentials: %w", err)
	}
	return c, nil
}

// parseIMDSCredentials reads the body of IMDS's
// /latest/meta-data/iam/security-credentials/<role>.
func parseIMDSCredentials(body []byte) (Credentials, error) {
	var r struct {
		Code, AccessKeyId, SecretAccessKey, Token string
		Expiration                                time.Time
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return Credentials{}, err
	}
	if r.Code != "Success" || r.AccessKeyId == "" {
		return Credentials{}, fmt.Errorf("code %q", r.Code)
	}
	return Credentials{AccessKeyID: r.AccessKeyId, SecretAccessKey: r.SecretAccessKey, SessionToken: r.Token, Expires: r.Expiration}, nil
}

// File reads the instance role's credentials from a file the host writes:
// IMDS's own answer, copied there by a host timer every two minutes
// (scripts/imds-lockdown.sh). On the hosted environments no container can
// reach IMDS (ADR-0005, amendment of 2026-10-03), compute-api included, so
// this is how compute-api gets the role there.
//
// The file is re-read once the credentials it held are within five minutes
// of expiring. EC2 offers rotated credentials at least five minutes before
// the old ones expire, and the timer runs well inside that, so the re-read
// finds the new set; until it does, the old set is returned while it is
// still valid.
type File struct {
	Path string
	Now  func() time.Time

	mu     sync.Mutex
	cached Credentials
}

func (f *File) Credentials(context.Context) (Credentials, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now
	if f.Now != nil {
		now = f.Now
	}
	if f.cached.AccessKeyID != "" && now().Add(imdsRefreshBefore).Before(f.cached.Expires) {
		return f.cached, nil
	}
	body, err := os.ReadFile(f.Path)
	if err == nil {
		var c Credentials
		if c, err = parseIMDSCredentials(body); err == nil {
			f.cached = c
		}
	}
	if f.cached.AccessKeyID != "" && now().Before(f.cached.Expires) {
		return f.cached, nil
	}
	if err != nil {
		return Credentials{}, fmt.Errorf("credentials file %s: %w", f.Path, err)
	}
	return Credentials{}, fmt.Errorf("credentials file %s: expired at %s; is the host's refresh timer running?", f.Path, f.cached.Expires.Format(time.RFC3339))
}
