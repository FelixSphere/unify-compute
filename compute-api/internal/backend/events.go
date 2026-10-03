package backend

import (
	"context"
	"fmt"
	"time"

	"github.com/FelixSphere/unify-compute/compute-api/internal/store"
)

// Placement is where an attempt landed (R10). It is the store's type so the
// value metering keys off and the value on the Job object cannot drift.
type Placement = store.Placement

// EventKind names a lifecycle edge of one attempt. The set and the rules for
// At are in docs/spec/backend.md, "Lifecycle events"; metering (C5) starts
// and stops the GPU meter on them.
type EventKind string

const (
	// EventProvisioning: the launch was handed to SkyPilot. Not billed (I1).
	EventProvisioning EventKind = "provisioning"
	// EventClusterUp: compute-api observed the cluster UP. The GPU meter
	// starts here (metering.md, "Start"). Carries Placement.
	EventClusterUp EventKind = "cluster_up"
	// EventJobStarted: the customer's process started on the cluster, as the
	// node recorded it. Informational; billing started at cluster_up.
	EventJobStarted EventKind = "job_started"
	// EventEnded: the attempt is over for billing. Emitted exactly once per
	// attempt, whether or not it was ever UP. The GPU meter stops at At.
	EventEnded EventKind = "ended"
	// EventClusterDown: SkyPilot confirmed the cluster torn down. After
	// EventEnded; the gap is our cost, never the customer's.
	EventClusterDown EventKind = "cluster_down"
)

// EndReason is why an attempt ended: metering.md attempts[].end_reason, plus
// provision_failed for an attempt that never reached UP.
type EndReason string

const (
	EndExited          EndReason = "exited"
	EndCancelled       EndReason = "cancelled"
	EndMaxRuntime      EndReason = "max_runtime"
	EndPreempted       EndReason = "preempted"
	EndProviderFault   EndReason = "provider_fault"
	EndProvisionFailed EndReason = "provision_failed"
)

// Event is one lifecycle edge. ID is deterministic per (job, attempt, kind),
// so a consumer that is handed the same event twice — the backend re-emits
// after a failed Emit — can drop the replay on ID alone.
type Event struct {
	ID      string
	JobID   string
	Attempt int
	Kind    EventKind
	// At is the instant the edge happened, by the rule for its kind; not
	// when compute-api processed it.
	At time.Time
	// ObservedAt is when compute-api learned of it. Equal to At for
	// cluster_up and provisioning; later for ended (process exit, preemption)
	// and cluster_down.
	ObservedAt time.Time
	// Placement is set on cluster_up and on every later event of the attempt.
	Placement *Placement
	// UpAt is the attempt's cluster_up At, repeated on ended so a consumer
	// can bill the attempt from the one event. Nil if never UP: zero GPU
	// seconds (B8, B10).
	UpAt *time.Time
	// Ended only.
	Reason EndReason
	// Ended only: the terminal job status this attempt produced.
	Status store.Status
	// Ended only, nil on success and customer cancel.
	Failure *Failure
}

func eventID(jobID string, attempt int, kind EventKind) string {
	return fmt.Sprintf("%s/%d/%s", jobID, attempt, kind)
}

// Sink receives lifecycle events. Emit must be durable before it returns nil
// (C5 writes its ledger row); on error the backend does not advance the
// attempt past the edge and emits it again on the next step.
type Sink interface {
	Emit(ctx context.Context, e Event) error
}

type SinkFunc func(ctx context.Context, e Event) error

func (f SinkFunc) Emit(ctx context.Context, e Event) error { return f(ctx, e) }

// UpObserver is an optional extension of Sink. A sink that implements it is
// told each time a running attempt is observed with its cluster UP and its job
// not ended, so metering can advance to that moment (metering.md I3: never
// past the last observation). It is informational: an error is logged and
// does not hold the attempt back.
type UpObserver interface {
	ObservedUp(ctx context.Context, jobID string, attempt int, at time.Time) error
}
