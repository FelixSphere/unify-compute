package jobs_test

import (
	"testing"

	"github.com/FelixSphere/unify-compute/compute-api/internal/billing/billingtest"
)

// localBillerParity is every test built on newWorld (the restart tests and
// the platform limit), run a second time with the console fake's balances
// and holds kept by the local biller (task A6, ADR-0011). The bodies and
// newWorld are unchanged: while this test runs every console fake uses a
// local biller (billingtest.UseLocalBooks), so restarts, lost responses and
// the final checks run against it, followed by the balance invariant.
var localBillerParity = []struct {
	name string
	fn   func(*testing.T)
}{
	{"TestRestartMidRunResumesAndBillsOnce", TestRestartMidRunResumesAndBillsOnce},
	{"TestRestartFinalizesJobsThatEndedDuringDowntime", TestRestartFinalizesJobsThatEndedDuringDowntime},
	{"TestRestartWithVanishedClusterInterruptsAtLastUp", TestRestartWithVanishedClusterInterruptsAtLastUp},
	{"TestRestartRejectsClusterRelaunchedUnderTheSameName", TestRestartRejectsClusterRelaunchedUnderTheSameName},
	{"TestRestartLaunchesQueuedJob", TestRestartLaunchesQueuedJob},
	{"TestRestartBoundsLateLaunchesByTheHold", TestRestartBoundsLateLaunchesByTheHold},
	{"TestRestartCatchesTheHoldUpAndReplaysTheExtendOnce", TestRestartCatchesTheHoldUpAndReplaysTheExtendOnce},
	{"TestRestartReplaysALostFinalSettleOnce", TestRestartReplaysALostFinalSettleOnce},
	{"TestRestartCapsADowntimeTheHoldCannotCover", TestRestartCapsADowntimeTheHoldCannotCover},
	{"TestTwoProcessesOnlyTheLockHolderDrives", TestTwoProcessesOnlyTheLockHolderDrives},
	{"TestSubmitHonoursThePlatformWideLimit", TestSubmitHonoursThePlatformWideLimit},
}

func TestLocalBiller(t *testing.T) {
	billingtest.UseLocalBooks(t)
	for _, tc := range localBillerParity {
		t.Run(tc.name, tc.fn)
	}
}

func TestLocalBillerParityListIsComplete(t *testing.T) {
	listed := map[string]bool{}
	for _, tc := range localBillerParity {
		listed[tc.name] = true
	}
	for _, name := range billingtest.TestsCalling(t, ".", "newWorld") {
		if !listed[name] {
			t.Errorf("%s uses newWorld but is not in localBillerParity", name)
		}
	}
}
