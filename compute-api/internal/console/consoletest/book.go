package consoletest

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"

	"github.com/FelixSphere/unify-compute/compute-api/internal/console"
)

// Book is the balance and hold state behind the hold routes. The fake keeps
// its own in memory; UseBook puts another implementation of the bridge's hold
// contract behind the same HTTP front, signature check, faults and call log.
// That is how the tests written against this fake (the ledger's harness, the
// restart tests) also run against compute's local biller (ADR-0011, task A6)
// and check that it behaves as the console does.
//
// A Book answers a refusal with a *console.Error whose Status and Message are
// what the console sends; any other error is a 500 internal_error.
type Book interface {
	Hold(ctx context.Context, req console.HoldRequest) (*console.HoldResult, error)
	Settle(ctx context.Context, holdID string, req console.SettleRequest) (*console.SettleResult, error)
	Extend(ctx context.Context, holdID string, req console.ExtendRequest) (*console.HoldResult, error)
	Release(ctx context.Context, holdID string) (*console.ReleaseResult, error)
	Sweep(ctx context.Context) error

	// SetBalance makes userID's spendable balance quota, as SeedUser does.
	SetBalance(userID int, quota int64)
	Balance(userID int) int64
	Inspect(holdID string) (Hold, bool)
	ConsumeLogs() []ConsumeLog
}

// UseBook routes holds, settles, extends and releases to b from now on, and
// the inspection methods (Balance, Hold, ConsumeLogs, Sweep) read b. Verify
// keeps using the fake's own users and keys. Tenants and per-key spending
// limits are console concepts a Book does not have: seeding a tenant user
// panics, and a key's RemainQuota is not enforced. Call it before seeding.
func (s *Server) UseBook(b Book) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.book = b
}

var bookFactory atomic.Pointer[func(*Server) Book]

// SetBookFactory makes every Server that New creates from now on use the
// Book f returns for it (UseBook), until it is called with nil. It lets a
// parity run send existing tests, unchanged, through another Book: a
// sequential top-level test sets it, runs those tests as its subtests, and
// clears it in its cleanup. Nothing else runs meanwhile, because Go starts
// top-level parallel tests only after every sequential one has finished.
// f gets the new Server so its Book can share the fake's clock.
func SetBookFactory(f func(*Server) Book) {
	if f == nil {
		bookFactory.Store(nil)
		return
	}
	bookFactory.Store(&f)
}

func (s *Server) currentBook() Book {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.book
}

func bookResponse(out any, err error) response {
	var ce *console.Error
	switch {
	case err == nil:
		return ok(out)
	case errors.As(err, &ce):
		return fail(ce.Status, ce.Message)
	}
	return fail(http.StatusInternalServerError, CodeInternalError)
}

func bookHold(b Book, r *http.Request, body []byte) response {
	var in console.HoldRequest
	if !decode(body, &in) {
		return fail(http.StatusBadRequest, CodeInvalidRequest)
	}
	return bookResponse(b.Hold(r.Context(), in))
}

func bookSettle(b Book, r *http.Request, body []byte) response {
	var in console.SettleRequest
	if !decode(body, &in) {
		return fail(http.StatusBadRequest, CodeInvalidRequest)
	}
	return bookResponse(b.Settle(r.Context(), r.PathValue("hold_id"), in))
}

func bookExtend(b Book, r *http.Request, body []byte) response {
	var in console.ExtendRequest
	if !decode(body, &in) {
		return fail(http.StatusBadRequest, CodeInvalidRequest)
	}
	return bookResponse(b.Extend(r.Context(), r.PathValue("hold_id"), in))
}

func bookRelease(b Book, r *http.Request) response {
	return bookResponse(b.Release(r.Context(), r.PathValue("hold_id")))
}
