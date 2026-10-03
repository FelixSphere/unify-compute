package backend

import (
	"errors"
	"os/exec"
	"strings"
	"testing"
)

type fakeUploader struct{ gotCommand string }

func (f *fakeUploader) OutputStep(j Job, command string) (map[string]string, string, error) {
	f.gotCommand = command
	return map[string]string{ReservedEnvPrefix + "ARTIFACTS_TOKEN": "tok"}, "upload-wrapper\n" + command, nil
}

func TestTaskWithOutputsUsesTheUploadStep(t *testing.T) {
	j := exampleJob()
	j.Spec.Outputs = []Output{{Mount: "/mnt/out"}}
	up := &fakeUploader{}
	task, err := TaskFor(j, TaskOptions{Outputs: up})
	if err != nil {
		t.Fatal(err)
	}
	want := shellJoin(j.Spec.Command)
	if up.gotCommand != want || task.Run != "upload-wrapper\n"+want {
		t.Fatalf("run %q, uploader saw %q", task.Run, up.gotCommand)
	}
	if task.Envs["BATCH_SIZE"] != "256" || task.Envs[ReservedEnvPrefix+"ARTIFACTS_TOKEN"] != "tok" {
		t.Fatalf("envs %v", task.Envs)
	}
	if len(j.Spec.Env) != 1 {
		t.Fatalf("the customer's env map was modified: %v", j.Spec.Env)
	}
	if _, ok := task.FileMounts["/mnt/out"]; ok {
		t.Fatal("an output must never be a file mount")
	}
}

func TestTaskRefusesReservedEnv(t *testing.T) {
	j := exampleJob()
	j.Spec.Env = map[string]string{ReservedEnvPrefix + "ARTIFACTS_URL": "https://evil.example"}
	if _, err := TaskFor(j, TaskOptions{Outputs: &fakeUploader{}}); !errors.Is(err, ErrReservedEnv) || !strings.Contains(err.Error(), "ARTIFACTS_URL") {
		t.Fatalf("err = %v", err)
	}
}

// An https input (public, or the customer's presigned URL) is fetched by the
// run step into its mount directory before the command, because SkyPilot
// would treat it as Azure Blob; a failed fetch stops the command.
func TestTaskFetchesHTTPSInputsInTheRunStep(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	j := exampleJob()
	src := "https://acme-data.s3.amazonaws.com/prompts.jsonl?X-Amz-Signature=abc&X-Amz-Expires=3600"
	dir := t.TempDir()
	j.Spec.Inputs = []Input{{Source: src, Mount: dir + "/in"}}
	j.Spec.Command = []string{"cat", dir + "/in/prompts.jsonl"}
	task, err := TaskFor(j, TaskOptions{HTTPSInputsOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(task.FileMounts) != 0 {
		t.Fatalf("file mounts %v", task.FileMounts)
	}
	// curl stands in for the real one: it writes its URL argument to the -o
	// path, so the test sees exactly the arguments it was given.
	stub := `curl() { local o; while [ $# -gt 1 ]; do [ "$1" = -o ] && o=$2; shift; done; printf '%s' "$1" > "$o"; }; `
	out, err := exec.Command("bash", "-c", stub+task.Run).CombinedOutput()
	if err != nil || string(out) != src {
		t.Fatalf("%v %q\nrun: %s", err, out, task.Run)
	}
	failing := `curl() { return 22; }; `
	if out, err := exec.Command("bash", "-c", failing+task.Run).CombinedOutput(); err == nil || len(out) != 0 {
		t.Fatalf("the command ran after a failed fetch: %v %q", err, out)
	}
}

func TestTaskRefusesInputsThatAreNotHTTPSWhenTold(t *testing.T) {
	for _, src := range []string{"s3://acme-data/docs/", "gs://b/x", "http://example.com/x", "https://example.com/", "https://example.com/a/.."} {
		j := exampleJob()
		j.Spec.Inputs = []Input{{Source: src, Mount: "/mnt/in"}}
		if _, err := TaskFor(j, TaskOptions{HTTPSInputsOnly: true}); !errors.Is(err, ErrInputSource) {
			t.Errorf("%s: %v", src, err)
		}
	}
}
