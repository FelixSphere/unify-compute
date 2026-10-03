package backend

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/FelixSphere/unify-compute/compute-api/internal/sku"
)

func exampleJob() Job {
	return Job{
		ID: "job_01J8Q6ZK3R7Y9W2X4V6T8S0A1B", Attempt: 1,
		Spec: Spec{
			Image:   "ghcr.io/acme/embed:1.4",
			Command: []string{"python", "run.py", "--input", "/mnt/in", "--msg", "it's $HOME"},
			Env:     map[string]string{"BATCH_SIZE": "256"},
			Inputs:  []Input{{Source: "s3://acme-data/docs/", Mount: "/mnt/in"}},
			// Outputs omitted: see TestTaskRefusesOutputs.
			MaxRuntimeSeconds: 7200,
		},
		SKU:        sku.SKU{Accelerator: "A10G", GPUCount: 1},
		Candidates: []sku.Provider{{Cloud: "aws", InstanceType: "g5.xlarge", Regions: []string{"us-east-1", "us-west-2"}}},
	}
}

// The goldens are byte-for-byte the tasks that a real SkyPilot 0.13.0 API
// server accepted: /validate answered 200 and /launch got past task parsing
// and file-mount validation to NoCloudAccessError (no credentials on that
// server, so nothing could launch). A diff here means the task changed shape
// and needs re-checking against a real server.
func TestTaskMatchesWhatSkyPilotAccepted(t *testing.T) {
	cases := map[string]func(*Job){
		"task_anyof.json": func(*Job) {},
		"task_flat.json": func(j *Job) {
			j.Candidates = []sku.Provider{{Cloud: "aws", InstanceType: "g5.xlarge", Regions: []string{"us-east-1"}}}
			j.SKU.Interruptible = true
		},
	}
	for golden, tweak := range cases {
		t.Run(golden, func(t *testing.T) {
			j := exampleJob()
			tweak(&j)
			task, err := TaskFor(j, TaskOptions{})
			if err != nil {
				t.Fatal(err)
			}
			got, _ := marshalNoEscape(task)
			want, err := os.ReadFile("testdata/skypilot-0.13.0/" + golden)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != strings.TrimSpace(string(want)) {
				t.Fatalf("\n got %s\nwant %s", got, want)
			}
		})
	}
}

func TestTaskSharedFieldsSitBesideAnyOf(t *testing.T) {
	task, err := TaskFor(exampleJob(), TaskOptions{IdleMinutesToDown: 10})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]map[string]any
	b, _ := json.Marshal(task)
	_ = json.Unmarshal(b, &m)
	r := m["resources"]
	if r["image_id"] != "docker:ghcr.io/acme/embed:1.4" {
		t.Fatalf("image_id %v", r["image_id"])
	}
	if !reflect.DeepEqual(r["autostop"], map[string]any{"idle_minutes": 10.0, "down": true}) {
		t.Fatalf("autostop %v", r["autostop"])
	}
	if anyOf, _ := r["any_of"].([]any); len(anyOf) != 2 {
		t.Fatalf("any_of %v", r["any_of"])
	}
}

func TestTaskTrimsTrailingSlashOnInputs(t *testing.T) {
	task, err := TaskFor(exampleJob(), TaskOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := task.FileMounts["/mnt/in"]; got != "s3://acme-data/docs" {
		t.Fatalf("input source %q", got)
	}
}

func TestTaskRefusals(t *testing.T) {
	cases := []struct {
		name string
		mod  func(*Job)
		want error
	}{
		{"no image", func(j *Job) { j.Spec.Image = "" }, ErrNoImage},
		{"no command", func(j *Job) { j.Spec.Command = nil }, ErrNoCommand},
		{"no candidates", func(j *Job) { j.Candidates = nil }, ErrNoCandidates},
		{"bad env", func(j *Job) { j.Spec.Env = map[string]string{"1BAD": "x"} }, ErrBadEnv},
		{"relative mount", func(j *Job) { j.Spec.Inputs = []Input{{Source: "s3://b/p", Mount: "in"}} }, ErrBadMount},
		{"outputs", func(j *Job) { j.Spec.Outputs = []Output{{Mount: "/mnt/out"}} }, ErrOutputsUnsupported},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			j := exampleJob()
			c.mod(&j)
			if _, err := TaskFor(j, TaskOptions{}); !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
		})
	}
}

// The run string must reach the process as the customer's argv, whatever it
// contains, under the bash -c SkyPilot uses.
func TestShellJoinRoundTripsThroughBash(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	argv := []string{"python", "-c", "print('hi')", "it's", "$HOME", "a b", "*", "", "back\\slash", "`id`", "semi;colon", "new\nline"}
	// The joined string is the command line; a function stands in for the
	// customer's program and prints the argv it received.
	out, err := exec.Command("bash", "-c", `python() { for a in "$@"; do printf '%s\0' "$a"; done; }; `+shellJoin(argv)).Output()
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
	if !reflect.DeepEqual(got, argv[1:]) {
		t.Fatalf("\n got %q\nwant %q", got, argv)
	}
}

func TestClusterNameIsLowercaseAndPerAttempt(t *testing.T) {
	if got := ClusterName("job_01J8Q6ZK3R7Y9W2X4V6T8S0A1B", 2); got != "uc-01j8q6zk3r7y9w2x4v6t8s0a1b-2" {
		t.Fatal(got)
	}
}

func strp(s string) *string { return &s }

func TestPlacementFromClusterRecord(t *testing.T) {
	cands := []sku.Provider{
		{Cloud: "aws", InstanceType: "g6.xlarge", Regions: []string{"us-east-1", "us-west-2"}},
		{Cloud: "kubernetes", Context: "coreweave-ord1"},
	}
	cases := []struct {
		name string
		rec  ClusterRecord
		want Placement
	}{
		{"vm candidate", ClusterRecord{Cloud: strp("AWS"), Region: strp("us-west-2")},
			Placement{Cloud: "aws", Region: "us-west-2", InstanceType: "g6.xlarge"}},
		{"kubernetes context", ClusterRecord{Cloud: strp("Kubernetes"), Region: strp("coreweave-ord1")},
			Placement{Cloud: "kubernetes", Region: "coreweave-ord1"}},
		{"unlisted falls back to resources string", ClusterRecord{Cloud: strp("GCP"), Region: strp("us-central1"),
			ResourcesStrFull: strp("1x(gpus=L4:1, cpus=4, mem=16, g2-standard-4, disk=256)")},
			Placement{Cloud: "gcp", Region: "us-central1", InstanceType: "g2-standard-4"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := placementFrom(c.rec, cands); got != c.want {
				t.Fatalf("got %+v want %+v", got, c.want)
			}
		})
	}
}
