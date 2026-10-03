// Package events is the job event log's vocabulary and its webhook delivery
// (docs/spec/events.md). Persistence lives in package store; this package
// decides which events exist, what they carry, and how they are sent.
package events

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/FelixSphere/unify-compute/compute-api/internal/store"
)

type Type string

const (
	JobQueued        Type = "job.queued"
	JobProvisioning  Type = "job.provisioning"
	JobPlaced        Type = "job.placed"
	JobRunning       Type = "job.running"
	JobFailover      Type = "job.failover"
	JobPreempted     Type = "job.preempted"
	JobSucceeded     Type = "job.succeeded"
	JobFailed        Type = "job.failed"
	JobCancelled     Type = "job.cancelled"
	HoldExtended     Type = "billing.hold_extended"
	BalanceExhausted Type = "billing.balance_exhausted"
)

// Types is every event type, in the order the spec's table lists them.
var Types = []Type{
	JobQueued, JobProvisioning, JobPlaced, JobRunning, JobFailover, JobPreempted,
	JobSucceeded, JobFailed, JobCancelled, HoldExtended, BalanceExhausted,
}

// DefaultWebhookTypes are the terminal job events: what a pipeline waiting on
// a job needs, and nothing that would page a receiver on every status step.
var DefaultWebhookTypes = []Type{JobSucceeded, JobFailed, JobCancelled, JobPreempted}

func (t Type) Known() bool {
	for _, k := range Types {
		if k == t {
			return true
		}
	}
	return false
}

type Placement struct {
	Cloud        string `json:"cloud"`
	Region       string `json:"region,omitempty"`
	InstanceType string `json:"instance_type,omitempty"`
}

type QueuedData struct {
	SKU string `json:"sku"`
}

type PlacedData struct {
	Placement *Placement `json:"placement"`
}

type FailoverData struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Reason string `json:"reason,omitempty"`
}

type PreemptedData struct {
	Provider string `json:"provider"`
}

type FailedData struct {
	Error     store.JobError `json:"error"`
	Retryable bool           `json:"retryable"`
}

type CancelledData struct {
	Reason string `json:"reason"`
}

type HoldExtendedData struct {
	HoldUSD   string `json:"hold_usd"`
	ExpiresAt int64  `json:"expires_at,omitempty"`
}

var ErrInvalidEvent = errors.New("events: invalid event")

// Validate checks that t is a known type and data, a JSON object, carries the
// fields the spec makes required for it. An event is never rewritten, so a
// malformed one would be permanent; refusing it here is the only chance.
func Validate(t Type, data json.RawMessage) error {
	if !t.Known() {
		return fmt.Errorf("%w: unknown type %q", ErrInvalidEvent, t)
	}
	if len(data) == 0 {
		data = json.RawMessage(`{}`)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil || obj == nil {
		return fmt.Errorf("%w: %s data must be a JSON object", ErrInvalidEvent, t)
	}
	missing := func(field string) error {
		return fmt.Errorf("%w: %s requires %s", ErrInvalidEvent, t, field)
	}
	switch t {
	case JobQueued:
		var d QueuedData
		if json.Unmarshal(data, &d) != nil || d.SKU == "" {
			return missing("sku")
		}
	case JobPlaced:
		var d PlacedData
		if json.Unmarshal(data, &d) != nil || d.Placement == nil || d.Placement.Cloud == "" {
			return missing("placement.cloud")
		}
	case JobFailover:
		var d FailoverData
		if json.Unmarshal(data, &d) != nil || d.From == "" || d.To == "" {
			return missing("from and to")
		}
	case JobPreempted:
		var d PreemptedData
		if json.Unmarshal(data, &d) != nil || d.Provider == "" {
			return missing("provider")
		}
	case JobFailed:
		var d FailedData
		if json.Unmarshal(data, &d) != nil || d.Error.Code == "" {
			return missing("error.code")
		}
		if _, ok := obj["retryable"]; !ok {
			return missing("retryable")
		}
	case JobCancelled:
		var d CancelledData
		if json.Unmarshal(data, &d) != nil || d.Reason == "" {
			return missing("reason")
		}
	case HoldExtended:
		var d HoldExtendedData
		if json.Unmarshal(data, &d) != nil || d.HoldUSD == "" {
			return missing("hold_usd")
		}
	}
	return nil
}

// Append validates and writes one event; the store queues its webhook
// deliveries in the same statement. data is marshalled as given, so pass
// one of the *Data types above or nil.
func Append(ctx context.Context, st *store.Store, jobID string, t Type, data any) (store.Event, error) {
	raw := json.RawMessage(`{}`)
	if data != nil {
		b, err := json.Marshal(data)
		if err != nil {
			return store.Event{}, err
		}
		raw = b
	}
	if err := Validate(t, raw); err != nil {
		return store.Event{}, err
	}
	return st.AppendEvent(ctx, store.NewEvent{JobID: jobID, Type: string(t), Data: raw})
}

// Object is an event as the API and webhooks present it.
type Object struct {
	ID        string          `json:"id"`
	Object    string          `json:"object"`
	JobID     string          `json:"job_id"`
	Type      string          `json:"type"`
	CreatedAt int64           `json:"created_at"`
	Data      json.RawMessage `json:"data"`
}

func ObjectOf(e store.Event) Object {
	return Object{ID: e.ID, Object: "compute.event", JobID: e.JobID, Type: e.Type, CreatedAt: e.CreatedAt.Unix(), Data: e.Data}
}

// NewSecret returns a webhook signing secret: "whsec_" and 32 random bytes in
// hex. Receivers use the whole string, prefix included, as the HMAC key.
func NewSecret() string {
	var b [32]byte
	_, _ = rand.Read(b[:])
	return "whsec_" + hex.EncodeToString(b[:])
}

// TypeStrings converts for store.NewWebhook.EventTypes.
func TypeStrings(ts []Type) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = string(t)
	}
	return out
}
