package bdtieba

import (
	"log"
	"sync"
	"time"
)

const (
	// rateBase is the first cooldown after a bfe 403. Measured recovery is
	// ~1s spacing, so 2s is ample for the 60s+ poll cadence.
	rateBase = 2 * time.Second
	rateMult = 2
	rateMax  = 10 * time.Second // 2 -> 4 -> 8 -> 10 cap
)

var (
	rateMu      sync.Mutex
	rateUntil   time.Time
	rateBackoff time.Duration
)

// waitRateGate blocks until the rate-limit cooldown expires (blocking mode).
func waitRateGate() {
	rateMu.Lock()
	for {
		if rateUntil.IsZero() || !rateUntil.After(time.Now()) {
			break
		}
		remain := time.Until(rateUntil)
		rateMu.Unlock()
		time.Sleep(remain)
		rateMu.Lock()
	}
	rateMu.Unlock()
}

// noteRateLimit doubles the cooldown (capped at rateMax) on a bfe 403.
func noteRateLimit() {
	rateMu.Lock()
	defer rateMu.Unlock()
	if rateBackoff == 0 {
		rateBackoff = rateBase
	} else {
		rateBackoff *= rateMult
		if rateBackoff > rateMax {
			rateBackoff = rateMax
		}
	}
	rateUntil = time.Now().Add(rateBackoff)
	log.Printf("bdtieba: bfe 403 rate limit, throttling %s", rateBackoff)
}

// clearRateLimit resets the cooldown to base on a 200 success. It only resets
// when no cooldown is active: gate blocking guarantees a sequential caller has
// already waited out the window, and this guard keeps a concurrent 200 (from a
// request started before a 403 landed) from cancelling an in-flight backoff.
func clearRateLimit() {
	rateMu.Lock()
	defer rateMu.Unlock()
	if rateUntil.After(time.Now()) {
		return
	}
	rateUntil = time.Time{}
	rateBackoff = rateBase
}
