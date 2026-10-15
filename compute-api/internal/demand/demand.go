// Package demand records what customers asked for and could not have yet
// (ADR-0013): a GPU whose providers this deployment has not enabled. The
// row is the signal to provision; the operator email is the nudge.
package demand

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/FelixSphere/unify-compute/compute-api/internal/mailer"
	"github.com/FelixSphere/unify-compute/compute-api/internal/notify"
)

// Recorder is the store's half.
type Recorder interface {
	RecordGPUDemand(ctx context.Context, requestedBy, skuID string) error
}

// Service records demand and tells the operator, once per account, SKU and
// UTC day (notify keys the email). Mailer, Notifier and OperatorEmail are
// optional together: without them the row is still written.
type Service struct {
	Store         Recorder
	Mailer        *mailer.Mailer
	Notifier      *notify.Notifier
	OperatorEmail string
	Log           *slog.Logger
	// Now times the daily key; nil is time.Now.
	Now func() time.Time
}

const mailTimeout = time.Minute

// Record notes that owner asked for skuID. It never fails the submit: errors
// are logged, and the caller has already answered the customer.
func (s *Service) Record(ctx context.Context, owner, skuID string) {
	log := s.Log
	if log == nil {
		log = slog.Default()
	}
	if s.Store != nil {
		if err := s.Store.RecordGPUDemand(ctx, owner, skuID); err != nil {
			log.Error("gpu demand: record", "sku", skuID, "owner", owner, "err", err)
		}
	}
	if s.Mailer == nil || s.Notifier == nil || s.OperatorEmail == "" {
		return
	}
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	day := now().UTC().Format("2006-01-02")
	key := notify.Key{AccountID: accountID(owner), Subject: "gpu-demand", SubjectID: skuID + "/" + day, Message: "operator-notice"}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), mailTimeout)
		defer cancel()
		_, err := s.Notifier.To(ctx, key, []string{s.OperatorEmail}, func(ctx context.Context, to string) error {
			return s.Mailer.Operator(ctx, to, mailer.OperatorNotice{
				Title:  "GPU demand: " + skuID,
				Body:   "A customer submitted a job for a GPU this deployment cannot launch yet. The submit was refused with gpu_not_provisioned and recorded in gpu_demand.",
				Action: "Provision it: raise the quota or enable the upstream for this SKU (ADR-0006, ADR-0013), then tell the customer.",
				Facts:  [][2]string{{"SKU", skuID}, {"Requested by", owner}, {"Day (UTC)", day}},
			})
		})
		if err != nil {
			log.Error("gpu demand: operator email", "sku", skuID, "err", err)
		}
	}()
}

// accountID reads accounts.OwnerFor's "account:N"; anything else is 0.
func accountID(owner string) int64 {
	n, err := strconv.ParseInt(strings.TrimPrefix(owner, "account:"), 10, 64)
	if err != nil {
		return 0
	}
	return n
}
