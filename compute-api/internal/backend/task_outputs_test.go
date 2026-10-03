package backend

import (
	"errors"
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
