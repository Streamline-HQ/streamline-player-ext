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
// to finish: one job every span/(finished-1), span running from the oldest of
// those finishes to the newest. The time since the newest comes off, up to
// one job's worth, so the figure counts down between two finishes and then
// holds until the next. It is 0 with fewer than two finishes to go by,
// rounded up to whole seconds otherwise. The pace since the scan began would
// not do: a scan starts on the quick jobs, and every minute spent on a slow
// one would then add to the time left.
func ETASeconds(waiting, finished int64, span, sinceLast time.Duration) int64 {
	if waiting <= 0 || finished < 2 || span <= 0 {
		return 0
	}
	each := span.Seconds() / float64(finished-1)
	spent := math.Max(0, math.Min(sinceLast.Seconds(), each))
	return int64(math.Ceil(each*float64(waiting) - spent))
}
