// Package ledger is compute-api's usage ledger and the settlement engine for
// the GPU-seconds meter (docs/spec/metering.md, task C5).
//
// Usage is written to the append-only usage_events table first and settled
// with the console afterwards, by event id, so a lost response or a console
// outage never loses or double-charges usage. The engine also keeps the
// job's hold ahead of usage (extend) and decides when a job must stop because
// the hold can no longer cover it (ADR-0008).
//
// Only the GPU meter is implemented. Storage and egress have their meter
// names and ledger columns, but sampling them needs the artifacts bucket
// (task I1) and S3 access logs, and charging them needs the account-level
// hold proposed in metering.md; none of that exists yet.
//
// Nothing here is wired to the HTTP handlers or the SkyPilot backend yet.
// The backend (C4) emits its own event type; an adapter that turns it into
// calls on Lifecycle is a later wiring task, as is the loop that calls Tick.
package ledger

import (
	"context"
	"time"
)

type Meter string

const (
	// MeterGPU counts whole seconds a job's cluster was UP and the job was
	// the customer's.
	MeterGPU Meter = "gpu_seconds"
	// MeterStorage counts bytes × seconds stored under the job's artifact
	// prefix. Not implemented.
	MeterStorage Meter = "storage_byte_seconds"
	// MeterEgress counts bytes S3 sent for the job's artifacts. Not
	// implemented.
	MeterEgress Meter = "egress_bytes"
)

// EndReason is why an attempt stopped metering; the values are
// usage.attempts[].end_reason in metering.md.
type EndReason string

const (
	EndExited             EndReason = "exited"
	EndCancelled          EndReason = "cancelled"
	EndMaxRuntime         EndReason = "max_runtime"
	EndPreempted          EndReason = "preempted"
	EndProviderFault      EndReason = "provider_fault"
	EndBalanceExhausted   EndReason = "balance_exhausted"
	EndBillingUnavailable EndReason = "billing_unavailable"
)

// The numbers metering.md fixes. Changing one is a spec change first.
const (
	SettleInterval     = 300 * time.Second  // R1
	Runway             = 3600 * time.Second // H3
	CancelMargin       = 600 * time.Second  // H4, H5
	InitialHoldSeconds = 7200               // H1
	HoldGrace          = 48 * time.Hour     // H6
)

// Lifecycle is what the ledger needs to hear about a job. The backend
// adapter calls it; every method is idempotent, because a poller re-reports
// what it sees.
//
// Times are what happened, not when compute-api noticed: the engine never
// bills past the last time the cluster was observed UP (I3).
type Lifecycle interface {
	// Started: SkyPilot reported the attempt's cluster UP at `at`. Metering
	// starts here (provisioning before it is never billed, I1).
	Started(ctx context.Context, jobID string, attempt int, at time.Time) error
	// Up: the cluster was still UP, and the job still the customer's, at
	// `at`. Metering advances only as far as the latest observation.
	Up(ctx context.Context, jobID string, attempt int, at time.Time) error
	// Stopped: the attempt ended at `at` (process exit, customer cancel
	// accepted, cluster down).
	Stopped(ctx context.Context, jobID string, attempt int, at time.Time, reason EndReason) error
	// Interrupted: preemption or provider fault. reported is the provider's
	// time, or zero when it gave none; then the last observed UP is used.
	Interrupted(ctx context.Context, jobID string, attempt int, reason EndReason, reported time.Time) error
	// Finished: the job reached a terminal state. Usage is settled finally
	// and the hold's remainder released.
	Finished(ctx context.Context, jobID string) error
}

// Actions is what the ledger asks of the rest of compute-api.
type Actions interface {
	// Stop asks the backend to end the job. reason is max_runtime,
	// balance_exhausted or billing_unavailable; metering has already stopped
	// at the moment the ledger decided.
	Stop(ctx context.Context, jobID string, reason EndReason) error
	// Notify records a job event (task C6): "extend_refused", "hold_expired".
	Notify(ctx context.Context, jobID string, event string) error
}

var _ Lifecycle = (*Engine)(nil)
