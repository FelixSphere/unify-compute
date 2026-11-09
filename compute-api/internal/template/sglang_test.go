package template

import (
	"strings"
	"testing"
)

// batch-infer-sglang is batch-infer-vllm on a different engine: same input and
// output contract and model defaults, so a customer can run one file through
// both and compare. SGLang has no batch-file runner, so the command serves on
// loopback and drives the server itself; these are the parts that must hold.
func TestBatchInferSGLangMatchesBatchInferVLLM(t *testing.T) {
	s, err := Load("../../config/templates.json", shippedCatalogue(t), ceiling)
	if err != nil {
		t.Fatal(err)
	}
	sg, err := s.Get("batch-infer-sglang")
	if err != nil {
		t.Fatal(err)
	}
	base, _ := s.Get("batch-infer-vllm")

	if !sg.Available || !strings.HasPrefix(sg.Image, "lmsysorg/sglang:v0.5.19-cu129-runtime@sha256:") || sg.ImageLicence != "Apache-2.0" || sg.ImageSource == "" {
		t.Fatalf("runs the upstream CUDA 12.9 SGLang image, pinned by digest, with its licence recorded: %+v", sg)
	}
	for _, k := range []string{"MODEL", "MODEL_REVISION", "MAX_MODEL_LEN"} {
		if sg.Env[k] != base.Env[k] {
			t.Errorf("%s drifted from batch-infer-vllm: %q vs %q", k, sg.Env[k], base.Env[k])
		}
	}
	if sg.Env["CONCURRENCY"] == "" {
		t.Error("CONCURRENCY must have a default: the driver sizes its pool from it")
	}
	if sg.SKU != base.SKU || sg.MaxRuntimeSeconds != base.MaxRuntimeSeconds {
		t.Fatalf("defaults should match batch-infer-vllm: %s/%d", sg.SKU, sg.MaxRuntimeSeconds)
	}
	if len(sg.Inputs) != 1 || sg.Inputs[0].Mount != base.Inputs[0].Mount || len(sg.Outputs) != 1 || sg.Outputs[0].Mount != base.Outputs[0].Mount {
		t.Fatalf("mounts: %+v %+v", sg.Inputs, sg.Outputs)
	}
	script := strings.Join(sg.Command, " ")
	for _, step := range []string{
		"python3 -m sglang.launch_server --model-path \"$MODEL\"",
		"${MODEL_REVISION:+--revision \"$MODEL_REVISION\"}",
		"--context-length \"$MAX_MODEL_LEN\"",
		"--host 127.0.0.1",
		"/health_generate",
		"kill -0 \"$SERVER\"",
		"trap 'kill \"$SERVER\"",
		"/mnt/in/requests.jsonl /mnt/out/results.jsonl",
		"pool.map(",
	} {
		if !strings.Contains(script, step) {
			t.Errorf("command lacks %q", step)
		}
	}
	// exec would replace the shell that owns the trap and leave the server running.
	if strings.Contains(script, "exec python3") {
		t.Error("the driver must not exec: the EXIT trap stops the server")
	}
}

// batch-infer-lora-sglang is batch-infer-lora on SGLang: it takes the adapter
// finetune-lora writes, on the same mount, with the same base model, rank
// limit and adapter name, so an adapter can be run through either engine.
func TestBatchInferLoraSGLangPairsWithFinetuneLora(t *testing.T) {
	s, err := Load("../../config/templates.json", shippedCatalogue(t), ceiling)
	if err != nil {
		t.Fatal(err)
	}
	ls, err := s.Get("batch-infer-lora-sglang")
	if err != nil {
		t.Fatal(err)
	}
	vl, _ := s.Get("batch-infer-lora")
	ft, _ := s.Get("finetune-lora")
	sg, _ := s.Get("batch-infer-sglang")

	if !ls.Available || ls.Image != sg.Image || ls.ImageLicence == "" || ls.ImageSource != sg.ImageSource {
		t.Fatalf("runs the same pinned SGLang image as batch-infer-sglang: %+v", ls)
	}
	for _, k := range []string{"MODEL", "MODEL_REVISION", "MAX_MODEL_LEN", "MAX_LORA_RANK", "LORA_NAME"} {
		if ls.Env[k] != vl.Env[k] {
			t.Errorf("%s drifted from batch-infer-lora: %q vs %q", k, ls.Env[k], vl.Env[k])
		}
	}
	if ls.Env["MODEL"] != ft.Env["BASE_MODEL"] || ls.Env["CONCURRENCY"] != sg.Env["CONCURRENCY"] {
		t.Fatalf("base model or concurrency drifted: %v", ls.Env)
	}
	if ls.SKU != vl.SKU || ls.MaxRuntimeSeconds != vl.MaxRuntimeSeconds {
		t.Fatalf("defaults should match batch-infer-lora: %s/%d", ls.SKU, ls.MaxRuntimeSeconds)
	}
	if len(ls.Inputs) != 2 || ls.Inputs[0].Mount != "/mnt/in" || ls.Inputs[1].Mount != "/mnt/adapter" || len(ls.Outputs) != 1 || ls.Outputs[0].Mount != "/mnt/out" {
		t.Fatalf("mounts: %+v %+v", ls.Inputs, ls.Outputs)
	}
	script := strings.Join(ls.Command, " ")
	for _, step := range []string{
		"tar -xzf \"$a\" -C /mnt/adapter",
		"-name adapter_config.json -print -quit",
		"--lora-paths \"$LORA_NAME=$ADAPTER\"",
		"--max-lora-rank \"$MAX_LORA_RANK\"",
		"set -- \"$LORA_NAME\"",
		// SGLang picks the adapter from "base:adapter" in the model field.
		`os.environ["MODEL"] + (":" + LORA if LORA else "")`,
		`parsed["model"] = LORA`,
	} {
		if !strings.Contains(script, step) {
			t.Errorf("command lacks %q", step)
		}
	}
}

// The SGLang templates carry the batch driver inline, because the image is
// upstream's and has no file of ours in it. The copies must not drift: a fix
// to one is a fix to both.
func TestSGLangTemplatesShareOneDriver(t *testing.T) {
	s, err := Load("../../config/templates.json", shippedCatalogue(t), ceiling)
	if err != nil {
		t.Fatal(err)
	}
	driver := func(id string) string {
		tpl, err := s.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		script := strings.Join(tpl.Command, " ")
		i := strings.Index(script, "python3 - /mnt/in/requests.jsonl")
		if i < 0 {
			t.Fatalf("%s: no driver", id)
		}
		return script[i:]
	}
	base := driver("batch-infer-sglang")
	for _, id := range []string{"batch-infer-lora-sglang", "batch-extract-sglang"} {
		if driver(id) != base {
			t.Errorf("%s's driver differs from batch-infer-sglang's", id)
		}
	}
}

// batch-extract-sglang is batch-infer-sglang with one JSON Schema imposed on
// every answer through SGLang's grammar-guided decoding. The schema arrives as
// its own input and is checked before the server starts, so a bad schema costs
// seconds, not a model download.
func TestBatchExtractSGLangConstrainsEveryAnswer(t *testing.T) {
	s, err := Load("../../config/templates.json", shippedCatalogue(t), ceiling)
	if err != nil {
		t.Fatal(err)
	}
	ex, err := s.Get("batch-extract-sglang")
	if err != nil {
		t.Fatal(err)
	}
	sg, _ := s.Get("batch-infer-sglang")

	if !ex.Available || ex.Image != sg.Image || ex.SKU != sg.SKU || ex.MaxRuntimeSeconds != sg.MaxRuntimeSeconds {
		t.Fatalf("same image and defaults as batch-infer-sglang: %+v", ex)
	}
	if len(ex.Env) != len(sg.Env) {
		t.Fatalf("env: %v vs %v", ex.Env, sg.Env)
	}
	for k, v := range sg.Env {
		if ex.Env[k] != v {
			t.Errorf("%s drifted from batch-infer-sglang: %q vs %q", k, ex.Env[k], v)
		}
	}
	if len(ex.Inputs) != 2 || ex.Inputs[0].Mount != "/mnt/in" || ex.Inputs[1].Mount != "/mnt/schema" || len(ex.Outputs) != 1 || ex.Outputs[0].Mount != "/mnt/out" {
		t.Fatalf("mounts: %+v %+v", ex.Inputs, ex.Outputs)
	}
	script := strings.Join(ex.Command, " ")
	check := strings.Index(script, "json.load(open(sys.argv[1]))' /mnt/schema/schema.json")
	launch := strings.Index(script, "sglang.launch_server")
	if check < 0 || launch < 0 || check > launch {
		t.Fatal("the schema must be checked before the server starts")
	}
	for _, step := range []string{
		`set -- "" /mnt/schema/schema.json`,
		`"type": "json_schema", "json_schema": {"name": "output", "schema": SCHEMA, "strict": True}`,
		`body["json_schema"] = json.dumps(SCHEMA)`,
		"answers parse as JSON",
	} {
		if !strings.Contains(script, step) {
			t.Errorf("command lacks %q", step)
		}
	}
	if strings.Contains(script, "--lora-paths") {
		t.Error("no adapter: extraction runs the base model")
	}
}
