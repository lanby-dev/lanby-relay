package relay

// healthTracker remembers the last probe outcome per monitor and decides when the
// relay is in "high-touch" mode: at least one monitor is unhealthy, so results are
// synced immediately instead of waiting for the next poll.
type healthTracker struct {
	lastOutcome map[string]string
	highTouch   bool
}

func newHealthTracker() *healthTracker {
	return &healthTracker{lastOutcome: map[string]string{}}
}

// record folds a batch of probe results into the tracker. cur is the current monitor
// set; monitors outside allow never run, so they are ignored when deciding whether
// everything is healthy. It reports whether high-touch mode was entered or exited by
// this batch.
func (h *healthTracker) record(batch []ResultItem, cur []RelayCheckConfig, allow AllowList) (entered, exited bool) {
	prev := h.highTouch
	for _, res := range batch {
		h.lastOutcome[res.MonitorID] = res.Status
		if !probeSuccess(res.Status) {
			h.highTouch = true
		}
	}
	current := make(map[string]struct{}, len(cur))
	for _, c := range cur {
		current[c.MonitorID] = struct{}{}
	}
	for id := range h.lastOutcome {
		if _, ok := current[id]; !ok {
			delete(h.lastOutcome, id)
		}
	}
	if h.highTouch {
		if len(cur) == 0 {
			h.highTouch = false
		} else {
			allGreen := true
			for _, c := range cur {
				if !allow.Allowed(c.Target) {
					continue
				}
				if st, ok := h.lastOutcome[c.MonitorID]; !ok || !probeSuccess(st) {
					allGreen = false
					break
				}
			}
			if allGreen {
				h.highTouch = false
			}
		}
	}
	return !prev && h.highTouch, prev && !h.highTouch
}
