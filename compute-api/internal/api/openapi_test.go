package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/FelixSphere/unify-compute/compute-api/internal/sku"
	"github.com/FelixSphere/unify-compute/compute-api/internal/upstream"
)

var openAPIMethods = []string{"get", "put", "post", "delete", "options", "head", "patch", "trace"}

func loadSpec(t *testing.T) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(openAPIDoc, &doc); err != nil {
		t.Fatalf("embedded openapi.json is not JSON: %v", err)
	}
	return doc
}

func specOperations(t *testing.T, doc map[string]any) map[string]map[string]any {
	t.Helper()
	ops := map[string]map[string]any{}
	for path, item := range doc["paths"].(map[string]any) {
		for _, m := range openAPIMethods {
			if op, ok := item.(map[string]any)[m]; ok {
				ops[strings.ToUpper(m)+" "+path] = op.(map[string]any)
			}
		}
	}
	return ops
}

func TestOpenAPIIsServedPublicly(t *testing.T) {
	rec := do(t, newTestServer(t, "aws"), http.MethodGet, "/compute/v1/openapi.json", "", "")
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("%d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if !bytes.Equal(rec.Body.Bytes(), openAPIDoc) {
		t.Fatal("served document differs from the embedded one")
	}
	if v := loadSpec(t)["openapi"]; v != "3.1.0" {
		t.Fatalf("openapi version %v", v)
	}
}

// The canonical document is compute-api/internal/api/openapi.json (go:embed
// cannot reach outside the module). docs/spec/openapi.json is the copy people
// link to; it must never drift.
func TestDocsCopyOfOpenAPIIsIdentical(t *testing.T) {
	docs, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "spec", "openapi.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(docs, openAPIDoc) {
		t.Fatal("docs/spec/openapi.json differs from compute-api/internal/api/openapi.json; run: cp compute-api/internal/api/openapi.json docs/spec/openapi.json")
	}
}

// TestRoutesAndOpenAPIAgree holds the mux's registered patterns against the
// document's paths, in both directions. Adding a route without its OpenAPI
// entry, or documenting one that does not exist, fails here.
func TestRoutesAndOpenAPIAgree(t *testing.T) {
	s := newTestServer(t, "aws")
	ops := specOperations(t, loadSpec(t))

	registered := map[string]bool{}
	for _, p := range s.mux.patterns {
		method, path, ok := strings.Cut(p, " ")
		if !ok || method == "" || !strings.HasPrefix(path, "/") {
			t.Errorf("route %q has no method or has a host; the OpenAPI check needs \"METHOD /path\"", p)
			continue
		}
		if strings.Contains(path, "...}") {
			t.Errorf("route %q uses a {rest...} wildcard, which OpenAPI cannot express", p)
		}
		registered[p] = true
		if _, ok := ops[p]; !ok {
			t.Errorf("route %q is registered but has no entry in compute-api/internal/api/openapi.json", p)
		}
	}
	for op := range ops {
		if !registered[op] {
			t.Errorf("openapi.json documents %q but the server registers no such route", op)
		}
	}

	// Belt and braces: the mux itself must resolve each documented operation
	// to the pattern of the same name, so a path-parameter typo is caught.
	param := regexp.MustCompile(`\{[^}]+\}`)
	for op := range ops {
		method, path, _ := strings.Cut(op, " ")
		req := httptest.NewRequest(method, param.ReplaceAllString(path, "x"), nil)
		if _, pattern := s.mux.Handler(req); pattern != op {
			t.Errorf("%s resolves to %q", op, pattern)
		}
	}
}

func TestOpenAPIRefsResolve(t *testing.T) {
	doc := loadSpec(t)
	var walk func(v any)
	walk = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			if r, ok := v["$ref"].(string); ok {
				if _, err := resolveRef(doc, r); err != nil {
					t.Error(err)
				}
			}
			for _, c := range v {
				walk(c)
			}
		case []any:
			for _, c := range v {
				walk(c)
			}
		}
	}
	walk(doc)
}

// TestOpenAPIExamplesMatchSchemas keeps hand-written examples honest.
func TestOpenAPIExamplesMatchSchemas(t *testing.T) {
	doc := loadSpec(t)
	var walk func(where string, v any)
	walk = func(where string, v any) {
		switch v := v.(type) {
		case map[string]any:
			if ex, ok := v["example"]; ok {
				if schema, ok := v["schema"]; ok {
					for _, e := range validate(doc, schema, ex, "example") {
						t.Errorf("%s: %s", where, e)
					}
				}
			}
			for k, c := range v {
				walk(where+"/"+k, c)
			}
		case []any:
			for i, c := range v {
				walk(fmt.Sprintf("%s/%d", where, i), c)
			}
		}
	}
	walk("#", doc)
}

// TestHandlerResponsesMatchOpenAPI drives the real handlers and checks each
// response against the document: the status is listed for the operation, the
// body fits its schema with no undocumented fields, and every error
// type/code pair the code emits is listed in Error.x-error-codes.
//
// The other direction, that every listed code is produced by something, needs
// the codes only a build with a job store emits, so TestEndToEnd checks it
// against these servers and its own scenarios together. It therefore runs
// only where COMPUTE_TEST_DATABASE_URL is set, as it is in CI.
func TestHandlerResponsesMatchOpenAPI(t *testing.T) {
	doc := loadSpec(t)
	dbLessErrorCodes(t, doc)
}

// dbLessErrorCodes drives the handlers of servers built without a job store,
// holds every response against the document, and returns the error
// type/code pairs they produced.
func dbLessErrorCodes(t *testing.T, doc map[string]any) map[string]bool {
	t.Helper()
	ops := specOperations(t, doc)
	codes := errorCodes(t, doc)

	real, err := sku.Load(filepath.Join("..", "..", "config", "skus.json"))
	if err != nil {
		t.Fatal(err)
	}
	reg, _ := upstream.Parse("aws")
	servers := map[string]*Server{
		"test catalogue":    newTestServer(t, "aws"),
		"empty registry":    newTestServer(t, ""),
		"shipped catalogue": New("test", real, reg, slog.New(slog.NewTextHandler(io.Discard, nil))),
	}

	type call struct{ method, path, key, body string }
	calls := []call{
		{"GET", "/healthz", "", ""},
		{"GET", "/compute/v1/openapi.json", "", ""},
		{"GET", "/compute/v1/skus", "", ""},
		{"GET", "/compute/v1/upstreams", "", ""},
	}
	for _, body := range []string{
		``, `not json`, `{"spec":{"max_runtime_seconds":10}}`,
		`{"sku":"h100-80gb-x1","spec":{"max_runtime_seconds":10}}`,
		`{"sku":"l4-24gb-x1","spec":{"max_runtime_seconds":10}}`,
		`{"sku":"a10g-24gb-x1","spec":{}}`,
		`{"sku":"a10g-24gb-x1","spec":{"max_runtime_seconds":86401}}`,
		`{"sku":"a10g-24gb-x1","providers":["azure"],"spec":{"max_runtime_seconds":10}}`,
		`{"sku":"a10g-24gb-x1","providers":["gcp"],"spec":{"max_runtime_seconds":10}}`,
		validSubmit,
	} {
		calls = append(calls, call{"POST", "/compute/v1/jobs", "sk-abcdefghij", body})
	}
	for op := range ops {
		if ops[op]["security"] != nil {
			continue
		}
		method, path, _ := strings.Cut(op, " ")
		path = strings.ReplaceAll(path, "{id}", "job_01J8Q6ZK3R7Y9W2X4V6T8S0A1B")
		calls = append(calls, call{method, path, "", ""}, call{method, path, "sk-", ""}, call{method, path, "sk-abcdefghij", validSubmit})
	}

	seen := map[string]bool{}
	for name, s := range servers {
		for _, c := range calls {
			req := httptest.NewRequest(c.method, c.path, strings.NewReader(c.body))
			if c.key != "" {
				req.Header.Set("Authorization", "Bearer "+c.key)
			}
			_, pattern := s.mux.Handler(req)
			rec := httptest.NewRecorder()
			s.ServeHTTP(rec, req)
			where := fmt.Sprintf("%s: %s %s %q -> %d", name, c.method, c.path, c.body, rec.Code)

			op, ok := ops[pattern]
			if !ok {
				t.Errorf("%s: pattern %q not documented", where, pattern)
				continue
			}
			responses := op["responses"].(map[string]any)
			resp, ok := responses[fmt.Sprint(rec.Code)]
			if !ok {
				t.Errorf("%s: status not documented for %s", where, pattern)
				continue
			}
			resp, err := deref(doc, resp)
			if err != nil {
				t.Fatal(err)
			}
			ct := rec.Header().Get("Content-Type")
			content, _ := resp.(map[string]any)["content"].(map[string]any)
			media, ok := content[ct].(map[string]any)
			if !ok {
				t.Errorf("%s: content type %q not documented", where, ct)
				continue
			}
			var body any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Errorf("%s: body is not JSON: %v", where, err)
				continue
			}
			for _, e := range validate(doc, media["schema"], body, "body") {
				t.Errorf("%s: %s", where, e)
			}
			if env, ok := body.(map[string]any)["error"].(map[string]any); ok {
				pair := fmt.Sprintf("%v/%v", env["type"], env["code"])
				seen[pair] = true
				if !codes[pair] {
					t.Errorf("%s: error %s is not listed in Error.x-error-codes", where, pair)
				}
			}
		}
	}

	return seen
}

// unproducedErrorCodes returns the listed codes no source produced. Listed
// codes the handlers no longer emit are stale documentation; planned types
// list none.
func unproducedErrorCodes(t *testing.T, doc map[string]any, sources ...map[string]bool) []string {
	t.Helper()
	var stale []string
	for pair := range errorCodes(t, doc) {
		produced := pair == "internal_error/placement_failed"
		for _, seen := range sources {
			produced = produced || seen[pair]
		}
		if !produced {
			stale = append(stale, pair)
		}
	}
	sort.Strings(stale)
	return stale
}

func errorCodes(t *testing.T, doc map[string]any) map[string]bool {
	t.Helper()
	schema, err := resolveRef(doc, "#/components/schemas/Error")
	if err != nil {
		t.Fatal(err)
	}
	m := schema.(map[string]any)
	types := map[string]bool{}
	for _, v := range m["properties"].(map[string]any)["type"].(map[string]any)["enum"].([]any) {
		types[v.(string)] = true
	}
	out := map[string]bool{}
	for typ, list := range m["x-error-codes"].(map[string]any) {
		if !types[typ] {
			t.Errorf("x-error-codes names type %q which is not in Error.type's enum", typ)
		}
		for _, c := range list.([]any) {
			out[typ+"/"+c.(string)] = true
		}
	}
	return out
}

func resolveRef(doc map[string]any, ref string) (any, error) {
	if !strings.HasPrefix(ref, "#/") {
		return nil, fmt.Errorf("only local refs are supported: %q", ref)
	}
	var cur any = doc
	for _, part := range strings.Split(ref[2:], "/") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("unresolvable ref %q", ref)
		}
		if cur, ok = m[part]; !ok {
			return nil, fmt.Errorf("unresolvable ref %q", ref)
		}
	}
	return cur, nil
}

func deref(doc map[string]any, v any) (any, error) {
	for {
		m, ok := v.(map[string]any)
		if !ok {
			return v, nil
		}
		r, ok := m["$ref"].(string)
		if !ok {
			return v, nil
		}
		var err error
		if v, err = resolveRef(doc, r); err != nil {
			return nil, err
		}
	}
}

// validate is a structural JSON Schema check covering the keywords this
// document uses. It is stricter than JSON Schema in one way on purpose: an
// object schema that lists properties and says nothing about
// additionalProperties rejects undeclared fields, so a field added to a
// response without documenting it fails the test.
func validate(doc map[string]any, schema, v any, at string) []string {
	schema, err := deref(doc, schema)
	if err != nil {
		return []string{err.Error()}
	}
	s, ok := schema.(map[string]any)
	if !ok {
		return []string{fmt.Sprintf("%s: schema is not an object", at)}
	}
	var errs []string
	fail := func(format string, args ...any) { errs = append(errs, at+": "+fmt.Sprintf(format, args...)) }

	if alts, ok := s["anyOf"].([]any); ok {
		if !anyMatches(doc, alts, v, at, false) {
			fail("matches no anyOf alternative: %v", v)
		}
	}
	if alts, ok := s["oneOf"].([]any); ok {
		if !anyMatches(doc, alts, v, at, true) {
			fail("does not match exactly one oneOf alternative: %v", v)
		}
	}
	if c, ok := s["const"]; ok && !jsonEqual(c, v) {
		fail("want const %v, got %v", c, v)
	}
	if enum, ok := s["enum"].([]any); ok {
		found := false
		for _, e := range enum {
			found = found || jsonEqual(e, v)
		}
		if !found {
			fail("%v not in enum %v", v, enum)
		}
	}
	if t, ok := s["type"]; ok {
		var types []string
		switch t := t.(type) {
		case string:
			types = []string{t}
		case []any:
			for _, x := range t {
				types = append(types, x.(string))
			}
		}
		match := false
		for _, typ := range types {
			match = match || hasType(typ, v)
		}
		if !match {
			fail("want type %v, got %T (%v)", types, v, v)
			return errs
		}
	}

	switch v := v.(type) {
	case map[string]any:
		props, _ := s["properties"].(map[string]any)
		for _, r := range asStrings(s["required"]) {
			if _, ok := v[r]; !ok {
				fail("missing required %q", r)
			}
		}
		if max, ok := s["maxProperties"].(float64); ok && float64(len(v)) > max {
			fail("%d properties, max %v", len(v), max)
		}
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if ps, ok := props[k]; ok {
				errs = append(errs, validate(doc, ps, v[k], at+"."+k)...)
			} else if ap, ok := s["additionalProperties"]; ok {
				if apm, ok := ap.(map[string]any); ok {
					errs = append(errs, validate(doc, apm, v[k], at+"."+k)...)
				} else if ap == false {
					fail("undeclared property %q", k)
				}
			} else if props != nil {
				fail("undeclared property %q (document it in openapi.json)", k)
			}
		}
	case []any:
		if items, ok := s["items"]; ok {
			for i, x := range v {
				errs = append(errs, validate(doc, items, x, fmt.Sprintf("%s[%d]", at, i))...)
			}
		}
	case string:
		if p, ok := s["pattern"].(string); ok && !regexp.MustCompile(p).MatchString(v) {
			fail("%q does not match %s", v, p)
		}
		if n, ok := s["minLength"].(float64); ok && float64(len(v)) < n {
			fail("shorter than %v", n)
		}
		if n, ok := s["maxLength"].(float64); ok && float64(len(v)) > n {
			fail("longer than %v", n)
		}
	case float64:
		if n, ok := s["minimum"].(float64); ok && v < n {
			fail("%v below minimum %v", v, n)
		}
		if n, ok := s["maximum"].(float64); ok && v > n {
			fail("%v above maximum %v", v, n)
		}
	}
	return errs
}

func anyMatches(doc map[string]any, alts []any, v any, at string, exactlyOne bool) bool {
	n := 0
	for _, a := range alts {
		if len(validate(doc, a, v, at)) == 0 {
			n++
		}
	}
	if exactlyOne {
		return n == 1
	}
	return n > 0
}

func hasType(typ string, v any) bool {
	switch typ {
	case "null":
		return v == nil
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "string":
		_, ok := v.(string)
		return ok
	case "number":
		_, ok := v.(float64)
		return ok
	case "integer":
		f, ok := v.(float64)
		return ok && f == math.Trunc(f)
	case "array":
		_, ok := v.([]any)
		return ok
	case "object":
		_, ok := v.(map[string]any)
		return ok
	}
	return false
}

func asStrings(v any) []string {
	var out []string
	for _, x := range asSlice(v) {
		out = append(out, x.(string))
	}
	return out
}

func asSlice(v any) []any { s, _ := v.([]any); return s }

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

// TestValidatorCatchesDrift proves the structural check is not vacuous.
func TestValidatorCatchesDrift(t *testing.T) {
	doc := loadSpec(t)
	ref := map[string]any{"$ref": "#/components/schemas/Upstream"}
	good := map[string]any{"object": "compute.upstream", "cloud": "aws", "enabled": true, "skus": []any{"a"}}
	if errs := validate(doc, ref, good, "u"); len(errs) != 0 {
		t.Fatalf("valid upstream rejected: %v", errs)
	}
	for name, bad := range map[string]map[string]any{
		"missing field":      {"object": "compute.upstream", "cloud": "aws", "skus": []any{}},
		"undocumented field": {"object": "compute.upstream", "cloud": "aws", "enabled": true, "skus": []any{}, "region": "x"},
		"wrong const":        {"object": "upstream", "cloud": "aws", "enabled": true, "skus": []any{}},
		"wrong item type":    {"object": "compute.upstream", "cloud": "aws", "enabled": true, "skus": []any{1.0}},
	} {
		if len(validate(doc, ref, bad, "u")) == 0 {
			t.Errorf("%s: accepted %v", name, bad)
		}
	}
	errRef := map[string]any{"$ref": "#/components/schemas/ErrorEnvelope"}
	if len(validate(doc, errRef, map[string]any{"error": map[string]any{"type": "teapot", "code": "x", "message": "m"}}, "e")) == 0 {
		t.Error("unknown error type accepted")
	}
}
