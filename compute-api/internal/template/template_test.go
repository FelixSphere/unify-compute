package template

import (
	"errors"
	"strings"
	"testing"

	"github.com/FelixSphere/unify-compute/compute-api/internal/sku"
)

const ceiling = 86400

const digest = "@sha256:a67f8f186d4567612ac37a55bd82295006002b18858eb12a8af2f05f86c2ae3f"

func shippedCatalogue(t *testing.T) *sku.Catalogue {
	t.Helper()
	cat, err := sku.Load("../../config/skus.json")
	if err != nil {
		t.Fatal(err)
	}
	return cat
}

func testCatalogue(t *testing.T) *sku.Catalogue {
	t.Helper()
	cat, err := sku.Parse([]byte(`{"skus":[
	  {"id":"on","price_usd_per_hour":"1","available":true,"tier":"hyperscaler","accelerator":"A10G","providers":[{"cloud":"aws","instance_type":"g5.xlarge"}]},
	  {"id":"off","price_usd_per_hour":"1","available":false}
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	return cat
}

// good returns a valid template body with one field replaced, so each bad
// case below differs from a passing file in exactly one way.
func good(field, value string) string {
	f := map[string]string{
		"id":                  `"t"`,
		"title":               `"T"`,
		"description":         `"D"`,
		"image":               `"vllm/vllm-openai:v0.30.0-cu129` + digest + `"`,
		"command":             `["true"]`,
		"env":                 `{"MODEL":"m"}`,
		"sku":                 `"on"`,
		"max_runtime_seconds": `600`,
		"inputs":              `[{"mount":"/mnt/in","description":"in"}]`,
		"outputs":             `[{"mount":"/mnt/out","description":"out"}]`,
		"overridable":         `["sku","spec.env","spec.max_runtime_seconds"]`,
		"available":           `true`,
	}
	if field != "" {
		f[field] = value
	}
	var parts []string
	for k, v := range f {
		if v != "" {
			parts = append(parts, `"`+k+`":`+v)
		}
	}
	return `{"templates":[{` + strings.Join(parts, ",") + `}]}`
}

func TestShippedTemplatesParseAgainstShippedCatalogue(t *testing.T) {
	s, err := Load("../../config/templates.json", shippedCatalogue(t), ceiling)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"batch-embed", "batch-infer-vllm", "finetune-lora"}
	if len(s.Templates) != len(want) {
		t.Fatalf("got %d templates, want %v", len(s.Templates), want)
	}
	for i, id := range want {
		got := s.Templates[i]
		if got.ID != id || got.Object != "compute.template" {
			t.Fatalf("template %d: %+v", i, got)
		}
		if got.Available && got.ImageLicence == "" {
			t.Errorf("%s: an available template must record its image licence", id)
		}
		if !got.Available && got.UnavailableReason == "" {
			t.Errorf("%s: unavailable without a reason", id)
		}
	}
}

// finetune-lora is the template form of finetune/sky/finetune.yaml. These are
// the parts of that task the template must keep: if one changes there, it
// changes here in the same review.
func TestFinetuneLoraMatchesTheReferenceRun(t *testing.T) {
	s, err := Load("../../config/templates.json", shippedCatalogue(t), ceiling)
	if err != nil {
		t.Fatal(err)
	}
	ft, err := s.Get("finetune-lora")
	if err != nil {
		t.Fatal(err)
	}
	if ft.Available || ft.Image != "" {
		t.Fatalf("finetune-lora has no published image yet and must stay unavailable: %+v", ft)
	}
	if ft.SKU != "a10g-24gb-x1-spot" || ft.MaxRuntimeSeconds != 7200 {
		t.Fatalf("reference run is one A10G spot for 7200 s by default, got %s / %d", ft.SKU, ft.MaxRuntimeSeconds)
	}
	if ft.Env["BASE_MODEL"] != "Qwen/Qwen3-4B-Instruct-2507" || ft.Env["FEW_SHOT_GATE"] != "0.95" {
		t.Fatalf("env defaults drifted from finetune.yaml: %v", ft.Env)
	}
	if _, ok := ft.Env["HF_TOKEN"]; ok {
		t.Fatal("a secret has no place in a template's env defaults")
	}
	script := strings.Join(ft.Command, " ")
	for _, step := range []string{
		"eval.py --model \"$BASE_MODEL\" --test /mnt/in/test.jsonl $SCHEMA_ARG --out /mnt/out/eval_zero_shot.json",
		"--few-shot 3",
		"task_not_worth_finetuning",
		"train.py --model \"$BASE_MODEL\" --train /mnt/in/train.jsonl --val /mnt/in/val.jsonl --out /mnt/out/adapter",
		"--lora /mnt/out/adapter",
		"echo completed > /mnt/out/RESULT",
	} {
		if !strings.Contains(script, step) {
			t.Errorf("command lost a step of the reference run: %q", step)
		}
	}
	if len(ft.Inputs) != 1 || ft.Inputs[0].Mount != "/mnt/in" || len(ft.Outputs) != 1 || ft.Outputs[0].Mount != "/mnt/out" {
		t.Fatalf("mounts: %+v %+v", ft.Inputs, ft.Outputs)
	}
}

func TestGoodFixtureParses(t *testing.T) {
	if _, err := Parse([]byte(good("", "")), testCatalogue(t), ceiling); err != nil {
		t.Fatal(err)
	}
}

func TestParseRefusesBadTemplates(t *testing.T) {
	cases := []struct{ name, field, value, want string }{
		{"unknown sku", "sku", `"h100"`, "not in the catalogue"},
		{"tag only", "image", `"vllm/vllm-openai:v0.30.0"`, "pinned by @sha256"},
		{"no tag no digest", "image", `"vllm/vllm-openai"`, "pinned by @sha256"},
		{"short digest", "image", `"vllm/vllm-openai@sha256:abc"`, "pinned by @sha256"},
		{"uppercase repo", "image", `"VLLM/vllm-openai` + digest + `"`, "pinned by @sha256"},
		{"latest", "image", `"vllm/vllm-openai:latest` + digest + `"`, "latest"},
		{"image overridable", "overridable", `["spec.image"]`, "cannot be overridable"},
		{"outputs overridable", "overridable", `["spec.outputs"]`, "cannot be overridable"},
		{"inputs overridable", "overridable", `["spec.inputs"]`, "cannot be overridable"},
		{"unknown overridable", "overridable", `["gpu_count"]`, "cannot be overridable"},
		{"overridable twice", "overridable", `["sku","sku"]`, "twice"},
		{"available without image", "image", `""`, "no image"},
		{"available without command", "command", `[]`, "no command"},
		{"runtime zero", "max_runtime_seconds", `0`, "max_runtime_seconds"},
		{"runtime over ceiling", "max_runtime_seconds", `86401`, "max_runtime_seconds"},
		{"relative mount", "inputs", `[{"mount":"mnt/in"}]`, "absolute"},
		{"root mount", "outputs", `[{"mount":"/"}]`, "absolute"},
		{"unclean mount", "outputs", `[{"mount":"/mnt/out/"}]`, "absolute"},
		{"nested mounts", "outputs", `[{"mount":"/mnt/in/out"}]`, "overlap"},
		{"same mount", "outputs", `[{"mount":"/mnt/in"}]`, "overlap"},
		{"bad env key", "env", `{"MY-VAR":"x"}`, "shell identifier"},
		{"bad id", "id", `"Batch Embed"`, "slug"},
		{"no title", "title", `""`, "title"},
		{"typo field", "overrideable", `["sku"]`, "unknown field"},
	}
	for _, tc := range cases {
		_, err := Parse([]byte(good(tc.field, tc.value)), testCatalogue(t), ceiling)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want error containing %q", tc.name, err, tc.want)
		}
	}
}

func TestParseRefusesDuplicatesAndSilentUnavailability(t *testing.T) {
	cat := testCatalogue(t)
	one := strings.TrimSuffix(strings.TrimPrefix(good("", ""), `{"templates":[`), `]}`)
	if _, err := Parse([]byte(`{"templates":[`+one+`,`+one+`]}`), cat, ceiling); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("duplicate id: %v", err)
	}
	unavail := good("available", "false")
	if _, err := Parse([]byte(unavail), cat, ceiling); err == nil || !strings.Contains(err.Error(), "unavailable_reason") {
		t.Errorf("unavailable without reason: %v", err)
	}
}

func TestPlaceholderMayOmitImageButNotSKU(t *testing.T) {
	cat := testCatalogue(t)
	body := strings.Replace(good("image", `""`), `"available":true`, `"available":false,"unavailable_reason":"no image yet"`, 1)
	s, err := Parse([]byte(body), cat, ceiling)
	if err != nil {
		t.Fatal(err)
	}
	if s.Templates[0].Available {
		t.Fatal("placeholder became available")
	}
	body = strings.Replace(body, `"sku":"on"`, `"sku":"gone"`, 1)
	if _, err := Parse([]byte(body), cat, ceiling); err == nil {
		t.Fatal("a placeholder must still name a SKU that exists")
	}
}

func TestUnsoldDefaultSKUMakesTemplateUnavailable(t *testing.T) {
	s, err := Parse([]byte(good("sku", `"off"`)), testCatalogue(t), ceiling)
	if err != nil {
		t.Fatal(err)
	}
	got := s.Templates[0]
	if got.Available || !strings.Contains(got.UnavailableReason, `"off"`) {
		t.Fatalf("want unavailable naming the sku, got %+v", got)
	}
}

func parsedGood(t *testing.T) Template {
	t.Helper()
	s, err := Parse([]byte(good("", "")), testCatalogue(t), ceiling)
	if err != nil {
		t.Fatal(err)
	}
	return s.Templates[0]
}

func ptr[T any](v T) *T { return &v }

var inputs = []Input{{Source: "s3://b/in/", Mount: "/mnt/in"}}

func TestResolveFillsDefaultsAndMergesEnv(t *testing.T) {
	tpl := parsedGood(t)
	r, err := tpl.Resolve(Overrides{Inputs: inputs, Env: map[string]string{"EXTRA": "1"}})
	if err != nil {
		t.Fatal(err)
	}
	if r.SKU != "on" || r.Spec.MaxRuntimeSeconds != 600 || r.Spec.Image != tpl.Image || r.Spec.Command[0] != "true" {
		t.Fatalf("defaults not applied: %+v", r)
	}
	if r.Spec.Env["MODEL"] != "m" || r.Spec.Env["EXTRA"] != "1" {
		t.Fatalf("env should merge key-wise: %v", r.Spec.Env)
	}
	if len(r.Spec.Outputs) != 1 || r.Spec.Outputs[0].Mount != "/mnt/out" || len(r.Spec.Inputs) != 1 {
		t.Fatalf("mounts: %+v", r.Spec)
	}
	r2, err := tpl.Resolve(Overrides{Inputs: inputs, SKU: ptr("other"), MaxRuntimeSeconds: ptr(int64(90000)), Env: map[string]string{"MODEL": "n"}})
	if err != nil {
		t.Fatal(err)
	}
	// Resolve does not judge the SKU or the runtime; the API's submit
	// validation does, exactly as for a hand-written spec.
	if r2.SKU != "other" || r2.Spec.MaxRuntimeSeconds != 90000 || r2.Spec.Env["MODEL"] != "n" {
		t.Fatalf("overrides not applied: %+v", r2)
	}
	if tpl.Env["MODEL"] != "m" {
		t.Fatal("resolving mutated the template's defaults")
	}
}

func TestResolveRefusesWhatTheTemplateDoesNotAllow(t *testing.T) {
	tpl := parsedGood(t)
	cases := []struct {
		name  string
		o     Overrides
		code  string
		field string
	}{
		{"image", Overrides{Inputs: inputs, Image: ptr("x")}, "field_not_overridable", "spec.image"},
		{"command", Overrides{Inputs: inputs, Command: []string{"sh"}}, "field_not_overridable", "spec.command"},
		{"outputs", Overrides{Inputs: inputs, Outputs: []Output{{Mount: "/mnt/out"}}}, "field_not_overridable", "spec.outputs"},
		{"providers", Overrides{Inputs: inputs, Providers: []string{"aws"}}, "field_not_overridable", "providers"},
		{"no inputs", Overrides{}, "missing_input", "spec.inputs"},
		{"empty source", Overrides{Inputs: []Input{{Mount: "/mnt/in"}}}, "missing_input", "spec.inputs"},
		{"undeclared mount", Overrides{Inputs: []Input{{Source: "s3://b", Mount: "/data"}}}, "unknown_input_mount", "spec.inputs"},
		{"bound twice", Overrides{Inputs: append(append([]Input{}, inputs...), inputs...)}, "duplicate_input_mount", "spec.inputs"},
		{"bad env key", Overrides{Inputs: inputs, Env: map[string]string{"A B": "1"}}, "invalid_env", "spec.env"},
	}
	for _, tc := range cases {
		_, err := tpl.Resolve(tc.o)
		var fe *FieldError
		if !errors.As(err, &fe) || fe.Code != tc.code || fe.Field != tc.field {
			t.Errorf("%s: got %v, want %s on %s", tc.name, err, tc.code, tc.field)
		}
	}
}

func TestCommandOverrideWhenAllowed(t *testing.T) {
	tpl := parsedGood(t)
	tpl.Overridable = append(tpl.Overridable, FieldCommand)
	r, err := tpl.Resolve(Overrides{Inputs: inputs, Command: []string{"python", "x.py"}})
	if err != nil || strings.Join(r.Spec.Command, " ") != "python x.py" {
		t.Fatalf("%v %v", r.Spec.Command, err)
	}
}

func TestNilSetIsEmpty(t *testing.T) {
	var s *Set
	if l := s.List(); l == nil || len(l) != 0 {
		t.Fatal("nil set must list as empty, not null")
	}
	if _, err := s.Get("x"); !errors.Is(err, ErrUnknown) {
		t.Fatal(err)
	}
}

func TestValidImageAcceptsRealReferenceShapes(t *testing.T) {
	for _, ref := range []string{
		"vllm/vllm-openai:v0.30.0-cu129" + digest,
		"vllm/vllm-openai" + digest,
		"ghcr.io/huggingface/text-embeddings-inference:1.8" + digest,
		"localhost:5000/team/img:dev" + digest,
		"123456789012.dkr.ecr.us-east-1.amazonaws.com/finetune:r1" + digest,
	} {
		if err := ValidImage(ref); err != nil {
			t.Errorf("%s: %v", ref, err)
		}
	}
}
