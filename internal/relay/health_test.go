package relay

import "testing"

func okResult(id string) ResultItem   { return ResultItem{MonitorID: id, Status: "ok"} }
func failResult(id string) ResultItem { return ResultItem{MonitorID: id, Status: "error"} }

func checkFor(id, target string) RelayCheckConfig {
	return RelayCheckConfig{MonitorID: id, Target: target}
}

func TestHealthTracker_FailureEntersAndRecoveryExitsHighTouch(t *testing.T) {
	h := newHealthTracker()
	cur := []RelayCheckConfig{checkFor("m1", "a.local")}

	entered, exited := h.record([]ResultItem{failResult("m1")}, cur, AllowList{})
	if !entered || exited || !h.highTouch {
		t.Fatalf("failure: want entered, got entered=%v exited=%v highTouch=%v", entered, exited, h.highTouch)
	}
	entered, exited = h.record([]ResultItem{okResult("m1")}, cur, AllowList{})
	if entered || !exited || h.highTouch {
		t.Fatalf("recovery: want exited, got entered=%v exited=%v highTouch=%v", entered, exited, h.highTouch)
	}
}

func TestHealthTracker_MonitorThatHasNotReportedYetHoldsHighTouch(t *testing.T) {
	h := newHealthTracker()
	cur := []RelayCheckConfig{checkFor("m1", "a.local"), checkFor("m2", "b.local")}

	h.record([]ResultItem{failResult("m1")}, cur, AllowList{})
	h.record([]ResultItem{okResult("m1")}, cur, AllowList{}) // m2 has not run yet
	if !h.highTouch {
		t.Fatal("high-touch must hold until every runnable monitor has reported healthy")
	}
	h.record([]ResultItem{okResult("m2")}, cur, AllowList{})
	if h.highTouch {
		t.Fatal("high-touch should end once all monitors are healthy")
	}
}

// A monitor blocked by ALLOWED_PROBE_HOSTS never runs, so it never reports an
// outcome. It must not keep the relay in high-touch mode forever.
func TestHealthTracker_AllowListBlockedMonitorDoesNotHoldHighTouch(t *testing.T) {
	allow := mustAllowList(t, "a.local")
	h := newHealthTracker()
	cur := []RelayCheckConfig{checkFor("m1", "a.local"), checkFor("blocked", "other.local")}

	h.record([]ResultItem{failResult("m1")}, cur, allow)
	if !h.highTouch {
		t.Fatal("failure should enter high-touch")
	}
	h.record([]ResultItem{okResult("m1")}, cur, allow)
	if h.highTouch {
		t.Fatal("high-touch stuck on: only a blocked monitor (which never runs) is without a healthy outcome")
	}
}

func TestHealthTracker_PrunesOutcomesOfRemovedMonitors(t *testing.T) {
	h := newHealthTracker()
	h.record([]ResultItem{okResult("m1"), okResult("m2")}, []RelayCheckConfig{checkFor("m1", "a"), checkFor("m2", "b")}, AllowList{})
	h.record([]ResultItem{okResult("m1")}, []RelayCheckConfig{checkFor("m1", "a")}, AllowList{}) // m2 removed from config

	if _, ok := h.lastOutcome["m2"]; ok {
		t.Fatalf("outcome for removed monitor still tracked: %v", h.lastOutcome)
	}
}
