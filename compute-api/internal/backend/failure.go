package backend

import (
	"errors"
	"strings"

	"github.com/FelixSphere/unify-compute/compute-api/internal/store"
)

// Failure is why an attempt did not succeed. Code and Message go on the Job
// as error; Retryable says whether running the job again could plausibly
// succeed. Retry itself is not built (job-api.md: *planned: auto-retry*); the
// flag is what that task will read.
type Failure struct {
	Code      string
	Message   string
	Retryable bool
}

func (f *Failure) jobError() *store.JobError {
	if f == nil {
		return nil
	}
	return &store.JobError{Code: f.Code, Message: f.Message}
}

// Failure codes. They are API surface: they reach the customer as
// error.code on the Job.
const (
	CodeCapacityExhausted  = "capacity_exhausted"
	CodeImagePullFailed    = "image_pull_failed"
	CodeProvisionFailed    = "provision_failed"
	CodePreempted          = "preempted"
	CodeProviderFault      = "provider_fault"
	CodeNonZeroExit        = "nonzero_exit"
	CodeMaxRuntimeExceeded = "max_runtime_exceeded"
)

// Substrings that identify an image pull failure in a launch error. SkyPilot
// has no exception type for it: on a VM the pull is a `docker pull` run by
// provision/docker_utils.py whose non-zero exit becomes a CommandError naming
// the command; on Kubernetes it is the pod's waiting reason
// (provision/kubernetes/utils.py KUBERNETES_FAILURE_HINTS).
var imagePullMarkers = []string{
	"docker pull", "ImagePullBackOff", "ErrImagePull",
	"pull access denied", "manifest unknown", "repository does not exist",
}

// classifyLaunchError maps a failed launch request to a Failure. Image pull
// is checked first because a pull failure on every candidate also surfaces as
// "no resources" once SkyPilot has failed over through all of them.
func classifyLaunchError(err error, interruptible bool) *Failure {
	var re *RequestError
	if !errors.As(err, &re) {
		return &Failure{Code: CodeProvisionFailed, Message: err.Error(), Retryable: true}
	}
	for _, m := range imagePullMarkers {
		if strings.Contains(re.Message, m) {
			// The customer's image or tag is wrong, or private without
			// credentials. Retrying the same spec fails the same way.
			return &Failure{Code: CodeImagePullFailed, Message: "the image could not be pulled: " + firstLine(re.Message), Retryable: false}
		}
	}
	if interruptible && strings.Contains(strings.ToLower(re.Message), "preempt") {
		return &Failure{Code: CodePreempted, Message: "the instance was preempted before the job started", Retryable: true}
	}
	switch re.Type {
	case "ResourcesUnavailableError":
		// Raised when every candidate in the any_of has been tried
		// (sky/exceptions.py). Capacity comes back; a later retry can land.
		return &Failure{Code: CodeCapacityExhausted, Message: "no candidate provider had capacity", Retryable: true}
	case "InvalidClusterNameError", "ValueError", "InvalidSkyPilotConfigError":
		// The task we built was refused. Our bug, not capacity; retrying
		// the same task cannot help.
		return &Failure{Code: CodeProvisionFailed, Message: firstLine(re.Message), Retryable: false}
	}
	return &Failure{Code: CodeProvisionFailed, Message: firstLine(re.Message), Retryable: true}
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 500 {
		s = s[:500]
	}
	return s
}

// jobOutcome maps a terminal SkyPilot job status to ours.
func jobOutcome(s JobStatus) (EndReason, store.Status, *Failure) {
	switch s {
	case JobSucceeded:
		return EndExited, store.StatusSucceeded, nil
	case JobFailed:
		return EndExited, store.StatusFailed, &Failure{Code: CodeNonZeroExit, Message: "the job's command exited non-zero"}
	case JobFailedSetup:
		return EndExited, store.StatusFailed, &Failure{Code: CodeNonZeroExit, Message: "the job failed during setup"}
	case JobFailedDriver:
		// The skylet lost the job's driver process: the node, not the
		// customer's code. Retryable like a preemption.
		return EndProviderFault, store.StatusInterrupted, &Failure{Code: CodeProviderFault, Message: "the node lost the job", Retryable: true}
	case JobCancelled:
		// Only compute-api cancels jobs on our clusters, and it ends the
		// attempt as cancelled before it does.
		return EndCancelled, store.StatusCancelled, nil
	}
	return "", "", nil
}
