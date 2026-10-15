package store

import "context"

// RecordGPUDemand keeps one row per submit refused for a SKU this deployment
// cannot launch yet (ADR-0013). requestedBy is the caller's owner string.
func (s *Store) RecordGPUDemand(ctx context.Context, requestedBy, skuID string) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO gpu_demand (requested_by, sku) VALUES ($1, $2)`, requestedBy, skuID)
	return err
}
