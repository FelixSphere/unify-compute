// Package api serves the compute job API (docs/spec/job-api.md). Routes and
// shapes are the v1 contract; job handling returns not_implemented until the
// SkyPilot backend and the console bridge land.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/FelixSphere/unify-compute/compute-api/internal/sku"
	"github.com/FelixSphere/unify-compute/compute-api/internal/template"
	"github.com/FelixSphere/unify-compute/compute-api/internal/upstream"
)

// MaxRuntimeSeconds is the platform ceiling on a job's max_runtime_seconds:
// 24 hours. It bounds the hold a single submit can place.
const MaxRuntimeSeconds int64 = 24 * 60 * 60

const maxBody = 1 << 20

type Server struct {
	Build     string
	Catalogue *sku.Catalogue
	Upstreams *upstream.Registry
	// Templates is set after New so existing callers keep their signature; nil
	// serves an empty list and refuses every `template` on submit.
	Templates *template.Set
	Log       *slog.Logger
	mux       *http.ServeMux
}

func New(build string, cat *sku.Catalogue, reg *upstream.Registry, log *slog.Logger) *Server {
	s := &Server{Build: build, Catalogue: cat, Upstreams: reg, Log: log, mux: http.NewServeMux()}
	s.mux.HandleFunc("GET /healthz", s.health)
	s.mux.HandleFunc("GET /compute/v1/skus", s.listSKUs)
	s.mux.HandleFunc("GET /compute/v1/upstreams", s.listUpstreams)
	s.mux.HandleFunc("GET /compute/v1/templates", s.listTemplates)
	s.mux.Handle("POST /compute/v1/jobs", s.auth(http.HandlerFunc(s.submitJob)))
	s.mux.Handle("GET /compute/v1/jobs", s.auth(s.notImplemented("job listing")))
	s.mux.Handle("GET /compute/v1/jobs/{id}", s.auth(s.notImplemented("job lookup")))
	s.mux.Handle("POST /compute/v1/jobs/{id}/cancel", s.auth(s.notImplemented("job cancel")))
	s.mux.Handle("GET /compute/v1/jobs/{id}/logs", s.auth(s.notImplemented("job logs")))
	s.mux.Handle("GET /compute/v1/jobs/{id}/artifacts", s.auth(s.notImplemented("job artifacts")))
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	rec := &recorder{ResponseWriter: w, status: http.StatusOK}
	s.mux.ServeHTTP(rec, r)
	s.Log.Info("request", "method", r.Method, "path", r.URL.Path, "status", rec.status, "ms", time.Since(start).Milliseconds())
}

type recorder struct {
	http.ResponseWriter
	status int
}

func (r *recorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

type apiError struct {
	Type    string `json:"type"`
	Code    string `json:"code"`
	Message string `json:"message"`
	Param   string `json:"param,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, e apiError) {
	writeJSON(w, status, map[string]apiError{"error": e})
}

func invalid(w http.ResponseWriter, code, param, msg string) {
	writeError(w, http.StatusBadRequest, apiError{Type: "invalid_request", Code: code, Message: msg, Param: param})
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "service": "compute-api", "build": s.Build})
}

func (s *Server) listSKUs(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": s.Catalogue.SKUs, "note": s.Catalogue.Note})
}

type upstreamView struct {
	Object  string   `json:"object"`
	Cloud   string   `json:"cloud"`
	Enabled bool     `json:"enabled"`
	SKUs    []string `json:"skus"`
}

// listUpstreams is public for the same reason /skus is: the pricing page
// says where each SKU runs. `enabled` is this deployment's view.
func (s *Server) listUpstreams(w http.ResponseWriter, _ *http.Request) {
	clouds := s.Catalogue.Clouds()
	out := make([]upstreamView, 0, len(clouds))
	for c, ids := range clouds {
		out = append(out, upstreamView{Object: "compute.upstream", Cloud: c, Enabled: s.Upstreams.Enabled(c), SKUs: ids})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Cloud < out[j].Cloud })
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": out})
}

// listTemplates is public like /skus: a template is what a customer reads
// before they have a key worth using.
func (s *Server) listTemplates(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": s.Templates.List()})
}

// submitRequest decodes optional fields as pointers or nil-able collections so
// a template submit can tell "not sent" from "sent as zero".
type submitRequest struct {
	Template  string   `json:"template"`
	SKU       *string  `json:"sku"`
	Providers []string `json:"providers"`
	Spec      struct {
		Image             *string           `json:"image"`
		Command           []string          `json:"command"`
		Env               map[string]string `json:"env"`
		Inputs            []template.Input  `json:"inputs"`
		Outputs           []template.Output `json:"outputs"`
		MaxRuntimeSeconds *int64            `json:"max_runtime_seconds"`
	} `json:"spec"`
}

// submitJob validates everything the catalogue and registry can decide
// without persistence: the template and its overrides, the SKU, the
// customer's provider restriction, the runtime ceiling, and whether this
// deployment can launch it at all. A template is resolved into a spec first
// and that spec then meets exactly the checks a hand-written one does. What
// survives is what the backend will receive; until it exists the answer is
// still 501.
func (s *Server) submitJob(w http.ResponseWriter, r *http.Request) {
	var raw submitRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	if err := dec.Decode(&raw); err != nil {
		invalid(w, "malformed_body", "", "Body must be a JSON object; see docs/spec/job-api.md.")
		return
	}
	var (
		skuID     string
		providers = raw.Providers
		runtime   int64
	)
	if raw.Template == "" {
		if raw.SKU != nil {
			skuID = *raw.SKU
		}
		if raw.Spec.MaxRuntimeSeconds != nil {
			runtime = *raw.Spec.MaxRuntimeSeconds
		}
	} else {
		t, err := s.Templates.Get(raw.Template)
		if err != nil {
			invalid(w, "unknown_template", "template", fmt.Sprintf("template %q is not offered; see GET /compute/v1/templates", raw.Template))
			return
		}
		if !t.Available {
			invalid(w, "template_unavailable", "template", fmt.Sprintf("template %q is listed but not currently runnable: %s", t.ID, t.UnavailableReason))
			return
		}
		res, err := t.Resolve(template.Overrides{
			SKU: raw.SKU, Providers: raw.Providers, Image: raw.Spec.Image, Command: raw.Spec.Command,
			Env: raw.Spec.Env, Inputs: raw.Spec.Inputs, Outputs: raw.Spec.Outputs, MaxRuntimeSeconds: raw.Spec.MaxRuntimeSeconds,
		})
		if err != nil {
			var fe *template.FieldError
			if errors.As(err, &fe) {
				invalid(w, fe.Code, fe.Field, fe.Msg)
				return
			}
			writeError(w, http.StatusInternalServerError, apiError{Type: "internal_error", Code: "template_resolution_failed", Message: err.Error()})
			return
		}
		skuID, providers, runtime = res.SKU, res.Providers, res.Spec.MaxRuntimeSeconds
	}
	if skuID == "" {
		invalid(w, "missing_sku", "sku", "Choose a sku from GET /compute/v1/skus.")
		return
	}
	item, err := s.Catalogue.Get(skuID)
	if err != nil {
		invalid(w, "unknown_sku", "sku", fmt.Sprintf("sku %q is not offered", skuID))
		return
	}
	if !item.Available {
		invalid(w, "sku_unavailable", "sku", fmt.Sprintf("sku %q is listed but not currently sold", skuID))
		return
	}
	switch rt := runtime; {
	case rt <= 0:
		invalid(w, "missing_max_runtime", "spec.max_runtime_seconds", "spec.max_runtime_seconds must be a positive integer")
		return
	case rt > MaxRuntimeSeconds:
		invalid(w, "runtime_over_ceiling", "spec.max_runtime_seconds", fmt.Sprintf("max_runtime_seconds %d exceeds the platform ceiling of %d", rt, MaxRuntimeSeconds))
		return
	}
	if _, _, err := s.Upstreams.Place(item, providers); err != nil {
		switch {
		case errors.Is(err, sku.ErrProviderNotOffered):
			invalid(w, "provider_not_offered", "providers", fmt.Sprintf("sku %q: %v", skuID, err))
		case errors.Is(err, upstream.ErrNoEnabledProvider):
			writeError(w, http.StatusServiceUnavailable, apiError{Type: "upstream_unavailable", Code: "upstream_unavailable",
				Message: fmt.Sprintf("sku %q has no upstream enabled in this deployment", skuID)})
		default:
			writeError(w, http.StatusInternalServerError, apiError{Type: "internal_error", Code: "placement_failed", Message: err.Error()})
		}
		return
	}
	s.notImplemented("job submission").ServeHTTP(w, r)
}

// auth only checks the credential's shape for now. Verification against the
// console bridge replaces the body of this function; the 401 contract stays.
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Get("Authorization")
		key, ok := strings.CutPrefix(h, "Bearer ")
		key = strings.TrimSpace(key)
		if !ok || !strings.HasPrefix(key, "sk-") || len(key) < 8 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="compute"`)
			writeError(w, http.StatusUnauthorized, apiError{
				Type:    "authentication_error",
				Code:    "missing_key",
				Message: "Provide your UnifyAPI key as 'Authorization: Bearer sk-...'.",
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) notImplemented(what string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotImplemented, apiError{
			Type:    "not_implemented",
			Code:    "not_implemented",
			Message: what + " is not available yet; see docs/spec/job-api.md for the contract.",
		})
	})
}
