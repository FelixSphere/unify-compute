package jobs_test

import (
	"testing"

	"github.com/FelixSphere/unify-compute/compute-api/internal/billing/billingtest"
)

// localBillerParity is every test built on newWorld (the restart tests and
// the platform limit), run a second time with the console fake's balances
// and holds kept by the local biller (task A6, ADR-0011). The bodies are
// unchanged: newWorld sees the TestLocalBiller/ prefix and puts the local
// biller behind the fake, so restarts, lost responses and the final checks
// run against it, followed by the balance invariant.
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
