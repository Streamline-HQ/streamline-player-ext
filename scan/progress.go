package scan

import (
	"math"
	"time"
)

// Measurable is the least progress an ETA is worked out from. The view shows
// progress in thousandths, and below one there is no rate to go by.
const Measurable = 0.001

// Progress is how far a folder's scan is, from 0 to 1. The comparison counts
// for half, by the share of the folder's rows behind the cursor, and the
// engine's jobs for the other half, by the share no longer waiting. A half
// with nothing to count is done.
func Progress(compared, rows, waiting, jobs int64) float64 {
	share := func(done, of int64) float64 {
		if of <= 0 {
			return 1
		}
		return float64(done) / float64(of)
	}
	return (share(compared, rows) + share(jobs-waiting, jobs)) / 2
}

// ETASeconds is the time left at the rate progress was made over elapsed,
// rounded up to whole seconds. Progress below Measurable gives 0.
func ETASeconds(elapsed time.Duration, progress float64) int64 {
	if progress < Measurable || progress >= 1 || elapsed <= 0 {
		return 0
	}
	return int64(math.Ceil(elapsed.Seconds() * (1 - progress) / progress))
}
