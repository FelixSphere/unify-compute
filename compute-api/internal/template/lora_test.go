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

	// Both run the same published image, so an adapter trained by one is
	// loaded by a vLLM built from the same torch.
	if !li.Available || !ft.Available || li.Image != ft.Image || !strings.Contains(li.Image, "unify-compute-finetune:") || !strings.Contains(li.Image, "@sha256:") {
		t.Fatalf("both templates run the same digest-pinned finetune image: %q vs %q", li.Image, ft.Image)
	}
	if li.ImageLicence == "" || li.ImageSource == "" || ft.ImageLicence == "" || ft.ImageSource == "" {
		t.Fatal("available templates record their image licence and source")
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
