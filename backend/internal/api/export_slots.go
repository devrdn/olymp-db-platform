package api

import (
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/devrdn/db-contest/backend/internal/platform/httpx"
)

// The one bound every CSV download shares: how many may hold a core-database
// connection at once.
//
// A streamed export holds its connection for as long as the client reads
// (postgres.QueryLog.ExportHistory, monitor.StreamFeed); exportDeadline caps
// each at a minute. The other bounds are per caller, but the pool has 25
// connections (CORE_DB_POOL_MAX), and 25 participants each starting one slow
// download would hold all of them, queueing every sign-in and answer behind
// them.
//
// So this is an unkeyed count that every export route takes a slot from.
// In-process only: each replica keeps its own count, a weaker bound but never a
// broken one.

// DefaultExportConcurrency is how many exports may hold a connection at once
// when EXPORT_CONCURRENCY is unset. Five of a 25-connection pool leaves twenty
// for ordinary requests, which hold theirs for milliseconds (at most
// storage.coreStatementTimeout), far more than three hundred participants need.
// A sixth download is told to come back in a moment instead of slowing everyone
// else.
const DefaultExportConcurrency = 5

// ErrExportsBusy reports that every export slot is taken. It is about load, not
// the caller: nothing was read or recorded, and asking again shortly is right
// (see exportsBusy).
var ErrExportsBusy = errors.New("as many exports are running as this installation allows")

// ExportSlots counts the exports holding a database connection. A count, not a
// semaphore to wait on: a slot can be held for a minute, and parking a request
// that long is worse than refusing it now.
type ExportSlots struct {
	mu   sync.Mutex
	max  int
	open int
}

// NewExportSlots allows max exports at once; max <= 0 means
// DefaultExportConcurrency.
func NewExportSlots(max int) *ExportSlots {
	if max <= 0 {
		max = DefaultExportConcurrency
	}
	return &ExportSlots{max: max}
}

// enter claims one slot and returns its release, or ErrExportsBusy when all are
// taken. release is never nil and safe to call more than once: an export can
// end on several paths, and returning a slot twice would hand out an extra one.
func (s *ExportSlots) enter() (release func(), err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.open >= s.max {
		return func() {}, ErrExportsBusy
	}
	s.open++

	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			s.open--
		})
	}, nil
}

// exportsBusy answers a download that found every slot taken. 503, not 429, as
// for sign-in's hashing slots: the caller is within every budget, and it is the
// process that is at capacity. Retry-After is exportDeadline, the longest a
// slot can be held. The refusal still cost the caller one read of their budget
// (CLAUDE.md rule 13).
func exportsBusy(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Retry-After", strconv.Itoa(int(exportDeadline/time.Second)))
	httpx.Error(w, r, http.StatusServiceUnavailable, codeTooManyExports,
		"The service is writing as many downloads at once as it will; try again in a moment")
}
