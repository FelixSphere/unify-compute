package consoletest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/FelixSphere/unify-compute/compute-api/internal/console"
)

// The vector pinned in internal/console/client_test.go. The hex is computed
// independently (openssl dgst -sha256 -hmac) so this test fails if either the
// client or the fake drifts from the spec's canonical string.
var (
	vectorSecret = []byte("0123456789abcdef0123456789abcdef")
	vectorBody   = []byte(`{"key":"sk-test"}`)
	vectorPath   = "/api/compute/v1/verify"
	vectorTS     = int64(1757520000)
	vectorSig    = "aba83132084d4c2cfefd1fd39b324e90358975c6b5ade128a7a6b960285eb29c"
)

func at(ts int64) func() time.Time { return func() time.Time { return time.Unix(ts, 0) } }

// seeded returns a fake on the vector's clock with:
//
//	user 1042 (no tenant, 25 USD), token 771 key sk-test limited to 8 USD
//	user 2001 in tenant 7 (100 USD), token 880 key sk-tenant unlimited
func seeded(t *testing.T) (*Server, *console.Client) {
	t.Helper()
	s := New(vectorSecret)
	t.Cleanup(s.Close)
	s.SetNow(at(vectorTS))
	s.SeedUser(User{ID: 1042, Quota: 25 * QuotaPerUnit})
	s.SeedKey(Key{Key: "sk-test", TokenID: 771, UserID: 1042, RemainQuota: 8 * QuotaPerUnit})
	s.SeedTenant(Tenant{ID: 7, Quota: 100 * QuotaPerUnit})
	s.SeedUser(User{ID: 2001, TenantID: 7})
	s.SeedKey(Key{Key: "sk-tenant", TokenID: 880, UserID: 2001, Unlimited: true})
	return s, s.Client()
}

func status(t *testing.T, err error) int {
	t.Helper()
	var e *console.Error
	if !errors.As(err, &e) {
		t.Fatalf("want *console.Error, got %v", err)
	}
	return e.Status
}

// refusal asserts a bridge refusal by status and the spec's code in message.
func refusal(t *testing.T, err error, wantStatus int, wantCode string) {
	t.Helper()
	var e *console.Error
	if !errors.As(err, &e) || e.Status != wantStatus || e.Message != wantCode {
		t.Errorf("want %d %s, got %v", wantStatus, wantCode, err)
	}
}

type rawResponse struct {
	status int
	header http.Header
	body   []byte
	env    struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
		Code    string `json:"code"`
	}
}

// post sends a correctly signed request without console.Client, for what the
// client deliberately hides: response bodies, headers, malformed requests.
func post(t *testing.T, s *Server, path string, in any) rawResponse {
	t.Helper()
	body, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	full := "/api/compute/v1" + path
	ts := s.Now().Unix()
	req, _ := http.NewRequest("POST", s.URL+full, bytes.NewReader(body))
	req.Header.Set(console.HeaderTimestamp, strconv.FormatInt(ts, 10))
	req.Header.Set(console.HeaderSignature, console.Sign(vectorSecret, "POST", full, ts, body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out := rawResponse{status: resp.StatusCode, header: resp.Header}
	out.body, _ = io.ReadAll(resp.Body)
	if len(out.body) > 0 {
		_ = json.Unmarshal(out.body, &out.env)
	}
	return out
}

func ext(id string, q int64) console.ExtendRequest {
	return console.ExtendRequest{ExtendID: id, AdditionalQuota: q}
}

func holdReq(id string, q int64) console.HoldRequest {
	return console.HoldRequest{HoldID: id, UserID: 1042, TokenID: 771, Quota: q, JobID: "job_" + id, SKU: "a10g-24gb-x1", ExpiresAt: vectorTS + 3600}
}

func TestPinnedVectorIsAcceptedByTheVerifier(t *testing.T) {
	if got := console.Sign(vectorSecret, "POST", vectorPath, vectorTS, vectorBody); got != vectorSig {
		t.Fatalf("client signature %s, pinned %s", got, vectorSig)
	}
	now := time.Unix(vectorTS, 0)
	ts := strconv.FormatInt(vectorTS, 10)
	if err := VerifySignature(vectorSecret, "POST", vectorPath, ts, vectorSig, vectorBody, now); err != nil {
		t.Fatalf("pinned vector refused: %v", err)
	}
	for name, err := range map[string]error{
		"body":       VerifySignature(vectorSecret, "POST", vectorPath, ts, vectorSig, []byte(`{"key":"sk-tesT"}`), now),
		"path":       VerifySignature(vectorSecret, "POST", "/api/compute/v1/holds", ts, vectorSig, vectorBody, now),
		"method":     VerifySignature(vectorSecret, "PUT", vectorPath, ts, vectorSig, vectorBody, now),
		"secret":     VerifySignature([]byte("fedcba9876543210fedcba9876543210"), "POST", vectorPath, ts, vectorSig, vectorBody, now),
		"missing":    VerifySignature(vectorSecret, "POST", vectorPath, ts, "", vectorBody, now),
		"stale 31s":  VerifySignature(vectorSecret, "POST", vectorPath, ts, vectorSig, vectorBody, now.Add(31*time.Second)),
		"future 31s": VerifySignature(vectorSecret, "POST", vectorPath, ts, vectorSig, vectorBody, now.Add(-31*time.Second)),
	} {
		if err == nil {
			t.Errorf("%s: tampered request accepted", name)
		}
	}
	if err := VerifySignature(vectorSecret, "POST", vectorPath, ts, vectorSig, vectorBody, now.Add(30*time.Second)); err != nil {
		t.Errorf("30 s old is inside the window: %v", err)
	}
}

func TestPinnedVectorOverHTTP(t *testing.T) {
	s, _ := seeded(t)
	send := func(sig string) int {
		req, _ := http.NewRequest("POST", s.URL+vectorPath, bytes.NewReader(vectorBody))
		req.Header.Set(console.HeaderTimestamp, strconv.FormatInt(vectorTS, 10))
		req.Header.Set(console.HeaderSignature, sig)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if got := send(vectorSig); got != 200 {
		t.Fatalf("pinned vector over HTTP: %d", got)
	}
	if got := send(vectorSig[:63] + "0"); got != 401 {
		t.Fatalf("bad signature: %d, want 401", got)
	}
	s.SetNow(at(vectorTS + 31))
	if got := send(vectorSig); got != 401 {
		t.Fatalf("stale timestamp: %d, want 401", got)
	}
}

func TestMissingAuthHeadersGetTheirOwnCodes(t *testing.T) {
	s, _ := seeded(t)
	for name, tc := range map[string]struct{ ts, sig, want string }{
		"no timestamp": {"", vectorSig, CodeInvalidTimestamp},
		"no signature": {strconv.FormatInt(vectorTS, 10), "", CodeInvalidSignature},
	} {
		req, _ := http.NewRequest("POST", s.URL+vectorPath, bytes.NewReader(vectorBody))
		if tc.ts != "" {
			req.Header.Set(console.HeaderTimestamp, tc.ts)
		}
		if tc.sig != "" {
			req.Header.Set(console.HeaderSignature, tc.sig)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var env struct{ Message string }
		_ = json.NewDecoder(resp.Body).Decode(&env)
		resp.Body.Close()
		if resp.StatusCode != 401 || env.Message != tc.want {
			t.Errorf("%s: %d %q, want 401 %s", name, resp.StatusCode, env.Message, tc.want)
		}
	}
}

func TestEveryRouteIs404WithNoBodyWithoutASecret(t *testing.T) {
	s := New(nil)
	defer s.Close()
	c, _ := console.New(s.URL, vectorSecret)
	ctx := context.Background()
	_, e1 := c.Verify(ctx, "sk-test")
	_, e2 := c.Hold(ctx, holdReq("h", 1))
	_, e3 := c.Settle(ctx, "h", console.SettleRequest{EventID: "e", SKU: "a10g-24gb-x1"})
	_, e4 := c.Extend(ctx, "h", ext("ext_1", 1))
	_, e5 := c.Release(ctx, "h")
	for i, err := range []error{e1, e2, e3, e4, e5} {
		if !errors.Is(err, console.ErrBridgeDisabled) || errors.Is(err, console.ErrHoldNotFound) {
			t.Errorf("verb %d: want ErrBridgeDisabled, got %v", i, err)
		}
	}
	if n := len(s.Calls()); n != 5 {
		t.Errorf("recorded %d calls, want 5", n)
	}
	for _, p := range []string{"/verify", "/holds", "/holds/h/settle", "/holds/h/extend", "/holds/h/release"} {
		if r := post(t, s, p, map[string]any{}); r.status != 404 || len(r.body) != 0 {
			t.Errorf("%s disabled: %d with body %q, want 404 and no body", p, r.status, r.body)
		}
	}
}

func TestVerify(t *testing.T) {
	s, c := seeded(t)
	ctx := context.Background()

	id, err := c.Verify(ctx, "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	want := console.Identity{UserID: 1042, TokenID: 771, Group: "default", Status: "active", RemainQuota: 25 * QuotaPerUnit, TokenRemainQuota: 8 * QuotaPerUnit}
	if *id != want {
		t.Fatalf("identity %+v, want %+v", *id, want)
	}

	id, err = c.Verify(ctx, "sk-tenant")
	if err != nil {
		t.Fatal(err)
	}
	if id.TenantID != 7 || id.RemainQuota != 100*QuotaPerUnit || !id.TokenUnlimited {
		t.Fatalf("tenant identity should carry the tenant's balance: %+v", *id)
	}

	s.SeedKey(Key{Key: "sk-off", TokenID: 1, UserID: 1042, Disabled: true})
	s.SeedKey(Key{Key: "sk-old", TokenID: 2, UserID: 1042, ExpiresAt: vectorTS - 1})
	s.SeedUser(User{ID: 3, Disabled: true})
	s.SeedKey(Key{Key: "sk-banned", TokenID: 3, UserID: 3})
	for _, k := range []string{"sk-nope", "sk-off", "sk-old"} {
		_, err := c.Verify(ctx, k)
		refusal(t, err, 401, CodeInvalidKey)
	}

	s.SeedTenant(Tenant{ID: 7, Quota: 100 * QuotaPerUnit, Suspended: true})
	for key, want := range map[string]string{"sk-banned": console.AccountDisabled, "sk-tenant": console.AccountSuspended} {
		_, err := c.Verify(ctx, key)
		refusal(t, err, 403, want)
		var e *console.Error
		if !errors.As(err, &e) || e.AccountStatus != want {
			t.Errorf("%s: AccountStatus from data.status: got %v, want %q", key, err, want)
		}
	}
}

func TestHoldSettleReleaseMovesQuota(t *testing.T) {
	s, c := seeded(t)
	ctx := context.Background()

	h, err := c.Hold(ctx, holdReq("hold_1", 1_270_000))
	if err != nil {
		t.Fatal(err)
	}
	if h.RemainQuota != 25*QuotaPerUnit-1_270_000 || s.TokenRemainQuota(771) != 8*QuotaPerUnit-1_270_000 {
		t.Fatalf("hold did not debit entity and token: %+v token=%d", h, s.TokenRemainQuota(771))
	}

	st, err := c.Settle(ctx, "hold_1", console.SettleRequest{EventID: "ue_1", CumulativeQuota: 430_000, GPUSeconds: 1290, SKU: "a10g-24gb-x1"})
	if err != nil {
		t.Fatal(err)
	}
	if st.SettledQuota != 430_000 || st.HoldRemaining != 840_000 {
		t.Fatalf("settle %+v", st)
	}
	if _, err := c.Settle(ctx, "hold_1", console.SettleRequest{EventID: "ue_2", CumulativeQuota: 500_000, GPUSeconds: 1500, SKU: "a10g-24gb-x1"}); err != nil {
		t.Fatal(err)
	}

	r, err := c.Release(ctx, "hold_1")
	if err != nil {
		t.Fatal(err)
	}
	if r.ReleasedQuota != 770_000 || r.RemainQuota != 25*QuotaPerUnit-500_000 {
		t.Fatalf("release %+v", r)
	}
	if s.TokenRemainQuota(771) != 8*QuotaPerUnit-500_000 {
		t.Fatalf("token not refunded: %d", s.TokenRemainQuota(771))
	}
	again, err := c.Release(ctx, "hold_1")
	if err != nil || *again != *r {
		t.Fatalf("release is idempotent: %+v %v", again, err)
	}
	if s.Balance(1042) != 25*QuotaPerUnit-500_000 {
		t.Fatalf("second release moved quota: %d", s.Balance(1042))
	}

	logs := s.ConsumeLogs()
	if len(logs) != 2 || logs[0].Quota != 430_000 || logs[1].Quota != 70_000 ||
		logs[0].ModelName != "compute/a10g-24gb-x1" || logs[1].GPUSeconds != 1500 || logs[0].JobID != "job_hold_1" {
		t.Fatalf("consume logs %+v", logs)
	}
	if _, err := c.Settle(ctx, "hold_1", console.SettleRequest{EventID: "ue_3", CumulativeQuota: 600_000, SKU: "a10g-24gb-x1"}); status(t, err) != 409 {
		t.Fatalf("settle after release: want 409, got %v", err)
	}
}

func TestTenantHoldSpendsTheTenantAndLeavesUnlimitedTokens(t *testing.T) {
	s, c := seeded(t)
	_, err := c.Hold(context.Background(), console.HoldRequest{HoldID: "ht", UserID: 2001, TokenID: 880, Quota: 2 * QuotaPerUnit, ExpiresAt: vectorTS + 60})
	if err != nil {
		t.Fatal(err)
	}
	if s.Balance(2001) != 98*QuotaPerUnit || s.TokenRemainQuota(880) != 0 {
		t.Fatalf("tenant=%d token=%d", s.Balance(2001), s.TokenRemainQuota(880))
	}
}

func TestHoldIsIdempotentAndRefusesWithoutBalance(t *testing.T) {
	s, c := seeded(t)
	ctx := context.Background()
	first, err := c.Hold(ctx, holdReq("hold_1", QuotaPerUnit))
	if err != nil {
		t.Fatal(err)
	}
	replay, err := c.Hold(ctx, holdReq("hold_1", QuotaPerUnit))
	if err != nil || *replay != *first {
		t.Fatalf("replay %+v %v, want %+v", replay, err, first)
	}
	if s.Balance(1042) != 24*QuotaPerUnit {
		t.Fatalf("replay debited twice: %d", s.Balance(1042))
	}
	for _, req := range map[string]console.HoldRequest{
		"quota":      holdReq("hold_1", QuotaPerUnit+1),
		"sku":        func() console.HoldRequest { r := holdReq("hold_1", QuotaPerUnit); r.SKU = "h100-80gb-x1"; return r }(),
		"expires_at": func() console.HoldRequest { r := holdReq("hold_1", QuotaPerUnit); r.ExpiresAt++; return r }(),
	} {
		_, err := c.Hold(ctx, req)
		refusal(t, err, 409, CodeIdempotencyConflict)
	}
	if s.Balance(1042) != 24*QuotaPerUnit {
		t.Fatalf("a conflicting replay moved quota: %d", s.Balance(1042))
	}

	// The token's 8 USD limit binds before the user's 24 USD balance.
	_, err = c.Hold(ctx, holdReq("hold_2", 7*QuotaPerUnit+1))
	refusal(t, err, 402, CodeInsufficientBalance)
	s.SeedKey(Key{Key: "sk-test", TokenID: 771, UserID: 1042, Unlimited: true})
	_, err = c.Hold(ctx, holdReq("hold_3", 24*QuotaPerUnit+1))
	refusal(t, err, 402, CodeInsufficientBalance)
	if _, err := c.Hold(ctx, holdReq("hold_4", 24*QuotaPerUnit)); err != nil {
		t.Fatalf("exact balance should hold: %v", err)
	}
	if s.Balance(1042) != 0 {
		t.Fatalf("balance %d", s.Balance(1042))
	}
	if r := post(t, s, "/holds", map[string]any{"hold_id": "hold_5"}); r.status != 400 || r.env.Message != CodeInvalidRequest {
		t.Fatalf("hold without quota: %d %s", r.status, r.body)
	}
}

func TestSettleIsIdempotentBoundedAndFinal(t *testing.T) {
	s, c := seeded(t)
	ctx := context.Background()
	if _, err := c.Hold(ctx, holdReq("hold_1", 1_000_000)); err != nil {
		t.Fatal(err)
	}
	ev := console.SettleRequest{EventID: "ue_1", CumulativeQuota: 400_000, SKU: "a10g-24gb-x1"}
	a, _ := c.Settle(ctx, "hold_1", ev)
	b, err := c.Settle(ctx, "hold_1", ev)
	if err != nil || *a != *b || len(s.ConsumeLogs()) != 1 {
		t.Fatalf("replayed event charged again: %+v %+v logs=%d", a, b, len(s.ConsumeLogs()))
	}

	_, err = c.Settle(ctx, "hold_1", console.SettleRequest{EventID: "ue_2", CumulativeQuota: 1_000_001, SKU: "a10g-24gb-x1"})
	refusal(t, err, 409, CodeHoldExceeded)
	_, err = c.Settle(ctx, "hold_1", console.SettleRequest{EventID: "ue_2b", CumulativeQuota: 399_999, SKU: "a10g-24gb-x1"})
	refusal(t, err, 409, CodeCumulativeQuotaRegressed)
	_, err = c.Settle(ctx, "hold_1", console.SettleRequest{EventID: "ue_2c", CumulativeQuota: 500_000, SKU: "h100-80gb-x1"})
	refusal(t, err, 409, CodeSKUMismatch)
	_, err = c.Settle(ctx, "hold_1", console.SettleRequest{EventID: "ue_2e", CumulativeQuota: 500_000})
	refusal(t, err, 400, CodeInvalidRequest)
	if len(s.ConsumeLogs()) != 1 {
		t.Fatalf("refused settles wrote consume logs: %+v", s.ConsumeLogs())
	}
	if r, err := c.Settle(ctx, "hold_1", console.SettleRequest{EventID: "ue_2d", CumulativeQuota: 400_000, SKU: "a10g-24gb-x1"}); err != nil || r.SettledQuota != 400_000 {
		t.Fatalf("an equal cumulative is not a regression: %+v %v", r, err)
	}

	if _, err := c.Extend(ctx, "hold_1", ext("ext_1", 100_000)); err != nil {
		t.Fatal(err)
	}
	f, err := c.Settle(ctx, "hold_1", console.SettleRequest{EventID: "ue_3", CumulativeQuota: 1_000_001, Final: true, SKU: "a10g-24gb-x1"})
	if err != nil {
		t.Fatal(err)
	}
	if f.SettledQuota != 1_000_001 || f.HoldRemaining != 0 {
		t.Fatalf("final %+v", f)
	}
	if s.Balance(1042) != 25*QuotaPerUnit-1_000_001 {
		t.Fatalf("final did not release the remainder: %d", s.Balance(1042))
	}
	if h, _ := s.Hold("hold_1"); h.Status != HoldSettled || h.Released != 99_999 {
		t.Fatalf("hold %+v", h)
	}
	if r, err := c.Release(ctx, "hold_1"); err != nil || r.ReleasedQuota != 99_999 {
		t.Fatalf("release after final: %+v %v", r, err)
	}
	_, err = c.Settle(ctx, "hold_1", console.SettleRequest{EventID: "ue_5", CumulativeQuota: 1_000_001, SKU: "a10g-24gb-x1"})
	refusal(t, err, 409, CodeHoldClosed)

	_, err = c.Settle(ctx, "nope", console.SettleRequest{EventID: "ue_4", SKU: "a10g-24gb-x1"})
	if !errors.Is(err, console.ErrHoldNotFound) || errors.Is(err, console.ErrBridgeDisabled) {
		t.Fatalf("unknown hold: want ErrHoldNotFound, got %v", err)
	}
	if r := post(t, s, "/holds/nope/settle", map[string]any{"event_id": "ue_6"}); r.status != 404 || r.env.Message != CodeHoldNotFound || r.env.Code != CodeHoldNotFound {
		t.Fatalf("unknown hold must be a JSON 404 hold_not_found: %d %s", r.status, r.body)
	}
	_, err = c.Settle(ctx, "nope", ev)
	refusal(t, err, 409, CodeIdempotencyConflict)
}

func TestExtend(t *testing.T) {
	s, c := seeded(t)
	ctx := context.Background()
	if _, err := c.Hold(ctx, holdReq("hold_1", QuotaPerUnit)); err != nil {
		t.Fatal(err)
	}
	out, err := c.Extend(ctx, "hold_1", ext("ext_01J8A", 300_000))
	if err != nil || out.HoldID != "hold_1" || out.Quota != 800_000 || out.RemainQuota != 25*QuotaPerUnit-800_000 {
		t.Fatalf("extend %+v %v", out, err)
	}
	// A hold replay is compared with the original create request, not with
	// the hold's extended quota.
	if r, err := c.Hold(ctx, holdReq("hold_1", QuotaPerUnit)); err != nil || r.Quota != QuotaPerUnit {
		t.Fatalf("hold replay after extend: %+v %v", r, err)
	}
	_, err = c.Hold(ctx, holdReq("hold_1", 800_000))
	refusal(t, err, 409, CodeIdempotencyConflict)
	_, err = c.Extend(ctx, "hold_1", ext("ext_01J8B", 7*QuotaPerUnit))
	refusal(t, err, 402, CodeInsufficientBalance)
	if h, _ := s.Hold("hold_1"); h.Quota != 800_000 {
		t.Fatalf("refused extend changed the hold: %+v", h)
	}
	for name, req := range map[string]console.ExtendRequest{
		"no extend_id":    ext("", 1),
		"extend_id > 64":  ext("ext_"+strings.Repeat("A", 61), 1),
		"no amount":       ext("ext_01J8C", 0),
		"negative amount": ext("ext_01J8D", -1),
	} {
		_, err := c.Extend(ctx, "hold_1", req)
		var e *console.Error
		if !errors.As(err, &e) || e.Status != 400 || e.Message != CodeInvalidRequest {
			t.Errorf("%s: want 400 invalid_request, got %v", name, err)
		}
	}
	if _, err := c.Extend(ctx, "hold_1", ext("ext_"+strings.Repeat("A", 60), 1)); err != nil {
		t.Errorf("64-char extend_id: %v", err)
	}
	if _, err := c.Extend(ctx, "nope", ext("ext_01J8E", 1)); !errors.Is(err, console.ErrHoldNotFound) {
		t.Errorf("unknown hold: %v", err)
	}
}

func TestExtendIsIdempotentOnExtendID(t *testing.T) {
	s, c := seeded(t)
	ctx := context.Background()
	if _, err := c.Hold(ctx, holdReq("hold_1", QuotaPerUnit)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Hold(ctx, holdReq("hold_2", QuotaPerUnit)); err != nil {
		t.Fatal(err)
	}
	before := s.Balance(1042)

	// The console commits the extend and the response is lost; compute retries
	// with the same extend_id and must reserve exactly once.
	s.InjectFault(Fault{Route: RouteExtend, AfterApply: true})
	if _, err := c.Extend(ctx, "hold_1", ext("ext_01J8RETRY", 300_000)); err == nil || errors.As(err, new(*console.Error)) {
		t.Fatalf("dropped response should surface as a transport error, got %v", err)
	}
	out, err := c.Extend(ctx, "hold_1", ext("ext_01J8RETRY", 300_000))
	if err != nil || out.Quota != 800_000 || out.RemainQuota != before-300_000 {
		t.Fatalf("retry: %+v %v", out, err)
	}
	again, err := c.Extend(ctx, "hold_1", ext("ext_01J8RETRY", 300_000))
	if err != nil || *again != *out {
		t.Fatalf("second retry: %+v %v", again, err)
	}
	if s.Balance(1042) != before-300_000 || s.TokenRemainQuota(771) != 8*QuotaPerUnit-2*QuotaPerUnit-300_000 {
		t.Fatalf("reserved more than once: balance %d token %d", s.Balance(1042), s.TokenRemainQuota(771))
	}
	if h, _ := s.Hold("hold_1"); h.Quota != 800_000 {
		t.Fatalf("hold %+v", h)
	}
	if calls := s.CallsTo(RouteExtend); len(calls) != 3 || !calls[0].Applied || !calls[0].Injected {
		t.Fatalf("extend calls %+v", calls)
	}

	_, err = c.Extend(ctx, "hold_1", ext("ext_01J8RETRY", 300_001))
	refusal(t, err, 409, CodeIdempotencyConflict)
	_, err = c.Extend(ctx, "hold_2", ext("ext_01J8RETRY", 300_000))
	refusal(t, err, 409, CodeIdempotencyConflict)
	if s.Balance(1042) != before-300_000 {
		t.Fatalf("a conflicting extend moved quota: %d", s.Balance(1042))
	}

	// A replay answers with the hold's current state, so after a later extend
	// it reports the larger quota, and it still succeeds once the hold closed.
	if _, err := c.Extend(ctx, "hold_1", ext("ext_01J8MORE", 100_000)); err != nil {
		t.Fatal(err)
	}
	if r, err := c.Extend(ctx, "hold_1", ext("ext_01J8RETRY", 300_000)); err != nil || r.Quota != 900_000 || r.RemainQuota != before-400_000 {
		t.Fatalf("replay reports current state: %+v %v", r, err)
	}
	if _, err := c.Release(ctx, "hold_1"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Extend(ctx, "hold_1", ext("ext_01J8RETRY", 300_000)); err != nil {
		t.Fatalf("replay after release: %v", err)
	}
	_, err = c.Extend(ctx, "hold_1", ext("ext_01J8NEW", 1))
	refusal(t, err, 409, CodeHoldClosed)
}

func TestRateLimitIsOffByDefaultAndAnswers429WithRetryAfter(t *testing.T) {
	s, c := seeded(t)
	ctx := context.Background()
	for i := 0; i < DefaultBurst+20; i++ {
		if _, err := c.Verify(ctx, "sk-test"); err != nil {
			t.Fatalf("call %d with the limiter off: %v", i, err)
		}
	}

	s.SetRateLimit(DefaultRate, DefaultBurst)
	for i := 0; i < DefaultBurst; i++ {
		if _, err := c.Verify(ctx, "sk-test"); err != nil {
			t.Fatalf("call %d inside the burst: %v", i, err)
		}
	}
	r := post(t, s, "/verify", map[string]string{"key": "sk-test"})
	if r.status != 429 || r.env.Message != CodeRateLimited || r.header.Get("Retry-After") != "1" {
		t.Fatalf("over the burst: %d Retry-After=%q %s", r.status, r.header.Get("Retry-After"), r.body)
	}
	_, err := c.Verify(ctx, "sk-test")
	refusal(t, err, 429, CodeRateLimited)

	// The limiter runs before HMAC: an unsigned request is also a 429.
	resp, err := http.Post(s.URL+vectorPath, "application/json", bytes.NewReader(vectorBody))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 429 {
		t.Fatalf("unsigned request over the limit: %d, want 429", resp.StatusCode)
	}

	// One second on the fake clock refills DefaultRate tokens.
	s.SetNow(at(vectorTS + 1))
	for i := 0; i < DefaultRate; i++ {
		if _, err := c.Verify(ctx, "sk-test"); err != nil {
			t.Fatalf("call %d after refill: %v", i, err)
		}
	}
	if _, err := c.Verify(ctx, "sk-test"); status(t, err) != 429 {
		t.Fatalf("refill is bounded by the rate: %v", err)
	}

	s.SetRateLimit(0, 0)
	if _, err := c.Verify(ctx, "sk-test"); err != nil {
		t.Fatalf("limiter off again: %v", err)
	}
}

func TestExpiredHoldsAreSweptAndReported(t *testing.T) {
	s, c := seeded(t)
	ctx := context.Background()
	if _, err := c.Hold(ctx, holdReq("hold_1", QuotaPerUnit)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Settle(ctx, "hold_1", console.SettleRequest{EventID: "ue_1", CumulativeQuota: 100_000, SKU: "a10g-24gb-x1"}); err != nil {
		t.Fatal(err)
	}
	// Past expires_at, and far enough that the client signs with the new clock.
	s.SetNow(at(vectorTS + 3601))
	s.Sweep()
	if s.Balance(1042) != 25*QuotaPerUnit-100_000 {
		t.Fatalf("sweep did not return the unsettled part: %d", s.Balance(1042))
	}
	_, err := c.Settle(ctx, "hold_1", console.SettleRequest{EventID: "ue_2", CumulativeQuota: 200_000, SKU: "a10g-24gb-x1"})
	refusal(t, err, 409, CodeHoldExpired)
	_, err = c.Release(ctx, "hold_1")
	refusal(t, err, 409, CodeHoldExpired)
	_, err = c.Extend(ctx, "hold_1", ext("ext_1", 1))
	refusal(t, err, 409, CodeHoldExpired)
	if r, err := c.Settle(ctx, "hold_1", console.SettleRequest{EventID: "ue_1", CumulativeQuota: 100_000, SKU: "a10g-24gb-x1"}); err != nil || r.SettledQuota != 100_000 {
		t.Errorf("an event settled before expiry still replays: %+v %v", r, err)
	}
}

func TestInjectedFaultsAreRecordedAndDoNotTouchTheLedger(t *testing.T) {
	s, c := seeded(t)
	ctx := context.Background()

	s.RefuseNext(2, http.StatusServiceUnavailable)
	for i := 0; i < 2; i++ {
		if _, err := c.Hold(ctx, holdReq("hold_1", QuotaPerUnit)); status(t, err) != 503 {
			t.Fatalf("attempt %d: want 503, got %v", i, err)
		}
	}
	if s.Balance(1042) != 25*QuotaPerUnit {
		t.Fatal("a refused call moved quota")
	}
	if _, err := c.Hold(ctx, holdReq("hold_1", QuotaPerUnit)); err != nil {
		t.Fatalf("third attempt should pass: %v", err)
	}

	s.SetDown(true)
	if _, err := c.Verify(ctx, "sk-test"); err == nil || errors.As(err, new(*console.Error)) {
		t.Fatalf("down console should be a transport error, got %v", err)
	}
	s.SetDown(false)

	s.InjectFault(Fault{Route: RouteRelease, Status: http.StatusBadGateway})
	if _, err := c.Verify(ctx, "sk-test"); err != nil {
		t.Fatalf("a release fault must not hit verify: %v", err)
	}
	if _, err := c.Release(ctx, "hold_1"); status(t, err) != 502 {
		t.Fatalf("want 502, got %v", err)
	}

	s.SetLatency(200 * time.Millisecond)
	c.HTTP.Timeout = 50 * time.Millisecond
	if _, err := c.Verify(ctx, "sk-test"); err == nil {
		t.Fatal("latency past the client timeout should fail")
	}
	s.ClearFaults()

	holds := s.CallsTo(RouteHold)
	if len(holds) != 3 || !holds[0].Injected || holds[0].Status != 503 || holds[2].Injected || !holds[2].Applied {
		t.Fatalf("hold calls %+v", holds)
	}
	if !bytes.Contains(holds[2].Body, []byte(`"hold_id":"hold_1"`)) {
		t.Fatalf("recorded body %s", holds[2].Body)
	}
}

// The scenario C5 has to survive: the console stops answering mid-job, and a
// settle commits on the console but its response is lost. Retrying with the
// same event id and cumulative amounts must charge exactly what was used.
func TestConsoleDownMidJobLosesNoUsageAndDoubleChargesNothing(t *testing.T) {
	s, c := seeded(t)
	ctx := context.Background()
	if _, err := c.Hold(ctx, holdReq("hold_1", 2*QuotaPerUnit)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Settle(ctx, "hold_1", console.SettleRequest{EventID: "ue_1", CumulativeQuota: 100_000, SKU: "a10g-24gb-x1"}); err != nil {
		t.Fatal(err)
	}

	s.InjectFault(Fault{Route: RouteSettle, AfterApply: true})
	if _, err := c.Settle(ctx, "hold_1", console.SettleRequest{EventID: "ue_2", CumulativeQuota: 250_000, SKU: "a10g-24gb-x1"}); err == nil {
		t.Fatal("dropped response should surface as an error")
	}
	s.SetDown(true)
	if _, err := c.Settle(ctx, "hold_1", console.SettleRequest{EventID: "ue_3", CumulativeQuota: 400_000, SKU: "a10g-24gb-x1"}); err == nil {
		t.Fatal("down console should fail")
	}
	s.SetDown(false)

	for _, ev := range []console.SettleRequest{
		{EventID: "ue_2", CumulativeQuota: 250_000, SKU: "a10g-24gb-x1"},
		{EventID: "ue_3", CumulativeQuota: 400_000, SKU: "a10g-24gb-x1"},
		{EventID: "ue_4", CumulativeQuota: 550_000, Final: true, SKU: "a10g-24gb-x1"},
	} {
		if _, err := c.Settle(ctx, "hold_1", ev); err != nil {
			t.Fatalf("%s: %v", ev.EventID, err)
		}
	}

	var charged int64
	for _, l := range s.ConsumeLogs() {
		charged += l.Quota
	}
	if charged != 550_000 || s.Balance(1042) != 25*QuotaPerUnit-550_000 {
		t.Fatalf("charged %d, balance %d; want exactly 550000 used", charged, s.Balance(1042))
	}
	if n := len(s.ConsumeLogs()); n != 4 {
		t.Fatalf("%d consume logs, want one per distinct event", n)
	}
	if dropped := s.CallsTo(RouteSettle)[1]; !dropped.Applied || !dropped.Injected || dropped.Status != 0 {
		t.Fatalf("dropped call record %+v", dropped)
	}
}
