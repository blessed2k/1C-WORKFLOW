package source

import (
	"sync/atomic"
	"time"
)

// maxQueryTimeout keeps the limit sent to the connector below the HTTP timeout of
// the client (300 s): the connector has to answer "interrupted" itself, otherwise
// the client just stops waiting and the query keeps running in the base.
const maxQueryTimeout = 240 * time.Second

// liveQueryTimeout is process-wide, like the export cache tunables: every
// HTTPSource of the process reads it when it posts a query.
var liveQueryTimeout atomic.Int64

// ConfigureLive sets the tunables of the live mode. queryTimeout is how long the
// connector lets a query run before interrupting it in the base; a non-positive
// value sends no limit, and the connector runs the query as it did before.
func ConfigureLive(queryTimeout time.Duration) {
	if queryTimeout < 0 {
		queryTimeout = 0
	}
	liveQueryTimeout.Store(int64(queryTimeout))
}

// LiveQueryTimeout reports the configured limit (zero: none).
func LiveQueryTimeout() time.Duration {
	return time.Duration(liveQueryTimeout.Load())
}

// queryTimeoutSeconds is the value for the body of /query: whole seconds, rounded
// up so that a sub-second limit does not turn into "no limit", and clamped.
func queryTimeoutSeconds() int {
	limit := LiveQueryTimeout()
	if limit <= 0 {
		return 0
	}
	if limit > maxQueryTimeout {
		limit = maxQueryTimeout
	}
	return int((limit + time.Second - 1) / time.Second)
}
