package consoletest

import (
	"net/http"
)

type identityData struct {
	UserID           int    `json:"user_id"`
	TenantID         int    `json:"tenant_id"`
	TokenID          int    `json:"token_id"`
	Group            string `json:"group"`
	Status           string `json:"status"`
	RemainQuota      int64  `json:"remain_quota"`
	TokenUnlimited   bool   `json:"token_unlimited"`
	TokenRemainQuota int64  `json:"token_remain_quota"`
}

type holdData struct {
	HoldID      string `json:"hold_id"`
	Quota       int64  `json:"quota"`
	RemainQuota int64  `json:"remain_quota"`
}

type settleData struct {
	HoldID        string `json:"hold_id"`
	SettledQuota  int64  `json:"settled_quota"`
	HoldRemaining int64  `json:"hold_remaining"`
}

type releaseData struct {
	ReleasedQuota int64 `json:"released_quota"`
	RemainQuota   int64 `json:"remain_quota"`
}

// entityBalance resolves the billing entity for userID. Callers hold s.mu.
func (s *Server) entityBalance(userID int) (*int64, *Tenant) {
	u := s.users[userID]
	if u == nil {
		return nil, nil
	}
	if u.TenantID > 0 {
		t := s.tenants[u.TenantID]
		if t == nil {
			return nil, nil
		}
		return &t.Quota, t
	}
	return &u.Quota, nil
}

// refund returns q to the hold's entity and token. Callers hold s.mu.
func (s *Server) refund(h *Hold, q int64) {
	if q <= 0 {
		return
	}
	if b, _ := s.entityBalance(h.UserID); b != nil {
		*b += q
	}
	if k := s.tokens[h.TokenID]; k != nil && !k.Unlimited {
		k.RemainQuota += q
	}
}

// close ends an active hold, returning whatever was not settled.
func (s *Server) close(h *Hold, status HoldStatus) {
	h.Released = h.Quota - h.Settled
	s.refund(h, h.Released)
	h.Status = status
}

func (s *Server) sweep() {
	now := s.now().Unix()
	for _, h := range s.holds {
		if h.Status == HoldActive && h.ExpiresAt > 0 && now > h.ExpiresAt {
			s.close(h, HoldExpired)
		}
	}
}

func (s *Server) currentBalance(userID int) int64 {
	if b, _ := s.entityBalance(userID); b != nil {
		return *b
	}
	return 0
}

// canDebit applies the spec's 402 rule to the entity, and the same rule to a
// limited token: the spec says holds mirror PreConsumeTokenQuota, which
// decrements the token, so a token without the headroom cannot be held
// against either.
func (s *Server) canDebit(userID, tokenID int, q int64) bool {
	b, _ := s.entityBalance(userID)
	if b == nil || *b < q {
		return false
	}
	if k := s.tokens[tokenID]; k != nil && !k.Unlimited && k.RemainQuota < q {
		return false
	}
	return true
}

func (s *Server) debit(userID, tokenID int, q int64) {
	b, _ := s.entityBalance(userID)
	*b -= q
	if k := s.tokens[tokenID]; k != nil && !k.Unlimited {
		k.RemainQuota -= q
	}
}

func (s *Server) verify(_ *http.Request, body []byte) response {
	var in struct {
		Key string `json:"key"`
	}
	if !decode(body, &in) {
		return fail(http.StatusBadRequest, CodeBadRequest)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweep()
	k := s.keys[in.Key]
	if k == nil || k.Disabled || (k.ExpiresAt > 0 && s.now().Unix() > k.ExpiresAt) {
		return fail(http.StatusUnauthorized, CodeInvalidKey)
	}
	u := s.users[k.UserID]
	if u == nil {
		return fail(http.StatusUnauthorized, CodeInvalidKey)
	}
	b, t := s.entityBalance(u.ID)
	if b == nil {
		return fail(http.StatusUnauthorized, CodeInvalidKey)
	}
	id := identityData{
		UserID:           u.ID,
		TenantID:         u.TenantID,
		TokenID:          k.TokenID,
		Group:            u.Group,
		Status:           "active",
		RemainQuota:      *b,
		TokenUnlimited:   k.Unlimited,
		TokenRemainQuota: k.RemainQuota,
	}
	if u.Disabled {
		id.Status = "disabled"
		return response{status: http.StatusForbidden, msg: CodeDisabled, data: id}
	}
	if t != nil && t.Suspended {
		id.Status = "suspended"
		return response{status: http.StatusForbidden, msg: CodeSuspended, data: id}
	}
	return ok(id)
}

func (s *Server) hold(_ *http.Request, body []byte) response {
	var in struct {
		HoldID    string `json:"hold_id"`
		UserID    int    `json:"user_id"`
		TokenID   int    `json:"token_id"`
		Quota     int64  `json:"quota"`
		JobID     string `json:"job_id"`
		SKU       string `json:"sku"`
		ExpiresAt int64  `json:"expires_at"`
	}
	if !decode(body, &in) || in.HoldID == "" || in.Quota <= 0 {
		return fail(http.StatusBadRequest, CodeBadRequest)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweep()
	// Replay returns the original row, even if the replayed body differs.
	if prev, seen := s.holdResp[in.HoldID]; seen {
		return ok(prev)
	}
	u := s.users[in.UserID]
	k := s.tokens[in.TokenID]
	if u == nil || k == nil || k.UserID != u.ID {
		return fail(http.StatusBadRequest, CodeBadRequest)
	}
	if !s.canDebit(u.ID, k.TokenID, in.Quota) {
		return fail(http.StatusPaymentRequired, CodeInsufficientBalance)
	}
	s.debit(u.ID, k.TokenID, in.Quota)
	s.holds[in.HoldID] = &Hold{
		HoldID:    in.HoldID,
		UserID:    u.ID,
		TenantID:  u.TenantID,
		TokenID:   k.TokenID,
		JobID:     in.JobID,
		SKU:       in.SKU,
		Quota:     in.Quota,
		Status:    HoldActive,
		ExpiresAt: in.ExpiresAt,
	}
	out := holdData{HoldID: in.HoldID, Quota: in.Quota, RemainQuota: s.currentBalance(u.ID)}
	s.holdResp[in.HoldID] = out
	return ok(out)
}

// lookupActive returns the hold for a mutating call, or the refusal. Callers
// hold s.mu.
func (s *Server) lookupActive(id string) (*Hold, *response) {
	h := s.holds[id]
	if h == nil {
		r := fail(http.StatusNotFound, CodeHoldNotFound)
		return nil, &r
	}
	switch h.Status {
	case HoldExpired:
		r := fail(http.StatusConflict, CodeHoldExpired)
		return h, &r
	case HoldActive:
		return h, nil
	default:
		r := fail(http.StatusConflict, CodeHoldClosed)
		return h, &r
	}
}

func (s *Server) settle(r *http.Request, body []byte) response {
	holdID := r.PathValue("hold_id")
	var in struct {
		EventID         string `json:"event_id"`
		CumulativeQuota int64  `json:"cumulative_quota"`
		GPUSeconds      int64  `json:"gpu_seconds"`
		SKU             string `json:"sku"`
		Final           bool   `json:"final"`
	}
	if !decode(body, &in) || in.EventID == "" || in.CumulativeQuota < 0 {
		return fail(http.StatusBadRequest, CodeBadRequest)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweep()
	if ev, seen := s.events[in.EventID]; seen {
		if ev.holdID != holdID {
			return fail(http.StatusConflict, CodeIdempotencyConflict)
		}
		return ok(ev.resp)
	}
	h, refusal := s.lookupActive(holdID)
	if refusal != nil {
		return *refusal
	}
	if in.CumulativeQuota > h.Quota {
		return fail(http.StatusConflict, CodeHoldExceeded)
	}
	// A cumulative below what is already settled moves nothing: the bridge
	// has no refund verb, and an out-of-order event must not un-charge.
	delta := in.CumulativeQuota - h.Settled
	if delta < 0 {
		delta = 0
	}
	h.Settled += delta
	sku := in.SKU
	if sku == "" {
		sku = h.SKU
	}
	s.logs = append(s.logs, ConsumeLog{
		UserID:     h.UserID,
		TenantID:   h.TenantID,
		TokenID:    h.TokenID,
		ModelName:  "compute/" + sku,
		Quota:      delta,
		GPUSeconds: in.GPUSeconds,
		JobID:      h.JobID,
		HoldID:     h.HoldID,
		EventID:    in.EventID,
	})
	if in.Final {
		s.close(h, HoldSettled)
	}
	out := settleData{HoldID: h.HoldID, SettledQuota: h.Settled, HoldRemaining: h.Quota - h.Settled - h.Released}
	s.events[in.EventID] = settledEvent{holdID: holdID, resp: out}
	return ok(out)
}

func (s *Server) extend(r *http.Request, body []byte) response {
	holdID := r.PathValue("hold_id")
	var in struct {
		ExtendID        string `json:"extend_id"`
		AdditionalQuota int64  `json:"additional_quota"`
	}
	if !decode(body, &in) || in.AdditionalQuota <= 0 || in.ExtendID == "" || len(in.ExtendID) > MaxExtendIDLen {
		return fail(http.StatusBadRequest, CodeBadRequest)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweep()
	// Checked before the hold's state so a retry of an extend that committed
	// still gets its original answer after the hold has closed. extend_ids are
	// unique across holds, like event_ids.
	if prev, seen := s.extends[in.ExtendID]; seen {
		if prev.holdID != holdID || prev.amount != in.AdditionalQuota {
			return fail(http.StatusConflict, CodeIdempotencyConflict)
		}
		return ok(prev.resp)
	}
	h, refusal := s.lookupActive(holdID)
	if refusal != nil {
		return *refusal
	}
	if !s.canDebit(h.UserID, h.TokenID, in.AdditionalQuota) {
		return fail(http.StatusPaymentRequired, CodeInsufficientBalance)
	}
	s.debit(h.UserID, h.TokenID, in.AdditionalQuota)
	h.Quota += in.AdditionalQuota
	out := holdData{HoldID: h.HoldID, Quota: h.Quota, RemainQuota: s.currentBalance(h.UserID)}
	s.extends[in.ExtendID] = extendRecord{holdID: holdID, amount: in.AdditionalQuota, resp: out}
	return ok(out)
}

func (s *Server) release(r *http.Request, _ []byte) response {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweep()
	h, refusal := s.lookupActive(r.PathValue("hold_id"))
	if h != nil && (h.Status == HoldReleased || h.Status == HoldSettled) {
		// Idempotent: a repeat reports what was released the first time and
		// moves nothing. A hold closed by final settle counts as released.
		return ok(releaseData{ReleasedQuota: h.Released, RemainQuota: s.currentBalance(h.UserID)})
	}
	if refusal != nil {
		return *refusal
	}
	s.close(h, HoldReleased)
	return ok(releaseData{ReleasedQuota: h.Released, RemainQuota: s.currentBalance(h.UserID)})
}
