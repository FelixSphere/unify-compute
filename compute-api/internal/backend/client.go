package backend

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// The wire contract below is SkyPilot v0.13.0's API server (commit b1431e5 of
// FelixSphere/skypilot, the pin in ADR-0003). Every path, field and header is
// cited to that source in docs/spec/backend.md; nothing is inferred from the
// Python SDK's behaviour alone.
const (
	// sky/server/constants.py: API_VERSION = 56, VERSION_HEADER, API_VERSION_HEADER.
	// Sending them makes the server answer in the current wire format rather
	// than the pre-0.11 compatibility path.
	apiVersion    = "56"
	skypilotPin   = "0.13.0"
	requestIDHdr  = "X-Skypilot-Request-ID"
	userIDEnvVar  = "SKYPILOT_USER_ID"
	userEnvVar    = "SKYPILOT_USER"
	serviceUser   = "unify-compute"
	requestPrefix = "sky."
)

// RequestStatus is sky/server/requests/requests.py RequestStatus.
type RequestStatus string

const (
	RequestPending   RequestStatus = "PENDING"
	RequestWaiting   RequestStatus = "WAITING"
	RequestRunning   RequestStatus = "RUNNING"
	RequestSucceeded RequestStatus = "SUCCEEDED"
	RequestFailed    RequestStatus = "FAILED"
	RequestCancelled RequestStatus = "CANCELLED"
)

func (s RequestStatus) Finished() bool {
	return s == RequestSucceeded || s == RequestFailed || s == RequestCancelled
}

// requestPayload is payloads.RequestPayload. return_value and error are JSON
// documents encoded as strings inside the JSON body.
type requestPayload struct {
	RequestID   string        `json:"request_id"`
	Name        string        `json:"name"`
	Status      RequestStatus `json:"status"`
	ReturnValue string        `json:"return_value"`
	Error       string        `json:"error"`
	ClusterName *string       `json:"cluster_name"`
	StatusMsg   *string       `json:"status_msg"`
	ShouldRetry bool          `json:"should_retry"`
	CreatedAt   float64       `json:"created_at"`
	FinishedAt  *float64      `json:"finished_at"`
}

// RequestError is a failed request's error, as set by Request.set_error:
// {"object": <pickled exception>, "type": <class name>, "message": str(e)}.
type RequestError struct {
	RequestID string
	Type      string `json:"type"`
	Message   string `json:"message"`
}

func (e *RequestError) Error() string {
	return fmt.Sprintf("skypilot request %s failed: %s: %s", e.RequestID, e.Type, e.Message)
}

// HTTPError is a non-2xx answer that is not a failed request (auth, version
// mismatch, validation, unknown request id).
type HTTPError struct {
	Method, Path string
	Status       int
	Body         string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("skypilot %s %s: HTTP %d: %s", e.Method, e.Path, e.Status, e.Body)
}

var (
	ErrRequestNotFound = errors.New("skypilot: request not found")
	// ErrRetryRequest is /api/get's 503: the server marked the request
	// should_retry (it was interrupted by a server restart), so the original
	// call has to be made again.
	ErrRetryRequest = errors.New("skypilot: request should be retried")
)

// Client talks to one SkyPilot API server. It holds no state beyond
// configuration; every call is one HTTP round trip.
type Client struct {
	BaseURL string
	// Token is a SkyPilot service-account token, sent as a bearer token. Empty
	// for a server without auth (local development).
	Token string
	// UserHash is the SkyPilot user id compute-api acts as. All clusters are
	// owned by it, so one service identity can see and tear down every job's
	// cluster.
	UserHash string
	HTTP     *http.Client
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

// requestBody carries the payloads.RequestBody fields every POST shares.
// They are sent explicitly: pydantic fills omitted ones on the server from the
// server's own environment (RequestBody.__init__), which would make clusters
// owned by whoever started the API server.
type requestBody struct {
	EnvVars                map[string]string `json:"env_vars"`
	Entrypoint             string            `json:"entrypoint"`
	EntrypointCommand      string            `json:"entrypoint_command"`
	UsingRemoteAPIServer   bool              `json:"using_remote_api_server"`
	OverrideSkypilotConfig map[string]any    `json:"override_skypilot_config"`
}

func (c *Client) base() requestBody {
	return requestBody{
		EnvVars:                map[string]string{userIDEnvVar: c.UserHash, userEnvVar: serviceUser},
		Entrypoint:             serviceUser,
		EntrypointCommand:      serviceUser,
		UsingRemoteAPIServer:   true,
		OverrideSkypilotConfig: map[string]any{},
	}
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, body any) (*http.Response, error) {
	u := strings.TrimRight(c.BaseURL, "/") + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var rd io.Reader
	if body != nil {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(body); err != nil {
			return nil, err
		}
		rd = &buf
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-SkyPilot-API-Version", apiVersion)
	req.Header.Set("X-SkyPilot-Version", skypilotPin)
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	return c.httpClient().Do(req)
}

func httpError(resp *http.Response, method, path string) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return &HTTPError{Method: method, Path: path, Status: resp.StatusCode, Body: strings.TrimSpace(string(b))}
}

// submit POSTs an asynchronous operation. The server schedules it and answers
// 200 at once with the request id in a header; the result is fetched later
// with Get. sky/server/server.py RequestIDMiddleware.
func (c *Client) submit(ctx context.Context, path string, body any) (string, error) {
	resp, err := c.do(ctx, http.MethodPost, path, nil, body)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", httpError(resp, http.MethodPost, path)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	id := resp.Header.Get(requestIDHdr)
	if id == "" {
		return "", fmt.Errorf("skypilot POST %s: no %s header", path, requestIDHdr)
	}
	return id, nil
}

// Get blocks until the request finishes (server side, /api/get polls its own
// table) and decodes the return value into out. A failed request is a
// *RequestError. Use RequestState for a non-blocking look at a long request.
func (c *Client) Get(ctx context.Context, requestID string, out any) error {
	resp, err := c.do(ctx, http.MethodGet, "/api/get", url.Values{"request_id": {requestID}}, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		var p requestPayload
		if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
			return fmt.Errorf("skypilot /api/get %s: %w", requestID, err)
		}
		if out == nil || p.ReturnValue == "" {
			return nil
		}
		return json.Unmarshal([]byte(p.ReturnValue), out)
	case http.StatusInternalServerError:
		// HTTPException(500, detail=request_task.encode().model_dump())
		var body struct {
			Detail requestPayload `json:"detail"`
		}
		raw, _ := io.ReadAll(resp.Body)
		if err := json.Unmarshal(raw, &body); err != nil || body.Detail.Error == "" {
			return &HTTPError{Method: http.MethodGet, Path: "/api/get", Status: resp.StatusCode, Body: string(raw)}
		}
		return decodeRequestError(body.Detail)
	case http.StatusNotFound:
		return ErrRequestNotFound
	case http.StatusServiceUnavailable:
		return ErrRetryRequest
	}
	return httpError(resp, http.MethodGet, "/api/get")
}

func decodeRequestError(p requestPayload) error {
	re := &RequestError{RequestID: p.RequestID}
	if err := json.Unmarshal([]byte(p.Error), re); err != nil || re.Type == "" {
		return fmt.Errorf("skypilot request %s failed with undecodable error %q", p.RequestID, p.Error)
	}
	// Messages are formatted for the CLI and carry colour codes, e.g.
	// "\x1b[33mCluster 'x' does not exist.\x1b[0m" (recorded from 0.13.0).
	re.Message = ansi.ReplaceAllString(re.Message, "")
	return re
}

var ansi = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// RequestInfo is the non-blocking view of a request.
type RequestInfo struct {
	ID         string
	Name       string
	Status     RequestStatus
	Cluster    string
	StatusMsg  string
	CreatedAt  time.Time
	FinishedAt *time.Time
}

func (p requestPayload) info() RequestInfo {
	ri := RequestInfo{ID: p.RequestID, Name: p.Name, Status: p.Status, CreatedAt: unixFloat(p.CreatedAt)}
	if p.ClusterName != nil {
		ri.Cluster = *p.ClusterName
	}
	if p.StatusMsg != nil {
		ri.StatusMsg = *p.StatusMsg
	}
	if p.FinishedAt != nil {
		t := unixFloat(*p.FinishedAt)
		ri.FinishedAt = &t
	}
	return ri
}

func unixFloat(f float64) time.Time {
	return time.UnixMilli(int64(f * 1000)).UTC()
}

func (c *Client) listRequests(ctx context.Context, q url.Values) ([]RequestInfo, error) {
	resp, err := c.do(ctx, http.MethodGet, "/api/status", q, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, httpError(resp, http.MethodGet, "/api/status")
	}
	var ps []requestPayload
	if err := json.NewDecoder(resp.Body).Decode(&ps); err != nil {
		return nil, fmt.Errorf("skypilot /api/status: %w", err)
	}
	out := make([]RequestInfo, len(ps))
	for i, p := range ps {
		out[i] = p.info()
	}
	return out, nil
}

// RequestState reports where a request is without waiting for it.
func (c *Client) RequestState(ctx context.Context, requestID string) (RequestInfo, error) {
	rs, err := c.listRequests(ctx, url.Values{"request_ids": {requestID}, "all_status": {"true"}})
	if err != nil {
		return RequestInfo{}, err
	}
	for _, r := range rs {
		if r.ID == requestID {
			return r, nil
		}
	}
	return RequestInfo{}, ErrRequestNotFound
}

// LaunchRequestFor finds the newest launch request for a cluster, in any
// state. It is how a launch whose POST answer was lost is adopted instead of
// launched twice: a second /launch on the same cluster name would run the
// task again on the first one's cluster.
func (c *Client) LaunchRequestFor(ctx context.Context, cluster string) (RequestInfo, bool, error) {
	rs, err := c.listRequests(ctx, url.Values{"cluster_name": {cluster}, "all_status": {"true"}})
	if err != nil {
		return RequestInfo{}, false, err
	}
	var best RequestInfo
	found := false
	for _, r := range rs {
		if r.Name != requestPrefix+"launch" || r.Cluster != cluster {
			continue
		}
		if !found || r.CreatedAt.After(best.CreatedAt) {
			best, found = r, true
		}
	}
	return best, found, nil
}

type launchBody struct {
	requestBody
	Task         string `json:"task"`
	ClusterName  string `json:"cluster_name"`
	RetryUntilUp bool   `json:"retry_until_up"`
}

// Launch schedules `sky launch` of task on a new cluster and returns the
// request id. The task is sent as JSON, which the server reads as YAML
// (dag_utils.load_chain_dag_from_yaml_str).
func (c *Client) Launch(ctx context.Context, cluster string, task Task) (string, error) {
	raw, err := marshalNoEscape(task)
	if err != nil {
		return "", err
	}
	// retry_until_up stays false: waiting for capacity forever would hold a
	// customer's job in provisioning with no bound. Exhausting every
	// candidate fails the request, and that is a failed job (job-api.md).
	return c.submit(ctx, "/launch", launchBody{requestBody: c.base(), Task: string(raw), ClusterName: cluster})
}

// ClusterStatus is status_lib.ClusterStatus.
type ClusterStatus string

const (
	ClusterInit         ClusterStatus = "INIT"
	ClusterUp           ClusterStatus = "UP"
	ClusterStopped      ClusterStatus = "STOPPED"
	ClusterAutostopping ClusterStatus = "AUTOSTOPPING"
)

// ClusterRecord is the subset of responses.StatusResponse the backend reads.
type ClusterRecord struct {
	Name             string        `json:"name"`
	Status           ClusterStatus `json:"status"`
	LaunchedAt       int64         `json:"launched_at"`
	StatusUpdatedAt  int64         `json:"status_updated_at"`
	ClusterEverUp    bool          `json:"cluster_ever_up"`
	Cloud            *string       `json:"cloud"`
	Region           *string       `json:"region"`
	Accelerators     *string       `json:"accelerators"`
	ResourcesStrFull *string       `json:"resources_str_full"`
	InitKind         *string       `json:"init_kind"`
	InitStatusReason *string       `json:"init_status_reason"`
	ClusterNameOnCld *string       `json:"cluster_name_on_cloud"`
	ToDown           bool          `json:"to_down"`
	Autostop         int           `json:"autostop"`
}

type statusBody struct {
	requestBody
	ClusterNames       []string `json:"cluster_names"`
	Refresh            string   `json:"refresh"`
	AllUsers           bool     `json:"all_users"`
	IncludeCredentials bool     `json:"include_credentials"`
}

// Status returns the named clusters that exist. A cluster that is gone is
// simply absent. Refresh is AUTO, which re-queries the cloud for clusters with
// autostop set or on spot (common.StatusRefreshMode); every cluster this
// backend launches has autostop set, so AUTO is a real cloud check for ours
// and catches a preempted or externally terminated instance.
func (c *Client) Status(ctx context.Context, clusters ...string) ([]ClusterRecord, error) {
	id, err := c.submit(ctx, "/status", statusBody{
		requestBody: c.base(), ClusterNames: clusters, Refresh: "AUTO", AllUsers: true,
		// The default is true and returns SSH private keys; nothing here
		// needs them.
		IncludeCredentials: false,
	})
	if err != nil {
		return nil, err
	}
	var recs []ClusterRecord
	if err := c.Get(ctx, id, &recs); err != nil {
		return nil, err
	}
	return recs, nil
}

// JobStatus is job_lib.JobStatus, the state of a job on its cluster.
type JobStatus string

const (
	JobInit         JobStatus = "INIT"
	JobPending      JobStatus = "PENDING"
	JobSettingUp    JobStatus = "SETTING_UP"
	JobRunning      JobStatus = "RUNNING"
	JobFailedDriver JobStatus = "FAILED_DRIVER"
	JobSucceeded    JobStatus = "SUCCEEDED"
	JobFailed       JobStatus = "FAILED"
	JobFailedSetup  JobStatus = "FAILED_SETUP"
	JobCancelled    JobStatus = "CANCELLED"
)

func (s JobStatus) Terminal() bool {
	switch s {
	case JobFailedDriver, JobSucceeded, JobFailed, JobFailedSetup, JobCancelled:
		return true
	}
	return false
}

// ClusterJob is responses.ClusterJobRecord. start_at and end_at are written
// by the skylet on the node, so end_at is the process exit time metering
// stops at, not when compute-api noticed.
type ClusterJob struct {
	JobID       int       `json:"job_id"`
	JobName     string    `json:"job_name"`
	SubmittedAt float64   `json:"submitted_at"`
	StartAt     *float64  `json:"start_at"`
	EndAt       *float64  `json:"end_at"`
	Status      JobStatus `json:"status"`
}

type queueBody struct {
	requestBody
	ClusterName  string `json:"cluster_name"`
	SkipFinished bool   `json:"skip_finished"`
	AllUsers     bool   `json:"all_users"`
}

// Queue returns the jobs on a cluster. It fails with ClusterNotUpError when
// the cluster is not UP.
func (c *Client) Queue(ctx context.Context, cluster string) ([]ClusterJob, error) {
	id, err := c.submit(ctx, "/queue", queueBody{requestBody: c.base(), ClusterName: cluster, AllUsers: true})
	if err != nil {
		return nil, err
	}
	var jobs []ClusterJob
	if err := c.Get(ctx, id, &jobs); err != nil {
		return nil, err
	}
	return jobs, nil
}

type downBody struct {
	requestBody
	ClusterName string `json:"cluster_name"`
	Purge       bool   `json:"purge"`
}

// Down schedules teardown of a cluster and returns the request id.
func (c *Client) Down(ctx context.Context, cluster string) (string, error) {
	return c.submit(ctx, "/down", downBody{requestBody: c.base(), ClusterName: cluster})
}

type cancelRequestsBody struct {
	requestBody
	RequestIDs []string `json:"request_ids"`
}

// CancelRequest kills an in-flight request, e.g. a launch still provisioning
// when the customer cancels. It does not tear anything down; Down does.
func (c *Client) CancelRequest(ctx context.Context, requestID string) error {
	id, err := c.submit(ctx, "/api/cancel", cancelRequestsBody{requestBody: c.base(), RequestIDs: []string{requestID}})
	if err != nil {
		return err
	}
	return c.Get(ctx, id, nil)
}

type logsBody struct {
	requestBody
	ClusterName string `json:"cluster_name"`
	JobID       *int   `json:"job_id"`
	Follow      bool   `json:"follow"`
	Tail        int    `json:"tail"`
}

// Logs copies a job's stdout/stderr to w. /logs streams text/plain directly;
// the stream also carries the CLI's spinner control lines
// (rich_utils.Control, e.g. <heartbeat></heartbeat>), which are dropped. A
// failure is not in the HTTP status: against 0.13.0 a missing cluster answers
// 200 with an empty body, and the ClusterDoesNotExist is on the request the
// response header names, so that request is read once the stream ends.
func (c *Client) Logs(ctx context.Context, cluster string, jobID *int, w io.Writer) error {
	resp, err := c.do(ctx, http.MethodPost, "/logs", nil, logsBody{requestBody: c.base(), ClusterName: cluster, JobID: jobID})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return httpError(resp, http.MethodPost, "/logs")
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if isControlLine(line) {
			continue
		}
		if _, err := io.WriteString(w, line+"\n"); err != nil {
			return err
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if id := resp.Header.Get(requestIDHdr); id != "" {
		// tail_logs returns an exit code (0, or 100 when the job failed),
		// which is not an error of fetching the logs.
		var code int
		return c.Get(ctx, id, &code)
	}
	return nil
}

var controlCodes = []string{"rich_init", "rich_start", "rich_stop", "rich_exit", "rich_update", "heartbeat", "retry"}

func isControlLine(line string) bool {
	for _, c := range controlCodes {
		if strings.Contains(line, "<"+c+">") {
			return true
		}
	}
	return false
}

func marshalNoEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
