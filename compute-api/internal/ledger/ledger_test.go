package ledger

import (
	"errors"
	"testing"
	"time"

	"github.com/FelixSphere/unify-compute/compute-api/internal/console/consoletest"
	"github.com/FelixSphere/unify-compute/compute-api/internal/store"
)

// Test names carry the row ids of docs/spec/metering.md: B (billable state),
// H (holds), R (settlement), I (interruption and retry).

func TestB1_QueuedHoldPlacedNotCharged(t *testing.T) {
	h := newHarness(t)
	job := h.placeHold(7200)
	h.idle(1800, 60)

	ch, ok := h.fake.Hold(h.hold(job).HoldID)
	if !ok || ch.Status != consoletest.HoldActive || ch.Quota != quota(testRate, 7200) {
		t.Fatalf("console hold = %+v, want active for quota(7200)", ch)
	}
	if n := len(h.fake.CallsTo(consoletest.RouteSettle)); n != 0 {
		t.Errorf("%d settles while queued, want 0", n)
	}
	if b := h.fake.Balance(testUser); b != testBalance-ch.Quota {
		t.Errorf("balance %d, want locked by the hold only", b)
	}
	if len(h.events(job)) != 0 {
		t.Error("queued job has usage events")
	}
}

func TestH1_InitialHoldIsCeilingUpToTwoHours(t *testing.T) {
	h := newHarness(t)
	for _, tc := range []struct{ max, held int64 }{{1800, 1800}, {7200, 7200}, {36000, 7200}} {
		hd, err := h.eng.PlaceHold(h.ctx, h.job(store.NewJobID(h.clock.Now()), tc.max))
		h.must(err)
		if hd.OriginalQuota != quota(testRate, tc.held) || hd.CeilingQuota != quota(testRate, tc.max) {
			t.Errorf("max %d: hold %d ceiling %d, want quota(%d) and quota(%d)", tc.max, hd.OriginalQuota, hd.CeilingQuota, tc.held, tc.max)
		}
	}
}

func TestH6_HoldExpiresAfterRuntimePlus48h(t *testing.T) {
	h := newHarness(t)
	h.clock.Set(h.at(12.7))
	job := h.placeHold(3600)
	ch, _ := h.fake.Hold(h.hold(job).HoldID)
	want := h.at(12).Add(3600*time.Second + 48*time.Hour).Unix()
	if ch.ExpiresAt != want {
		t.Errorf("expires_at %d, want submitted + max_runtime + 48h = %d", ch.ExpiresAt, want)
	}
}

func TestH2_RefusedHoldCarriesHoldAndCeiling(t *testing.T) {
	h := newHarness(t)
	h.setBalance(1000)
	_, err := h.eng.PlaceHold(h.ctx, h.job("job_01J8REFUSED00000000000000", 36000))
	var ib *InsufficientBalanceError
	if !errors.As(err, &ib) {
		t.Fatalf("err = %v, want InsufficientBalanceError", err)
	}
	if ib.HoldQuota != quota(testRate, 7200) || ib.CeilingQuota != quota(testRate, 36000) {
		t.Errorf("refusal carries %d / %d, want quota(7200) / quota(36000)", ib.HoldQuota, ib.CeilingQuota)
	}
	if b := h.fake.Balance(testUser); b != 1000 {
		t.Errorf("balance %d after a refused hold, want unchanged", b)
	}
}

func TestPlaceHoldLostResponseReservesOnce(t *testing.T) {
	h := newHarness(t)
	h.fake.InjectFault(consoletest.Fault{Route: consoletest.RouteHold, Status: 0, AfterApply: true})
	j := h.job("job_01J8LOSTHOLD0000000000000", 3600)
	if _, err := h.eng.PlaceHold(h.ctx, j); err == nil {
		t.Fatal("place hold succeeded through a dropped response")
	}
	hd, err := h.eng.PlaceHold(h.ctx, j)
	h.must(err)
	if hd.State != "open" {
		t.Fatalf("state %q after retry, want open", hd.State)
	}
	if b := h.fake.Balance(testUser); b != testBalance-quota(testRate, 3600) {
		t.Errorf("balance %d, want one hold of quota(3600) reserved", b)
	}
}

func TestB2_I1_ProvisioningNeverBilled(t *testing.T) {
	h := newHarness(t)
	job := h.placeHold(3600)
	h.idle(7200, 60) // two hours waiting for capacity
	if len(h.events(job)) != 0 || len(h.fake.CallsTo(consoletest.RouteSettle)) != 0 {
		t.Fatal("provisioning produced usage")
	}
	h.start(job, 1, 7200)
	h.run(job, 1, 7800, 60)
	h.must(h.eng.Stopped(h.ctx, job, 1, h.at(7800), EndExited))
	h.finish(job)
	h.assertSettledExactly(job, 600)
}

func TestB3_R1_R3_RunningSettledEveryFiveMinutesLedgerFirst(t *testing.T) {
	h := newHarness(t)
	job := h.placeHold(3600)
	h.start(job, 1, 10.25)
	h.run(job, 1, 1000, 30)

	evs := h.events(job)
	if len(evs) != 3 {
		t.Fatalf("%d events, want 3 (at 300, 600, 900 s after UP)", len(evs))
	}
	for i, ev := range evs {
		wantTo := h.at(10.25 + float64(300*(i+1)))
		if !ev.To.Equal(wantTo) || ev.Quantity != 300 || ev.CumulativeQuantity != int64(300*(i+1)) {
			t.Errorf("event %d = %+v, want 300 s to %v", i, ev, wantTo)
		}
	}
	ss := h.settles()
	if len(ss) != 3 {
		t.Fatalf("%d settles, want 3", len(ss))
	}
	for i, s := range ss {
		// R3: every event id the console saw is already in the ledger.
		if s.EventID != evs[i].ID || s.CumulativeQuota != evs[i].CumulativeAmount || s.Final {
			t.Errorf("settle %d = %+v, want event %s cumulative %d", i, s, evs[i].ID, evs[i].CumulativeAmount)
		}
	}
	h.must(h.eng.Stopped(h.ctx, job, 1, h.at(10.25+1000.9), EndExited))
	h.finish(job)
	h.assertSettledExactly(job, 1000) // 1000.9 s truncated
}

func TestB6_H7_SucceededFinalSettleReleasesRemainder(t *testing.T) {
	h := newHarness(t)
	job := h.placeHold(3600)
	h.start(job, 1, 0)
	h.run(job, 1, 1830, 60)
	h.must(h.eng.Stopped(h.ctx, job, 1, h.at(1830), EndExited))
	h.run(job, 1, 2000, 60) // teardown: still UP, no longer the customer's
	h.finish(job)

	h.assertSettledExactly(job, 1830)
	ss := h.settles()
	if last := ss[len(ss)-1]; !last.Final {
		t.Errorf("last settle not final: %+v", last)
	}
	ch, _ := h.fake.Hold(h.hold(job).HoldID)
	if ch.Released != quota(testRate, 3600)-quota(testRate, 1830) {
		t.Errorf("released %d, want the remainder", ch.Released)
	}
}

func TestB7_FailedAfterUpBilledToExit(t *testing.T) {
	h := newHarness(t)
	job := h.placeHold(3600)
	h.start(job, 1, 0)
	h.run(job, 1, 420, 60)
	h.must(h.eng.Stopped(h.ctx, job, 1, h.at(437.5), EndExited)) // non-zero exit
	h.finish(job)
	h.assertSettledExactly(job, 437)
}

func TestB8_FailedBeforeUpReleasedInFull(t *testing.T) {
	h := newHarness(t)
	job := h.placeHold(3600)
	h.idle(900, 60)
	h.finish(job) // capacity_exhausted
	if n := len(h.fake.CallsTo(consoletest.RouteSettle)); n != 0 {
		t.Errorf("%d settles for a job that never ran, want 0", n)
	}
	if n := len(h.fake.CallsTo(consoletest.RouteRelease)); n != 1 {
		t.Errorf("%d releases, want 1", n)
	}
	h.assertSettledExactly(job, 0)
	if h.consumeRows(job) != 0 {
		t.Error("consume-log row for a job that never ran")
	}
}

func TestB9_I2_I3_InterruptedBilledToLastObservedUp(t *testing.T) {
	h := newHarness(t)
	job := h.placeHold(3600)
	h.start(job, 1, 0)
	h.run(job, 1, 720, 60)
	// The poller notices the cluster gone at 900 s; the provider gave no
	// time, so the last observation (720 s) ends the attempt.
	h.idle(900, 60)
	h.must(h.eng.Interrupted(h.ctx, job, 1, EndPreempted, time.Time{}))
	h.finish(job)
	h.assertSettledExactly(job, 720)
}

func TestB9_I3_InterruptedAtProviderReportedTime(t *testing.T) {
	h := newHarness(t)
	job := h.placeHold(3600)
	h.start(job, 1, 0)
	h.run(job, 1, 720, 60)
	h.idle(900, 60)
	h.must(h.eng.Interrupted(h.ctx, job, 1, EndPreempted, h.at(651.4)))
	h.finish(job)
	h.assertSettledExactly(job, 651)
}

func TestB10_CancelledFromQueuedOrProvisioning(t *testing.T) {
	h := newHarness(t)
	queued := h.placeHold(3600)
	provisioning := h.placeHold(3600)
	h.idle(600, 60)
	h.finish(queued)
	h.finish(provisioning)
	for _, job := range []string{queued, provisioning} {
		ch, _ := h.fake.Hold(h.hold(job).HoldID)
		if ch.Status != consoletest.HoldReleased || ch.Settled != 0 {
			t.Errorf("hold %+v, want released with nothing settled", ch)
		}
	}
	if b := h.fake.Balance(testUser); b != testBalance {
		t.Errorf("balance %d, want fully restored", b)
	}
	if len(h.fake.ConsumeLogs()) != 0 {
		t.Error("consume-log rows for cancelled jobs that never ran")
	}
}

func TestB11_CustomerCancelBilledToAcceptance(t *testing.T) {
	h := newHarness(t)
	job := h.placeHold(3600)
	h.start(job, 1, 0)
	h.run(job, 1, 960, 60)
	h.must(h.eng.Stopped(h.ctx, job, 1, h.at(973.2), EndCancelled)) // the 202
	h.run(job, 1, 1200, 60)                                         // sky down takes four minutes
	h.finish(job)
	h.assertSettledExactly(job, 973)
}

// A 10-hour job whose balance covers only the initial hold.
func TestB12_H3_H4_ExtendRefusedCancelsBalanceExhausted(t *testing.T) {
	h := newHarness(t)
	h.setBalance(quota(testRate, 7200))
	job := h.placeHold(36000)
	h.start(job, 1, 0)
	h.run(job, 1, 7200, 60)

	hd := h.hold(job)
	if hd.CancelCode != EndBalanceExhausted {
		t.Fatalf("cancel code %q, want balance_exhausted", hd.CancelCode)
	}
	if len(h.act.stops) != 1 || h.act.stops[0].Reason != EndBalanceExhausted {
		t.Fatalf("stops = %+v, want one balance_exhausted", h.act.stops)
	}
	// H3: the first extend is at the first settlement where less than an
	// hour of rate remains over settled (3900 s); H4: the cancel comes when
	// less than ten minutes remain over accrued (just past 6600 s).
	cancelAt := h.act.stops[0].At.Sub(t0)
	if cancelAt <= 6600*time.Second || cancelAt > 6660*time.Second {
		t.Errorf("cancelled at %v, want first tick after 6600 s", cancelAt)
	}
	exts := h.extends()
	if len(exts) != 10 { // 3900, 4200, …, 6600: every settlement retries
		t.Errorf("%d extend attempts, want one per settlement from 3900 s to 6600 s (10)", len(exts))
	}
	for _, x := range exts {
		if x.ExtendID != exts[0].ExtendID || x.AdditionalQuota != quota(testRate, 3600) {
			t.Errorf("extend %+v, want the same id and quota(3600) on every retry", x)
		}
	}
	if len(h.act.notes) != 1 || h.act.notes[0] != job+":extend_refused" {
		t.Errorf("notes = %v, want one extend_refused event", h.act.notes)
	}

	// The backend tears down; GPU metering already stopped at the cancel.
	h.must(h.eng.Stopped(h.ctx, job, 1, h.at(7200), EndCancelled))
	h.finish(job)
	billed := int64(cancelAt / time.Second)
	if got := h.charged(job); got != quota(testRate, billed) || got > hd.OriginalQuota {
		t.Errorf("charged %d, want quota(%d) within the hold %d", got, billed, hd.OriginalQuota)
	}
	if ch, _ := h.fake.Hold(hd.HoldID); ch.Status != consoletest.HoldSettled {
		t.Errorf("console hold %s, want settled", ch.Status)
	}
}

func TestH3_ExtendKeepsAnHourAheadAndStopsAtCeiling(t *testing.T) {
	h := newHarness(t)
	job := h.placeHold(4 * 3600)
	h.start(job, 1, 0)
	h.run(job, 1, 4*3600, 60)

	exts := h.extends()
	if len(exts) != 2 {
		t.Fatalf("%d extends, want 2 (to 3 h at 3900 s, to the 4 h ceiling at 7500 s)", len(exts))
	}
	if exts[0].ExtendID == exts[1].ExtendID {
		t.Error("two extensions shared an extend_id")
	}
	hd := h.hold(job)
	if hd.CurrentQuota != hd.CeilingQuota {
		t.Errorf("hold %d, want the ceiling %d", hd.CurrentQuota, hd.CeilingQuota)
	}
	if hd.CancelCode != "" {
		t.Errorf("cancel code %q on a funded job", hd.CancelCode)
	}
	h.finish(job)
	h.assertSettledExactly(job, 4*3600)
}

func TestH3_ShortJobFullyHeldNeverExtends(t *testing.T) {
	h := newHarness(t)
	job := h.placeHold(1800)
	h.start(job, 1, 0)
	h.run(job, 1, 1800, 60)
	if n := len(h.extends()); n != 0 {
		t.Errorf("%d extends for a job held to its ceiling", n)
	}
	if len(h.act.stops) != 1 || h.act.stops[0].Reason != EndMaxRuntime {
		t.Errorf("stops = %+v, want max_runtime only", h.act.stops)
	}
	h.finish(job)
	h.assertSettledExactly(job, 1800)
}

func TestLostExtendResponseRetryReservesOnce(t *testing.T) {
	h := newHarness(t)
	job := h.placeHold(36000)
	h.fake.InjectFault(consoletest.Fault{Route: consoletest.RouteExtend, Status: 0, AfterApply: true})
	h.start(job, 1, 0)
	h.run(job, 1, 4000, 60)

	exts := h.extends()
	if len(exts) != 2 || exts[0] != exts[1] {
		t.Fatalf("extends = %+v, want the lost one retried with the same id and amount", exts)
	}
	ch, _ := h.fake.Hold(h.hold(job).HoldID)
	if want := quota(testRate, 7200+3600); ch.Quota != want {
		t.Errorf("console hold %d, want %d (one extension)", ch.Quota, want)
	}
	if hd := h.hold(job); hd.CurrentQuota != ch.Quota || hd.PendingExtendID != "" {
		t.Errorf("ledger hold %+v, want %d with no pending extend", hd, ch.Quota)
	}
	if b := h.fake.Balance(testUser); b != testBalance-ch.Quota {
		t.Errorf("balance %d, want %d", b, testBalance-ch.Quota)
	}
}

func TestR4_LostSettleResponseRetryChargesOnce(t *testing.T) {
	h := newHarness(t)
	job := h.placeHold(3600)
	h.fake.InjectFault(consoletest.Fault{Route: consoletest.RouteSettle, Status: 0, AfterApply: true})
	h.start(job, 1, 0)
	h.run(job, 1, 360, 60)

	ss := h.settles()
	if len(ss) != 2 || ss[0].EventID != ss[1].EventID {
		t.Fatalf("settles = %+v, want the lost one retried with the same event id", ss)
	}
	if got := h.charged(job); got != quota(testRate, 300) {
		t.Errorf("charged %d after retry, want quota(300) once", got)
	}
	if h.consumeRows(job) != 1 {
		t.Errorf("%d consume rows, want 1", h.consumeRows(job))
	}
	h.run(job, 1, 900, 60)
	h.finish(job)
	h.assertSettledExactly(job, 900)
}

func TestR4_MissedSettlesSupersededByCumulative(t *testing.T) {
	h := newHarness(t)
	job := h.placeHold(3600)
	h.start(job, 1, 0)
	h.fake.InjectFault(consoletest.Fault{Route: consoletest.RouteSettle, Times: 2, Status: 503})
	h.run(job, 1, 900, 300) // settles at 300 and 600 fail, 900 succeeds
	ss := h.settles()
	if len(ss) != 3 || ss[2].CumulativeQuota != quota(testRate, 900) {
		t.Fatalf("settles = %+v, want the third to carry quota(900)", ss)
	}
	if h.consumeRows(job) != 1 || h.charged(job) != quota(testRate, 900) {
		t.Errorf("console has %d rows / %d, want one row for quota(900)", h.consumeRows(job), h.charged(job))
	}
}

func TestR2_LostFinalSettleResponseRetried(t *testing.T) {
	h := newHarness(t)
	job := h.placeHold(3600)
	h.start(job, 1, 0)
	h.run(job, 1, 450, 60)
	h.must(h.eng.Stopped(h.ctx, job, 1, h.at(450), EndExited))
	h.fake.InjectFault(consoletest.Fault{Route: consoletest.RouteSettle, Status: 0, AfterApply: true})
	h.finish(job)
	if s := h.hold(job).State; s != "closing" {
		t.Fatalf("state %q after a lost final response, want closing", s)
	}
	h.tick()
	h.assertSettledExactly(job, 450)
	ss := h.settles()
	if a, b := ss[len(ss)-2], ss[len(ss)-1]; a != b || !b.Final {
		t.Errorf("final settle retried as %+v then %+v, want the same final event", a, b)
	}
}

func TestConsoleDownMidJobLosesNoUsage(t *testing.T) {
	h := newHarness(t)
	job := h.placeHold(36000)
	h.start(job, 1, 0)
	h.run(job, 1, 1000, 60)
	h.fake.SetDown(true)
	h.run(job, 1, 3000, 60) // 33 minutes dark: inside the hold, no cancel
	if n := len(h.events(job)); n != 10 {
		t.Errorf("%d events written while the console was down, want 10 (every 300 s to 3000 s)", n)
	}
	h.fake.SetDown(false)
	h.run(job, 1, 5000, 60)
	if hd := h.hold(job); hd.CancelCode != "" {
		t.Fatalf("job cancelled (%s) by an outage the hold covered", hd.CancelCode)
	}
	h.must(h.eng.Stopped(h.ctx, job, 1, h.at(5000), EndExited))
	h.finish(job)
	h.assertSettledExactly(job, 5000)
}

func TestB13_H5_ConsoleUnreachablePastHoldCancelsBillingUnavailable(t *testing.T) {
	h := newHarness(t)
	job := h.placeHold(36000)
	h.start(job, 1, 0)
	h.run(job, 1, 1000, 60)
	h.fake.SetDown(true)
	h.run(job, 1, 7200, 60)

	hd := h.hold(job)
	if hd.CancelCode != EndBillingUnavailable {
		t.Fatalf("cancel code %q, want billing_unavailable", hd.CancelCode)
	}
	if len(h.act.stops) != 1 || h.act.stops[0].Reason != EndBillingUnavailable {
		t.Fatalf("stops = %+v", h.act.stops)
	}
	cancelAt := h.act.stops[0].At.Sub(t0)
	if cancelAt <= 6600*time.Second || cancelAt > 6660*time.Second {
		t.Errorf("cancelled at %v, want first tick after 6600 s", cancelAt)
	}
	h.must(h.eng.Stopped(h.ctx, job, 1, h.at(7200), EndCancelled))
	h.finish(job)
	h.idle(9000, 300) // final settle queued and retried
	if s := h.hold(job).State; s != "closing" {
		t.Fatalf("state %q while the console is down, want closing", s)
	}
	h.fake.SetDown(false)
	h.tick()
	h.assertSettledExactly(job, int64(cancelAt/time.Second))
}

func TestB4_B5_I4_I5_I6_RetryAttemptsCumulativeAndCapped(t *testing.T) {
	h := newHarness(t)
	job := h.placeHold(1800)
	h.start(job, 1, 0)
	h.run(job, 1, 1000, 60)
	h.must(h.eng.Interrupted(h.ctx, job, 1, EndPreempted, h.at(1000.6)))

	before := len(h.fake.Calls())
	h.idle(4600, 60) // B4: the retry's provisioning
	for _, c := range h.fake.Calls()[before:] {
		if c.Route == consoletest.RouteExtend {
			t.Fatal("extended while provisioning a retry")
		}
	}

	h.start(job, 2, 4600) // I4: billed from UP, setup included
	h.run(job, 2, 6000, 60)
	if len(h.act.stops) != 1 || h.act.stops[0].Reason != EndMaxRuntime {
		t.Fatalf("stops = %+v, want max_runtime", h.act.stops)
	}
	if err := h.eng.Started(h.ctx, job, 3, h.at(6100)); !errors.Is(err, ErrRuntimeExhausted) {
		t.Errorf("third attempt: %v, want ErrRuntimeExhausted (I5)", err)
	}
	h.finish(job)

	var perAttempt [3]int64
	for _, ev := range h.events(job) {
		perAttempt[ev.Attempt] += ev.Quantity
	}
	if perAttempt[1] != 1000 || perAttempt[2] != 800 {
		t.Errorf("attempt seconds %v, want 1000 then 800 (capped at 1800 cumulative)", perAttempt[1:])
	}
	h.assertSettledExactly(job, 1800)
}

func TestB16_MaxRuntimeBillsExactlyCap(t *testing.T) {
	h := newHarness(t)
	job := h.placeHold(900)
	h.start(job, 1, 3.3)
	h.run(job, 1, 1200, 60)
	if len(h.act.stops) != 1 || h.act.stops[0].Reason != EndMaxRuntime {
		t.Fatalf("stops = %+v, want one max_runtime", h.act.stops)
	}
	h.finish(job)
	h.assertSettledExactly(job, 900)
	if got := h.charged(job); got != h.hold(job).CeilingQuota {
		t.Errorf("charged %d, want the ceiling quota(900) %d", got, h.hold(job).CeilingQuota)
	}
}

func TestB14_B15_NothingMeteredAfterTerminal(t *testing.T) {
	h := newHarness(t)
	job := h.placeHold(3600)
	h.start(job, 1, 0)
	h.run(job, 1, 600, 60)
	h.must(h.eng.Stopped(h.ctx, job, 1, h.at(600), EndExited))
	h.finish(job)
	calls := len(h.fake.Calls())
	events := len(h.events(job))

	// A late poll, a duplicate terminal report, a day of ticks.
	h.must(h.eng.Up(h.ctx, job, 1, h.at(700)))
	h.must(h.eng.Stopped(h.ctx, job, 1, h.at(800), EndExited))
	h.finish(job)
	h.idle(86400, 3600)
	if len(h.fake.Calls()) != calls || len(h.events(job)) != events {
		t.Error("GPU meter or hold touched after terminal state")
	}
	h.assertSettledExactly(job, 600)
}

func TestRoundingTruncatesPerAttemptAndRoundsOnce(t *testing.T) {
	h := newHarness(t)
	job := h.placeHold(3600)
	h.start(job, 1, 0)
	h.must(h.eng.Stopped(h.ctx, job, 1, h.at(1.9), EndPreempted))
	h.start(job, 2, 100)
	h.must(h.eng.Stopped(h.ctx, job, 2, h.at(101.9), EndExited))
	h.finish(job)
	// floor(1.9) + floor(1.9) = 2 s, and quota(2) = 333, not 2 × quota(1) = 334.
	h.assertSettledExactly(job, 2)
	if got := h.charged(job); got != 333 {
		t.Errorf("charged %d, want 333", got)
	}
}

func TestR6_HoldExpiredRecordsUnbilled(t *testing.T) {
	h := newHarness(t)
	job := h.placeHold(600)
	h.start(job, 1, 0)
	h.run(job, 1, 300, 60)
	// The exit is reported long after the hold's expiry (600 s + 48 h), so
	// the final settle meets a swept hold.
	h.clock.Set(h.at(600 + 49*3600))
	h.must(h.eng.Stopped(h.ctx, job, 1, h.at(400), EndExited))
	h.finish(job)
	if len(h.act.notes) != 1 || h.act.notes[0] != job+":hold_expired" {
		t.Errorf("notes = %v, want hold_expired", h.act.notes)
	}
	hd := h.hold(job)
	if want := quota(testRate, 400) - quota(testRate, 300); hd.State != "closed" || hd.UnbilledQuota != want {
		t.Errorf("hold %+v, want closed with %d unbilled", hd, want)
	}
	if got := h.charged(job); got != quota(testRate, 300) {
		t.Errorf("charged %d, want only what settled before expiry", got)
	}
	n := len(h.fake.Calls())
	h.tick()
	if len(h.fake.Calls()) != n {
		t.Error("closed hold still retried against the console")
	}
}

func TestLifecycleReportsAreIdempotent(t *testing.T) {
	h := newHarness(t)
	job := h.placeHold(3600)
	h.start(job, 1, 0)
	h.start(job, 1, 0)
	h.must(h.eng.Started(h.ctx, job, 1, h.at(50))) // a later poll re-reporting UP
	h.run(job, 1, 600, 60)
	h.must(h.eng.Up(h.ctx, job, 1, h.at(300))) // out of order
	h.must(h.eng.Stopped(h.ctx, job, 1, h.at(600), EndExited))
	h.must(h.eng.Stopped(h.ctx, job, 1, h.at(650), EndExited))
	h.finish(job)
	h.finish(job)
	h.assertSettledExactly(job, 600)
}

func TestStopRequestRetriedUntilBackendAccepts(t *testing.T) {
	h := newHarness(t)
	job := h.placeHold(300)
	h.start(job, 1, 0)
	h.act.failing = true
	if err := h.eng.Up(h.ctx, job, 1, h.at(360)); err != nil {
		t.Fatal(err)
	}
	h.clock.Set(h.at(360))
	if err := h.eng.Tick(h.ctx); err == nil {
		t.Error("tick hid a failed stop request")
	}
	h.act.failing = false
	h.tick()
	h.tick()
	if len(h.act.stops) != 1 || h.act.stops[0].Reason != EndMaxRuntime {
		t.Errorf("stops = %+v, want one max_runtime after the retry", h.act.stops)
	}
}

func TestUsageEventsAreAppendOnly(t *testing.T) {
	h := newHarness(t)
	job := h.placeHold(3600)
	h.start(job, 1, 0)
	h.run(job, 1, 300, 60)
	for _, sql := range []string{
		`UPDATE usage_events SET quantity = 0`,
		`DELETE FROM usage_events`,
		`TRUNCATE usage_events`,
	} {
		if _, err := h.db.Exec(h.ctx, sql); err == nil {
			t.Errorf("%s succeeded on the append-only ledger", sql)
		}
	}
	if len(h.events(job)) != 1 {
		t.Error("ledger changed")
	}
}

func TestEventIDsAreGloballyUnique(t *testing.T) {
	h := newHarness(t)
	a, b := h.placeHold(3600), h.placeHold(3600)
	h.start(a, 1, 0)
	h.must(h.eng.Started(h.ctx, b, 1, h.at(0)))
	h.run(a, 1, 900, 300)
	seen := map[string]bool{}
	for _, job := range []string{a, b} {
		for _, ev := range h.events(job) {
			if seen[ev.ID] {
				t.Fatalf("event id %s reused", ev.ID)
			}
			seen[ev.ID] = true
		}
	}
}
