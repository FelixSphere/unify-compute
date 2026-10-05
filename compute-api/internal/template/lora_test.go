package template

import (
	"strings"
	"testing"
)

// batch-infer-lora is batch-infer-vllm with an adapter: same base model and
// revision defaults, the adapter arriving as one archive on its own mount,
// and infer_lora.py from the finetune image doing what `vllm run-batch`
// cannot (it takes engine flags but no adapter list).
func TestBatchInferLoraPairsWithFinetuneLora(t *testing.T) {
	s, err := Load("../../config/templates.json", shippedCatalogue(t), ceiling)
	if err != nil {
		t.Fatal(err)
	}
	li, err := s.Get("batch-infer-lora")
	if err != nil {
		t.Fatal(err)
	}
	ft, _ := s.Get("finetune-lora")
	base, _ := s.Get("batch-infer-vllm")

	// Both wait on the same image; the reason names the publish step.
	if li.Available || li.Image != "" || ft.Available || ft.Image != "" {
		t.Fatalf("the finetune image is not published yet; both templates must stay unavailable: %v %v", li.Available, ft.Available)
	}
	if li.UnavailableReason != ft.UnavailableReason || !strings.Contains(li.UnavailableReason, "unify-compute-finetune") {
		t.Fatalf("unavailable reasons should be the same image story: %q", li.UnavailableReason)
	}
	// Same base model and revision as the plain batch template, so an
	// adapter trained with finetune-lora's default base is served on it.
	if li.Env["MODEL"] != base.Env["MODEL"] || li.Env["MODEL_REVISION"] != base.Env["MODEL_REVISION"] || li.Env["MODEL"] != ft.Env["BASE_MODEL"] {
		t.Fatalf("base model drifted: infer %v, batch %v, finetune %v", li.Env, base.Env, ft.Env)
	}
	if li.SKU != base.SKU || li.MaxRuntimeSeconds != base.MaxRuntimeSeconds {
		t.Fatalf("defaults should match batch-infer-vllm: %s/%d", li.SKU, li.MaxRuntimeSeconds)
	}
	// The hand-off: finetune-lora writes adapter.tar.gz, this takes it on /mnt/adapter.
	if !strings.Contains(strings.Join(ft.Command, " "), "tar -czf /mnt/out/adapter.tar.gz") || !strings.Contains(ft.Outputs[0].Description, "adapter.tar.gz") {
		t.Fatal("finetune-lora must write adapter.tar.gz for batch-infer-lora")
	}
	if len(li.Inputs) != 2 || li.Inputs[0].Mount != "/mnt/in" || li.Inputs[1].Mount != "/mnt/adapter" || len(li.Outputs) != 1 || li.Outputs[0].Mount != "/mnt/out" {
		t.Fatalf("mounts: %+v %+v", li.Inputs, li.Outputs)
	}
	script := strings.Join(li.Command, " ")
	for _, step := range []string{
		"tar -xzf",
		"adapter_config.json",
		"infer_lora.py --model \"$MODEL\"",
		"--lora \"$ADAPTER\" --lora-name \"$LORA_NAME\"",
		"-i /mnt/in/requests.jsonl -o /mnt/out/results.jsonl",
		"--max-lora-rank \"$MAX_LORA_RANK\"",
	} {
		if !strings.Contains(script, step) {
			t.Errorf("command lacks %q", step)
		}
	}
	// finetune-lora now takes one archive and unpacks it; the reference
	// run's paths inside /mnt/in are unchanged.
	fts := strings.Join(ft.Command, " ")
	for _, step := range []string{"tar -xzf \"$a\" -C /mnt/in", "/mnt/in/train.jsonl", "/mnt/in/schema.json"} {
		if !strings.Contains(fts, step) {
			t.Errorf("finetune-lora command lacks %q", step)
		}
	}
}
