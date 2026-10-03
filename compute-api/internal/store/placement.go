package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// ErrTerminal is returned for a write that only makes sense while a job is
// still live.
var ErrTerminal = errors.New("store: job is in a terminal state")

// Placement is where a job landed (docs/spec/upstreams.md R10). Metering,
// reconciliation and the invoice line key off it, so it is written once
// SkyPilot reports the cluster UP and before the job reads `running`.
type Placement struct {
	Cloud        string `json:"cloud"`
	Region       string `json:"region"`
	InstanceType string `json:"instance_type"`
}

// SetPlacement records where the job's current attempt runs. It replaces any
// earlier placement, because a retried attempt may land on another upstream,
// and refuses a terminal job: where a finished job ran is history and must not
// move after metering has keyed off it. Like UpdateStatus it is not
// owner-scoped; its caller is the backend.
func (s *Store) SetPlacement(ctx context.Context, id string, p Placement) (Job, error) {
	return setPlacement(ctx, s.pool, id, p)
}

// SetPlacementWithEvent is SetPlacement and its job.placed event in one
// transaction, like UpdateStatusWithEvent.
func (s *Store) SetPlacementWithEvent(ctx context.Context, id string, p Placement, event func(Job) (*NewEvent, error)) (Job, error) {
	var job Job
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		if job, err = setPlacement(ctx, tx, id, p); err != nil {
			return err
		}
		return appendBuilt(ctx, tx, job, event)
	})
	if err != nil {
		return Job{}, err
	}
	return job, nil
}

func setPlacement(ctx context.Context, q rowQuerier, id string, p Placement) (Job, error) {
	raw, err := json.Marshal(p)
	if err != nil {
		return Job{}, err
	}
	job, err := scanJob(q.QueryRow(ctx, `
		UPDATE jobs SET placement = $2, updated_at = now()
		WHERE id = $1 AND status IN ('queued', 'provisioning', 'running')
		RETURNING `+jobColumns, id, raw))
	if err == nil {
		return job, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Job{}, err
	}
	var status string
	err = q.QueryRow(ctx, `SELECT status FROM jobs WHERE id = $1`, id).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	if err != nil {
		return Job{}, err
	}
	return Job{}, fmt.Errorf("%w: %s", ErrTerminal, status)
}
