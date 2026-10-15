package runstate

import "testing"

func TestAllSeventeenStates(t *testing.T) {
	if len(All) != 17 {
		t.Fatalf("All has %d states, want 17", len(All))
	}
	for _, s := range All {
		if !Valid(s) {
			t.Fatalf("state %s in All but Valid()=false", s)
		}
	}
}

func TestInvalidStateRejected(t *testing.T) {
	for _, bad := range []string{"", "QUEUED", "WAITING_RECONCILIATION", "UNKNOWN_OUTCOME", "running"} {
		if Valid(bad) {
			t.Fatalf("Valid(%q) = true, want false (历史词表/大小写不合法)", bad)
		}
	}
}

func TestSchedulingClass(t *testing.T) {
	cases := map[string]SchedulingClass{
		Recovering:             ClassRecovery,
		RequiresReconciliation: ClassReconciliation,
		Cancelling:             ClassCancelling,
		Ready:                  ClassNormal,
		Running:                ClassNormal,
		Completed:              "",
		Failed:                 "",
		Cancelled:              "",
		Expired:                "",
		ManualReview:           "",
		WaitingApproval:        "",
	}
	for state, want := range cases {
		if got := SchedulingClassOf(state); got != want {
			t.Fatalf("SchedulingClassOf(%s) = %q, want %q", state, got, want)
		}
	}
}

func TestTerminalAndWaiting(t *testing.T) {
	for _, s := range []string{Completed, Failed, Cancelled, Expired} {
		if !IsTerminal(s) {
			t.Fatalf("IsTerminal(%s) = false", s)
		}
	}
	for _, s := range []string{WaitingInput, WaitingApproval} {
		if !IsWaiting(s) {
			t.Fatalf("IsWaiting(%s) = false", s)
		}
		if IsTerminal(s) {
			t.Fatalf("waiting state %s must not be terminal", s)
		}
	}
}
