package scaletest

import (
	"runtime"
	"time"
)

// MemStats summarizes heap allocation (runtime.MemStats.HeapAlloc) sampled
// at intervals over the course of a run, in bytes.
type MemStats struct {
	Start, End uint64
	Min, Max   uint64
}

// TrackMemory starts a background goroutine that samples the process's heap
// allocation every interval. Call the returned stop func to end sampling; it
// blocks until the goroutine exits and returns the observed MemStats,
// including one final sample taken at stop time.
func TrackMemory(interval time.Duration) (stop func() MemStats) {
	quit := make(chan struct{})
	done := make(chan MemStats)

	go func() {
		sample := func() uint64 {
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			return m.HeapAlloc
		}

		start := sample()
		stats := MemStats{Start: start, Min: start, Max: start}
		update := func(v uint64) {
			if v < stats.Min {
				stats.Min = v
			}
			if v > stats.Max {
				stats.Max = v
			}
		}

		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				update(sample())
			case <-quit:
				end := sample()
				update(end)
				stats.End = end
				done <- stats
				return
			}
		}
	}()

	return func() MemStats {
		close(quit)
		return <-done
	}
}
