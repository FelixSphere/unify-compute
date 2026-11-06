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
