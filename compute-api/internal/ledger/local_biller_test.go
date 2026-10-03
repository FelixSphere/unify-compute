package ledger

import (
	"testing"

	"github.com/FelixSphere/unify-compute/compute-api/internal/billing/billingtest"
)

// localBillerParity is every harness test, run a second time with the
// console fake's balances and holds kept by the local biller (task A6,
// ADR-0011). The bodies and their expectations are the console tests',
// unchanged: newHarness sees the TestLocalBiller/ prefix and puts the local
// biller behind the fake (consoletest.Server.UseBook), so the engine, the
// fault injection and every assertion are the same and only the book under
// them differs. After each one the balance invariant is checked as well.
var localBillerParity = []struct {
	name string
	fn   func(*testing.T)
}{
	{"TestB1_QueuedHoldPlacedNotCharged", TestB1_QueuedHoldPlacedNotCharged},
	{"TestH1_InitialHoldIsCeilingUpToTwoHours", TestH1_InitialHoldIsCeilingUpToTwoHours},
	{"TestH6_HoldExpiresAfterRuntimePlus48h", TestH6_HoldExpiresAfterRuntimePlus48h},
	{"TestH2_RefusedHoldCarriesHoldAndCeiling", TestH2_RefusedHoldCarriesHoldAndCeiling},
	{"TestPlaceHoldLostResponseReservesOnce", TestPlaceHoldLostResponseReservesOnce},
	{"TestB2_I1_ProvisioningNeverBilled", TestB2_I1_ProvisioningNeverBilled},
	{"TestB3_R1_R3_RunningSettledEveryFiveMinutesLedgerFirst", TestB3_R1_R3_RunningSettledEveryFiveMinutesLedgerFirst},
	{"TestB6_H7_SucceededFinalSettleReleasesRemainder", TestB6_H7_SucceededFinalSettleReleasesRemainder},
	{"TestB7_FailedAfterUpBilledToExit", TestB7_FailedAfterUpBilledToExit},
	{"TestB8_FailedBeforeUpReleasedInFull", TestB8_FailedBeforeUpReleasedInFull},
	{"TestB9_I2_I3_InterruptedBilledToLastObservedUp", TestB9_I2_I3_InterruptedBilledToLastObservedUp},
	{"TestB9_I3_InterruptedAtProviderReportedTime", TestB9_I3_InterruptedAtProviderReportedTime},
	{"TestB10_CancelledFromQueuedOrProvisioning", TestB10_CancelledFromQueuedOrProvisioning},
	{"TestB11_CustomerCancelBilledToAcceptance", TestB11_CustomerCancelBilledToAcceptance},
	{"TestB12_H3_H4_ExtendRefusedCancelsBalanceExhausted", TestB12_H3_H4_ExtendRefusedCancelsBalanceExhausted},
	{"TestH3_ExtendKeepsAnHourAheadAndStopsAtCeiling", TestH3_ExtendKeepsAnHourAheadAndStopsAtCeiling},
	{"TestH3_ShortJobFullyHeldNeverExtends", TestH3_ShortJobFullyHeldNeverExtends},
	{"TestLostExtendResponseRetryReservesOnce", TestLostExtendResponseRetryReservesOnce},
	{"TestR4_LostSettleResponseRetryChargesOnce", TestR4_LostSettleResponseRetryChargesOnce},
	{"TestR4_MissedSettlesSupersededByCumulative", TestR4_MissedSettlesSupersededByCumulative},
	{"TestR2_LostFinalSettleResponseRetried", TestR2_LostFinalSettleResponseRetried},
	{"TestConsoleDownMidJobLosesNoUsage", TestConsoleDownMidJobLosesNoUsage},
	{"TestB13_H5_ConsoleUnreachablePastHoldCancelsBillingUnavailable", TestB13_H5_ConsoleUnreachablePastHoldCancelsBillingUnavailable},
	{"TestB4_B5_I4_I5_I6_RetryAttemptsCumulativeAndCapped", TestB4_B5_I4_I5_I6_RetryAttemptsCumulativeAndCapped},
	{"TestB16_MaxRuntimeBillsExactlyCap", TestB16_MaxRuntimeBillsExactlyCap},
	{"TestB14_B15_NothingMeteredAfterTerminal", TestB14_B15_NothingMeteredAfterTerminal},
	{"TestRoundingTruncatesPerAttemptAndRoundsOnce", TestRoundingTruncatesPerAttemptAndRoundsOnce},
	{"TestR6_HoldExpiredRecordsUnbilled", TestR6_HoldExpiredRecordsUnbilled},
	{"TestLifecycleReportsAreIdempotent", TestLifecycleReportsAreIdempotent},
	{"TestStopRequestRetriedUntilBackendAccepts", TestStopRequestRetriedUntilBackendAccepts},
	{"TestUsageEventsAreAppendOnly", TestUsageEventsAreAppendOnly},
	{"TestEventIDsAreGloballyUnique", TestEventIDsAreGloballyUnique},
}

func TestLocalBiller(t *testing.T) {
	for _, tc := range localBillerParity {
		t.Run(tc.name, tc.fn)
	}
}

// A harness test added later must join the parity run, or the local biller
// would silently stop being checked against it.
func TestLocalBillerParityListIsComplete(t *testing.T) {
	listed := map[string]bool{}
	for _, tc := range localBillerParity {
		listed[tc.name] = true
	}
	for _, name := range billingtest.TestsCalling(t, ".", "newHarness") {
		if !listed[name] {
			t.Errorf("%s uses newHarness but is not in localBillerParity", name)
		}
	}
}
