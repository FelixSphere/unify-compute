package backend

import (
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/FelixSphere/unify-compute/compute-api/internal/sku"
	"github.com/FelixSphere/unify-compute/compute-api/internal/skypilot"
)

// Spec is the customer's job spec (docs/spec/job-api.md, Job.spec).
type Spec struct {
	Image             string            `json:"image"`
	Command           []string          `json:"command"`
	Env               map[string]string `json:"env,omitempty"`
	Inputs            []Input           `json:"inputs,omitempty"`
	Outputs           []Output          `json:"outputs,omitempty"`
	MaxRuntimeSeconds int               `json:"max_runtime_seconds"`
}

type Input struct {
	Source string `json:"source"`
	Mount  string `json:"mount"`
}

type Output struct {
	Mount string `json:"mount"`
}

// Job is what the backend launches: one attempt of one job. Candidates is the
// placement the registry resolved at submit (upstream.Registry.Place), in
// catalogue order.
type Job struct {
	ID         string
	Attempt    int
	Spec       Spec
	SKU        sku.SKU
	Candidates []sku.Provider
	// Owner is the billing identity that submitted the job; the artifact
	// upload binds its token to it.
	Owner string
	// RuntimeLimitSeconds, when positive and below Spec.MaxRuntimeSeconds,
	// is the cap the attempt is held to instead: a job launched late after a
	// restart gets only the runtime its hold still covers (backend.md,
	// "Restart").
	RuntimeLimitSeconds int
}

// runtimeLimit is the cap the attempt ends at, in seconds from UP.
func (j Job) runtimeLimit() int {
	if j.RuntimeLimitSeconds > 0 && j.RuntimeLimitSeconds < j.Spec.MaxRuntimeSeconds {
		return j.RuntimeLimitSeconds
	}
	return j.Spec.MaxRuntimeSeconds
}

// Task is a SkyPilot task (sky/utils/schemas.py get_task_schema). Marshalled
// to JSON it is valid task YAML.
type Task struct {
	Name       string            `json:"name"`
	Resources  TaskResources     `json:"resources"`
	Envs       map[string]string `json:"envs,omitempty"`
	FileMounts map[string]string `json:"file_mounts,omitempty"`
	Run        string            `json:"run"`
}

// TaskResources is the placement block from internal/skypilot plus the fields
// only a launch needs. With any_of, these sit beside it and SkyPilot applies
// them to every candidate.
type TaskResources struct {
	skypilot.Resources
	ImageID  string   `json:"image_id"`
	Autostop Autostop `json:"autostop"`
}

type Autostop struct {
	IdleMinutes int  `json:"idle_minutes"`
	Down        bool `json:"down"`
}

var (
	ErrNoImage      = errors.New("backend: spec.image is required")
	ErrNoCommand    = errors.New("backend: spec.command is required")
	ErrNoCandidates = errors.New("backend: no placement candidates")
	// ErrOutputsUnsupported: SkyPilot 0.13.0 can only MOUNT a whole bucket
	// (sky/data/storage.py, StorageModeError "MOUNT mode does not support
	// mounting specific files"), so jobs/<job_id>/ of a shared artifact
	// bucket cannot be the output mount, and mounting the whole bucket would
	// expose every other job's artifacts. Outputs are uploaded by the run
	// step instead (ADR-0005), which needs TaskOptions.Outputs; without one
	// they are still refused.
	ErrOutputsUnsupported = errors.New("backend: spec.outputs is not supported yet")
	ErrBadMount           = errors.New("backend: mount must be an absolute path")
	ErrBadEnv             = errors.New("backend: env names must match [A-Za-z_][A-Za-z0-9_]*")
)

// ReservedEnvPrefix is the upload step's own variables; a customer env name
// with it is refused (ErrReservedEnv) so it cannot shadow them.
const ReservedEnvPrefix = "UNIFY_COMPUTE_"

var (
	ErrReservedEnv = errors.New("backend: env names starting with " + ReservedEnvPrefix + " are reserved")
	ErrInputSource = errors.New("backend: inputs must be https:// URLs with a file name")
)

// OutputUploader turns spec.outputs into the attempt's run step: it returns
// the env the upload needs and a run script that runs command and then
// uploads the output mounts, whatever command's exit status
// (internal/artifacts).
type OutputUploader interface {
	OutputStep(j Job, command string) (envs map[string]string, run string, err error)
}

// The pattern SkyPilot's task schema enforces on envs keys.
var envName = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// ClusterName is the SkyPilot cluster an attempt runs on. One cluster per
// attempt is the isolation rule (R15: one job per VM) and makes the cluster
// name the only handle the backend needs to find, observe or tear down the
// attempt after a restart. SkyPilot cluster names must start with a letter
// and be lowercase-safe across clouds; job ids are ULIDs, so lowercasing
// keeps them unique.
func ClusterName(jobID string, attempt int) string {
	return fmt.Sprintf("uc-%s-%d", strings.ToLower(strings.TrimPrefix(jobID, "job_")), attempt)
}

// TaskOptions are deployment settings the translation needs.
type TaskOptions struct {
	// IdleMinutesToDown is SkyPilot's own autodown, the backstop that tears
	// the cluster down if compute-api is not there to do it.
	IdleMinutesToDown int
	// Outputs builds the upload of spec.outputs. Nil refuses outputs.
	Outputs OutputUploader
	// HTTPSInputsOnly refuses every input that is not an https:// URL. A
	// deployment sets it (ADR-0005): any other source is copied by SkyPilot
	// with its own cloud credentials, which are ours, so a customer could
	// name a bucket only we should read. False keeps the recorded task
	// shapes that SkyPilot accepted (testdata/) reproducible.
	HTTPSInputsOnly bool
}

// TaskFor translates one attempt into a SkyPilot task.
func TaskFor(j Job, opt TaskOptions) (Task, error) {
	s := j.Spec
	if s.Image == "" {
		return Task{}, ErrNoImage
	}
	if len(s.Command) == 0 {
		return Task{}, ErrNoCommand
	}
	if len(j.Candidates) == 0 {
		return Task{}, ErrNoCandidates
	}
	for k := range s.Env {
		if !envName.MatchString(k) {
			return Task{}, fmt.Errorf("%w: %q", ErrBadEnv, k)
		}
		if strings.HasPrefix(k, ReservedEnvPrefix) {
			return Task{}, fmt.Errorf("%w: %q", ErrReservedEnv, k)
		}
	}
	mounts := map[string]string{}
	var fetch []string
	for _, in := range s.Inputs {
		if !path.IsAbs(in.Mount) {
			return Task{}, fmt.Errorf("%w: %q", ErrBadMount, in.Mount)
		}
		// SkyPilot reads an https:// file-mount source as Azure Blob
		// (sky/cloud_stores.py:1053, "Azure URL starts with https://"), so a
		// public or presigned URL is fetched by the run step instead, into
		// the mount directory under the URL's last path segment, which for
		// a presigned S3 URL is the object's name.
		if strings.HasPrefix(in.Source, "https://") {
			u, err := url.Parse(in.Source)
			name := ""
			if err == nil {
				name = path.Base(path.Clean("/" + u.Path))
			}
			if name == "/" || name == "." || name == ".." || u.Host == "" {
				return Task{}, fmt.Errorf("%w: %q has no file name", ErrInputSource, in.Source)
			}
			fetch = append(fetch, fmt.Sprintf("mkdir -p -- %s && curl -fsSL --retry 3 -o %s %s",
				shellJoin([]string{in.Mount}), shellJoin([]string{path.Join(in.Mount, name)}), shellJoin([]string{in.Source})))
			continue
		}
		if opt.HTTPSInputsOnly {
			return Task{}, fmt.Errorf("%w: %q", ErrInputSource, in.Source)
		}
		// A bare URI is a COPY: inputs are read before the run starts and a
		// customer's bucket is never mounted read-write. SkyPilot refuses a
		// source with a trailing slash ("Mount paths cannot end with a
		// slash", observed against 0.13.0), which is how job-api.md writes a
		// prefix, so it is trimmed.
		mounts[in.Mount] = strings.TrimRight(in.Source, "/")
	}
	run, envs := shellJoin(s.Command), s.Env
	if len(fetch) > 0 {
		run = strings.Join(append(fetch, run), " && ")
	}
	if len(s.Outputs) > 0 && opt.Outputs == nil {
		return Task{}, ErrOutputsUnsupported
	}
	// With an uploader, every job runs through its step, outputs or not: it
	// also archives the job's log, which must outlive the cluster.
	if opt.Outputs != nil {
		extra, script, err := opt.Outputs.OutputStep(j, run)
		if err != nil {
			return Task{}, err
		}
		envs = make(map[string]string, len(s.Env)+len(extra))
		for k, v := range s.Env {
			envs[k] = v
		}
		for k, v := range extra {
			envs[k] = v
		}
		run = script
	}
	idle := opt.IdleMinutesToDown
	if idle <= 0 {
		idle = 5
	}
	t := Task{
		Name: ClusterName(j.ID, j.Attempt),
		Resources: TaskResources{
			Resources: skypilot.ResourcesFor(j.SKU, j.Candidates),
			ImageID:   "docker:" + s.Image,
			Autostop:  Autostop{IdleMinutes: idle, Down: true},
		},
		Envs: envs,
		Run:  run,
	}
	if len(mounts) > 0 {
		t.FileMounts = mounts
	}
	return t, nil
}

// shellJoin quotes argv for the bash -c that SkyPilot runs `run` under, so
// the customer's command reaches exec unchanged: no globbing, no expansion.
func shellJoin(argv []string) string {
	q := make([]string, len(argv))
	for i, a := range argv {
		if a != "" && strings.IndexFunc(a, func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./=:,@%+", r))
		}) < 0 {
			q[i] = a
			continue
		}
		q[i] = "'" + strings.ReplaceAll(a, "'", `'"'"'`) + "'"
	}
	return strings.Join(q, " ")
}

// placementFrom resolves where an attempt landed from SkyPilot's cluster
// record. SkyPilot reports cloud and region but not the instance type as a
// field, so the instance type comes from the candidate that matches; the
// catalogue forbids two providers of one cloud in a SKU (R5), so the match is
// unique. resources_str_full is the fallback for a cluster that landed on
// something we did not list, which would itself be worth an alert.
func placementFrom(rec ClusterRecord, candidates []sku.Provider) Placement {
	p := Placement{}
	if rec.Cloud != nil {
		p.Cloud = strings.ToLower(*rec.Cloud)
	}
	if rec.Region != nil {
		p.Region = *rec.Region
	}
	for _, c := range candidates {
		if c.Cloud != p.Cloud {
			continue
		}
		if c.IsCluster() {
			p.InstanceType = ""
			if p.Region == "" {
				p.Region = c.Context
			}
			return p
		}
		p.InstanceType = c.InstanceType
		return p
	}
	if rec.ResourcesStrFull != nil {
		p.InstanceType = instanceTypeFromResourcesStr(*rec.ResourcesStrFull)
	}
	return p
}

// instanceTypeFromResourcesStr reads the instance type out of
// resources_utils.format_resource output, e.g.
// "1x(gpus=A10G:1, cpus=4, mem=16, g5.xlarge, disk=256)": it is the one
// element that is not key=value.
func instanceTypeFromResourcesStr(s string) string {
	open, close := strings.IndexByte(s, '('), strings.LastIndexByte(s, ')')
	if open < 0 || close < open {
		return ""
	}
	var bare []string
	for _, el := range strings.Split(s[open+1:close], ",") {
		el = strings.TrimSpace(el)
		if el != "" && el != "..." && !strings.Contains(el, "=") {
			bare = append(bare, el)
		}
	}
	sort.Strings(bare)
	if len(bare) == 1 {
		return bare[0]
	}
	return ""
}
