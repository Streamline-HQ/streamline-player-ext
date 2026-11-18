package scan

import (
	"math"
	"time"
)

// Progress is how far a folder's scan is, from 0 to 1: the share of the
// engine's jobs no longer waiting. The comparison takes minutes where the
// jobs take hours, so it only scales that share down while rows are still
// being compared and more jobs may come. A scan that queued no job is as far
// as its comparison.
func Progress(compared, rows, waiting, jobs int64) float64 {
	share := func(done, of int64) float64 {
		if of <= 0 {
			return 1
		}
		return float64(done) / float64(of)
	}
	return share(compared, rows) * share(jobs-waiting, jobs)
}

// ETASeconds is the time the waiting jobs take at the pace of the last ones
// to finish: finished of them over span. It is 0 while nothing finished yet,
// rounded up to whole seconds otherwise. The pace since the scan began would
// not do: a scan starts on the quick jobs, and every minute spent on a slow
// one would then add to the time left.
func ETASeconds(waiting, finished int64, span time.Duration) int64 {
	if waiting <= 0 || finished <= 0 || span <= 0 {
		return 0
	}
	return int64(math.Ceil(span.Seconds() * float64(waiting) / float64(finished)))
}
