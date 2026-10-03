// Package skypilottest is an in-process fake of the part of the SkyPilot
// v0.13.0 API server the backend uses: /launch, /status, /queue, /down,
// /logs, /api/status, /api/get and /api/cancel. Response shapes follow the
// recordings in ../testdata/skypilot-0.13.0 (taken from a real 0.13.0 server);
// a parity test keeps them in step. Cluster and job lifecycle is scripted by
// the test: nothing moves until the test says so, so every observation the
// backend makes is deterministic.
package skypilottest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"time"
)

const elided = "<pickle elided>"

type request struct {
	ID         string
	Name       string
	Cluster    string
	Status     string
	CreatedAt  float64
	FinishedAt *float64
	Return     any
	ErrType    string
	ErrMessage string
	Body       json.RawMessage
}

// Cluster is the fake's record of one cluster.
type Cluster struct {
	Name             string
	Status           string // INIT, UP, STOPPED, AUTOSTOPPING
	Cloud            string // as SkyPilot prints it: "AWS"
	Region           string
	ResourcesStrFull string
	StatusUpdatedAt  int64
	Jobs             []Job
	Logs             string
}

type Job struct {
	ID      int
	Status  string
	StartAt *float64
	EndAt   *float64
}

// Launch is what a /launch call carried.
type Launch struct {
	RequestID string
	Cluster   string
	Task      map[string]any
	TaskRaw   string
	Body      map[string]any
}

type Server struct {
	*httptest.Server
	// Token, when set, is the bearer token every request must carry.
	Token string

	mu       sync.Mutex
	seq      int
	now      func() time.Time
	requests map[string]*request
	clusters map[string]*Cluster
	launches []Launch
	calls    []string
	// downFailures makes the next n /down requests fail.
	downFailures int
	// failNext makes the next request to a path answer this HTTP status.
	failNext map[string]int
}

func New() *Server {
	s := &Server{
		now:      time.Now,
		requests: map[string]*request{},
		clusters: map[string]*Cluster{},
		failNext: map[string]int{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /launch", s.launch)
	mux.HandleFunc("POST /status", s.status)
	mux.HandleFunc("POST /queue", s.queue)
	mux.HandleFunc("POST /down", s.down)
	mux.HandleFunc("POST /logs", s.logs)
	mux.HandleFunc("POST /api/cancel", s.cancel)
	mux.HandleFunc("GET /api/status", s.apiStatus)
	mux.HandleFunc("GET /api/get", s.apiGet)
	s.Server = httptest.NewServer(s.guard(mux))
	return s
}

// guard enforces what the real server needs from every client call.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.calls = append(s.calls, r.Method+" "+r.URL.Path)
		code := s.failNext[r.URL.Path]
		delete(s.failNext, r.URL.Path)
		s.mu.Unlock()
		if code != 0 {
			http.Error(w, `{"detail":"injected"}`, code)
			return
		}
		if r.Header.Get("X-SkyPilot-API-Version") == "" || r.Header.Get("X-SkyPilot-Version") == "" {
			http.Error(w, `{"detail":"missing version headers"}`, http.StatusBadRequest)
			return
		}
		if s.Token != "" && r.Header.Get("Authorization") != "Bearer "+s.Token {
			http.Error(w, `{"detail":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		w.Header().Set("X-SkyPilot-API-Version", "56")
		w.Header().Set("X-SkyPilot-Version", "0.13.0")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) ts() float64 { return float64(s.now().UnixMilli()) / 1000 }

func (s *Server) newRequest(name, cluster string, body json.RawMessage) *request {
	s.seq++
	r := &request{ID: fmt.Sprintf("%08x-0000-4000-8000-%012x", s.seq, s.seq), Name: "sky." + name, Cluster: cluster, Status: "PENDING", CreatedAt: s.ts(), Body: body}
	s.requests[r.ID] = r
	return r
}

func (s *Server) finish(r *request, ret any) {
	t := s.ts()
	r.Status, r.Return, r.FinishedAt = "SUCCEEDED", ret, &t
}

func (s *Server) fail(r *request, typ, msg string) {
	t := s.ts()
	r.Status, r.ErrType, r.ErrMessage, r.FinishedAt = "FAILED", typ, msg, &t
}

type common struct {
	EnvVars map[string]string `json:"env_vars"`
}

// decode reads a body and refuses one without the user id, which the real
// server would otherwise fill from its own environment.
func decode(w http.ResponseWriter, r *http.Request, v any) (json.RawMessage, bool) {
	var raw json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		http.Error(w, `{"detail":"bad json"}`, http.StatusUnprocessableEntity)
		return nil, false
	}
	var c common
	_ = json.Unmarshal(raw, &c)
	if c.EnvVars["SKYPILOT_USER_ID"] == "" {
		http.Error(w, `{"detail":"env_vars.SKYPILOT_USER_ID missing"}`, http.StatusUnprocessableEntity)
		return nil, false
	}
	if err := json.Unmarshal(raw, v); err != nil {
		http.Error(w, `{"detail":"bad body"}`, http.StatusUnprocessableEntity)
		return nil, false
	}
	return raw, true
}

func accepted(w http.ResponseWriter, r *request) {
	w.Header().Set("X-Skypilot-Request-ID", r.ID)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte("null"))
}

func (s *Server) launch(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Task        string `json:"task"`
		ClusterName string `json:"cluster_name"`
	}
	raw, ok := decode(w, r, &b)
	if !ok {
		return
	}
	var task map[string]any
	if err := json.Unmarshal([]byte(b.Task), &task); err != nil {
		http.Error(w, `{"detail":"task is not a JSON document"}`, http.StatusUnprocessableEntity)
		return
	}
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	s.mu.Lock()
	defer s.mu.Unlock()
	req := s.newRequest("launch", b.ClusterName, raw)
	req.Status = "RUNNING"
	s.launches = append(s.launches, Launch{RequestID: req.ID, Cluster: b.ClusterName, Task: task, TaskRaw: b.Task, Body: body})
	accepted(w, req)
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ClusterNames       []string `json:"cluster_names"`
		Refresh            string   `json:"refresh"`
		IncludeCredentials *bool    `json:"include_credentials"`
	}
	raw, ok := decode(w, r, &b)
	if !ok {
		return
	}
	if b.IncludeCredentials == nil || *b.IncludeCredentials {
		http.Error(w, `{"detail":"test fake: refusing to return credentials"}`, http.StatusUnprocessableEntity)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	req := s.newRequest("status", "", raw)
	out := []map[string]any{}
	for _, n := range b.ClusterNames {
		c, ok := s.clusters[n]
		if !ok {
			continue
		}
		out = append(out, statusRecord(c))
	}
	s.finish(req, out)
	accepted(w, req)
}

// statusRecord is encoders.encode_status of a StatusResponse.
func statusRecord(c *Cluster) map[string]any {
	rec := map[string]any{
		"name": c.Name, "launched_at": c.StatusUpdatedAt, "status": c.Status, "autostop": 5, "to_down": true,
		"cluster_hash": "0", "cluster_ever_up": c.Status == "UP" || c.StatusUpdatedAt != 0,
		"status_updated_at": c.StatusUpdatedAt, "user_hash": "unifycmp", "user_name": "unify-compute",
		"workspace": "default", "is_managed": false, "nodes": 1, "labels": map[string]string{},
		"handle": elided, "storage_mounts_metadata": elided, "last_use": "unify-compute",
		"cluster_name_on_cloud": c.Name + "-2ea4",
	}
	if c.Cloud != "" {
		rec["cloud"] = c.Cloud
	}
	if c.Region != "" {
		rec["region"] = c.Region
	}
	if c.ResourcesStrFull != "" {
		rec["resources_str_full"] = c.ResourcesStrFull
		rec["resources_str"] = c.ResourcesStrFull
	}
	if c.Status == "INIT" {
		rec["init_kind"] = "launching"
	}
	return rec
}

func (s *Server) queue(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ClusterName string `json:"cluster_name"`
	}
	raw, ok := decode(w, r, &b)
	if !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	req := s.newRequest("queue", b.ClusterName, raw)
	c, ok := s.clusters[b.ClusterName]
	switch {
	case !ok:
		s.fail(req, "ClusterDoesNotExist", fmt.Sprintf("\x1b[33mCluster '%s' does not exist.\x1b[0m", b.ClusterName))
	case c.Status != "UP":
		s.fail(req, "ClusterNotUpError", fmt.Sprintf("Cluster '%s' is not up.", b.ClusterName))
	default:
		jobs := []map[string]any{}
		for _, j := range c.Jobs {
			jobs = append(jobs, map[string]any{
				"job_id": j.ID, "job_name": c.Name, "username": "unify-compute", "user_hash": "unifycmp",
				"submitted_at": s.ts(), "start_at": j.StartAt, "end_at": j.EndAt, "resources": "1x[A10G:1]",
				"status": j.Status, "log_path": "~/sky_logs/sky-x", "metadata": map[string]any{},
			})
		}
		s.finish(req, jobs)
	}
	accepted(w, req)
}

func (s *Server) down(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ClusterName string `json:"cluster_name"`
	}
	raw, ok := decode(w, r, &b)
	if !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	req := s.newRequest("down", b.ClusterName, raw)
	_, exists := s.clusters[b.ClusterName]
	switch {
	case s.downFailures > 0:
		s.downFailures--
		s.fail(req, "CommandError", "Command failed with code 255: ssh: connect to host 10.0.0.1 port 22: Connection timed out")
	case !exists:
		s.fail(req, "ClusterDoesNotExist", fmt.Sprintf("Cluster '%s' does not exist.", b.ClusterName))
	default:
		delete(s.clusters, b.ClusterName)
		s.finish(req, nil)
	}
	accepted(w, req)
}

func (s *Server) logs(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ClusterName string `json:"cluster_name"`
	}
	raw, ok := decode(w, r, &b)
	if !ok {
		return
	}
	s.mu.Lock()
	req := s.newRequest("logs", b.ClusterName, raw)
	c, exists := s.clusters[b.ClusterName]
	text := ""
	if exists {
		text = c.Logs
		s.finish(req, 0)
	} else {
		s.fail(req, "ClusterDoesNotExist", fmt.Sprintf("\x1b[33mCluster '%s' does not exist.\x1b[0m", b.ClusterName))
	}
	s.mu.Unlock()
	w.Header().Set("X-Skypilot-Request-ID", req.ID)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if text != "" {
		_, _ = w.Write([]byte("<heartbeat></heartbeat>\n" + text))
	}
}

func (s *Server) cancel(w http.ResponseWriter, r *http.Request) {
	var b struct {
		RequestIDs []string `json:"request_ids"`
	}
	raw, ok := decode(w, r, &b)
	if !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	req := s.newRequest("api_cancel", "", raw)
	var killed []string
	for _, id := range b.RequestIDs {
		if t, ok := s.requests[id]; ok && (t.Status == "PENDING" || t.Status == "RUNNING") {
			now := s.ts()
			t.Status, t.FinishedAt = "CANCELLED", &now
			killed = append(killed, id)
		}
	}
	if killed == nil {
		killed = []string{}
	}
	s.finish(req, killed)
	accepted(w, req)
}

func optString(v string) any {
	if v == "" {
		return nil
	}
	return v
}

// payload is payloads.RequestPayload. full is encode() (for /api/get);
// otherwise readable_encode() (for /api/status), which nulls return_value
// and error.
func (r *request) payload(full bool) map[string]any {
	rv, er := "null", "null"
	body := string(r.Body)
	entry := strings.TrimPrefix(r.Name, "sky.")
	if full {
		b, _ := json.Marshal(r.Return)
		rv = string(b)
		if r.ErrType != "" {
			e, _ := json.Marshal(map[string]string{"object": elided, "type": r.ErrType, "message": r.ErrMessage})
			er = string(e)
		}
		body, entry = elided, elided
	}
	return map[string]any{
		"request_id": r.ID, "name": r.Name, "entrypoint": entry, "request_body": body,
		"status": r.Status, "created_at": r.CreatedAt, "user_id": "unifycmp", "return_value": rv,
		"error": er, "pid": nil, "schedule_type": "short", "user_name": "unify-compute",
		"cluster_name": optString(r.Cluster), "status_msg": nil, "should_retry": false,
		"finished_at": r.FinishedAt, "file_mounts_blob_id": nil,
	}
}

func (s *Server) apiStatus(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ids, cluster := q["request_ids"], q.Get("cluster_name")
	s.mu.Lock()
	var out []map[string]any
	var rs []*request
	for _, req := range s.requests {
		if len(ids) > 0 {
			for _, id := range ids {
				if strings.HasPrefix(req.ID, id) {
					rs = append(rs, req)
				}
			}
			continue
		}
		if cluster != "" && req.Cluster != cluster {
			continue
		}
		rs = append(rs, req)
	}
	sort.Slice(rs, func(i, j int) bool {
		return rs[i].CreatedAt > rs[j].CreatedAt || rs[i].CreatedAt == rs[j].CreatedAt && rs[i].ID > rs[j].ID
	})
	for _, req := range rs {
		out = append(out, req.payload(false))
	}
	s.mu.Unlock()
	if out == nil {
		out = []map[string]any{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// apiGet blocks until the request finishes, as the real one does.
func (s *Server) apiGet(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("request_id")
	for {
		s.mu.Lock()
		req, ok := s.requests[id]
		var p map[string]any
		done := ok && req.FinishedAt != nil
		if done {
			p = req.payload(true)
		}
		failed := ok && req.Status != "SUCCEEDED"
		s.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"detail": fmt.Sprintf("Request '%s' not found", id)})
			return
		}
		if done {
			w.Header().Set("Content-Type", "application/json")
			if failed {
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(map[string]any{"detail": p})
				return
			}
			_ = json.NewEncoder(w).Encode(p)
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(2 * time.Millisecond):
		}
	}
}

// --- scripting -----------------------------------------------------------

// Launches returns every /launch the fake has received.
func (s *Server) Launches() []Launch {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Launch(nil), s.launches...)
}

// Calls returns "METHOD /path" for every request received, in order.
func (s *Server) Calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

func (s *Server) launchFor(cluster string) *request {
	var newest *request
	for _, r := range s.requests {
		if r.Name == "sky.launch" && r.Cluster == cluster && (newest == nil || r.CreatedAt >= newest.CreatedAt && r.ID > newest.ID) {
			newest = r
		}
	}
	if newest == nil {
		panic("skypilottest: no launch for " + cluster)
	}
	return newest
}

// Provision creates the cluster in INIT, as SkyPilot does once instances are
// requested.
func (s *Server) Provision(cluster string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.launchFor(cluster)
	s.clusters[cluster] = &Cluster{Name: cluster, Status: "INIT"}
}

// Up marks the cluster UP on cloud/region with the resources string SkyPilot
// prints, e.g. "1x(gpus=A10G:1, cpus=4, mem=16, g5.xlarge, disk=256)". The
// launch request keeps running (setup, job submit).
func (s *Server) Up(cluster, cloud, region, resourcesStrFull string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.launchFor(cluster)
	c, ok := s.clusters[cluster]
	if !ok {
		c = &Cluster{Name: cluster}
		s.clusters[cluster] = c
	}
	c.Status, c.Cloud, c.Region, c.ResourcesStrFull = "UP", cloud, region, resourcesStrFull
	c.StatusUpdatedAt = s.now().Unix()
}

// SubmitJob finishes the launch request successfully with job id 1 queued on
// the cluster, which is what `sky launch` returns.
func (s *Server) SubmitJob(cluster string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.clusters[cluster]
	c.Jobs = append(c.Jobs, Job{ID: 1, Status: "PENDING"})
	s.finish(s.launchFor(cluster), map[string]any{"job_id": 1, "handle": elided})
}

// FailLaunch fails the launch request. keepCluster leaves the cluster as it
// is (INIT after a failed provision, UP after a failure in file mounts or
// setup), as SkyPilot does ("not torn down for debugging purposes");
// otherwise the cluster is gone, as after a provision that never got an
// instance.
func (s *Server) FailLaunch(cluster, errType, message string, keepCluster bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fail(s.launchFor(cluster), errType, message)
	if !keepCluster {
		delete(s.clusters, cluster)
	}
}

// SetJob sets job 1's status and node-recorded timestamps.
func (s *Server) SetJob(cluster, status string, startAt, endAt *time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.clusters[cluster]
	j := &c.Jobs[0]
	j.Status = status
	if startAt != nil {
		f := float64(startAt.UnixMilli()) / 1000
		j.StartAt = &f
	}
	if endAt != nil {
		f := float64(endAt.UnixMilli()) / 1000
		j.EndAt = &f
	}
}

func (s *Server) SetLogs(cluster, text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clusters[cluster].Logs = text
}

// Vanish removes the cluster without a /down: a spot preemption or an
// instance terminated outside SkyPilot, as a refreshing status sees it.
func (s *Server) Vanish(cluster string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.clusters, cluster)
}

// SetClusterStatus overrides the cluster's status (e.g. INIT after an
// abnormal-state refresh).
func (s *Server) SetClusterStatus(cluster, status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clusters[cluster].Status = status
}

func (s *Server) Exists(cluster string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.clusters[cluster]
	return ok
}

// LaunchStatus is the newest launch request's status for the cluster.
func (s *Server) LaunchStatus(cluster string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.launchFor(cluster).Status
}

// FailDowns makes the next n /down requests fail with an SSH error.
func (s *Server) FailDowns(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.downFailures = n
}

// FailNext makes the next request to path answer status without reaching
// the handler (a proxy error, a restart).
func (s *Server) FailNext(path string, status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failNext[path] = status
}

// SetClock replaces the fake's clock for timestamps it reports.
func (s *Server) SetClock(now func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = now
}
