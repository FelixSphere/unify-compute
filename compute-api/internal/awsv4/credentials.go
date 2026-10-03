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
// compute-api runs in a container, one network hop beyond the instance, so
// the instance's http_put_response_hop_limit must be at least 2
// (deploy/aws/modules/compute-stack/instance.tf); with 1 the token PUT
// times out.
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
	var r struct {
		Code, AccessKeyId, SecretAccessKey, Token string
		Expiration                                time.Time
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return Credentials{}, fmt.Errorf("imds credentials: %w", err)
	}
	if r.Code != "Success" || r.AccessKeyId == "" {
		return Credentials{}, fmt.Errorf("imds credentials: code %q", r.Code)
	}
	return Credentials{AccessKeyID: r.AccessKeyId, SecretAccessKey: r.SecretAccessKey, SessionToken: r.Token, Expires: r.Expiration}, nil
}
