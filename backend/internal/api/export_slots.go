package api

import (
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/devrdn/db-contest/backend/internal/platform/httpx"
)

// The one bound every CSV download in this service shares: how many of them
// may hold a core-database connection at the same moment.
//
// A streamed export is the only read this API serves that holds its
// connection for as long as the client cares to take — the rows come out of a
// transaction with an open cursor, and the slow party is the reader, not the
// query (postgres.QueryLog.ExportHistory, monitor.StreamFeed). exportDeadline
// caps that at a minute apiece; nothing capped how many minutes were being
// held at once.
//
// The bounds already in place are each about one caller. ExportGate is one
// download per registration, and the organiser's is one per account; the read
// budgets are per account per minute. None of them is a statement about the
// service: the pool has 25 connections (CORE_DB_POOL_MAX, deploy/docker-compose.yml),
// and 25 participants each starting one slow download own every one of them —
// every sign-in, every admission check, every answer and the leaderboard queue
// behind those downloads until they finish or time out. A contest of three
// hundred has that many participants to spare.
//
// So this is a count, with no key at all, and every export route takes one
// slot from it: the participant's own log from the play screen and from their
// profile afterwards, and the organiser's participant and contest feeds. In
// this process only, which is what the deployment is (docs/ARCHITECTURE.md §2.3);
// a second replica would get its own count, which is a weaker bound and never
// a broken one.

// DefaultExportConcurrency is how many exports may hold a connection at once
// when the deployment does not say (EXPORT_CONCURRENCY,
// config.Config.ExportConcurrency).
//
// Five, against a core pool of 25 (storage.defaultMaxConns, and what
// deploy/docker-compose.yml sets CORE_DB_POOL_MAX to). It is one fifth of the
// pool, so twenty connections are left for the traffic that actually runs a
// contest, and the arithmetic is on that twenty rather than on the five: an
// ordinary request holds its connection for the milliseconds its query takes,
// capped at ten seconds by storage.coreStatementTimeout, so twenty
// connections serve a rate far above what three hundred participants can ask
// for between them — while the five are allowed to hold theirs for the whole
// of exportDeadline without any of that queueing behind them.
//
// Read the other way it is a promise to whoever is downloading: five people
// may take a file at the same time, and a sixth is told to come back in a
// moment rather than being served slowly at everybody else's expense. That is
// the right trade for a file an organiser or a participant asks for by hand,
// a few times over a contest.
const DefaultExportConcurrency = 5

// ErrExportsBusy reports that every export slot is taken. It is a statement
// about load, not about the caller: nothing was read, nothing was recorded,
// and asking again in a moment is the right response (CLAUDE.md rule 1 — the
// handlers' fail switches name it, see exportsBusy).
var ErrExportsBusy = errors.New("as many exports are running as this installation allows")

// ExportSlots counts the exports holding a database connection.
//
// A plain count rather than a semaphore anything waits on: a slot can be held
// for exportDeadline, so waiting for one means parking a request — and its
// goroutine, its socket and its share of a graceful shutdown — for up to a
// minute to be told what it could have been told immediately. A download is
// something a person asks for by hand and can ask for again.
type ExportSlots struct {
	mu   sync.Mutex
	max  int
	open int
}

// NewExportSlots returns the count, allowing max exports at once.
// max <= 0 takes DefaultExportConcurrency, which is what a deployment that
// sets nothing gets.
func NewExportSlots(max int) *ExportSlots {
	if max <= 0 {
		max = DefaultExportConcurrency
	}
	return &ExportSlots{max: max}
}

// enter claims one slot and returns the release for it, or ErrExportsBusy
// when every slot is taken.
//
// release is never nil and is safe to call more than once, because the paths
// that call it are the paths an export can end on — the file written, the
// read failed, the deadline reached, the client gone mid-stream — and one of
// them returning a slot twice would hand out a sixth.
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

// exportsBusy answers a download that found every slot taken.
//
// 503 rather than 429, for the reason sign-in answers 503 when every hashing
// slot is taken (auth_handler.go's busy): the caller asked for one download
// and is inside every budget they have — it is the process that is at
// capacity, not the caller that is being limited. Retry-After is
// exportDeadline, the longest a slot can be held, so a client that waits it
// out is certain to find one free rather than guessing.
//
// The refusal is not free to the caller: every route that reaches it has
// already spent one read of its own per-minute budget, in the middleware
// above it, refusal included (CLAUDE.md rule 13). What it costs is the
// budget, not a connection.
func exportsBusy(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Retry-After", strconv.Itoa(int(exportDeadline/time.Second)))
	httpx.Error(w, r, http.StatusServiceUnavailable, codeTooManyExports,
		"The service is writing as many downloads at once as it will; try again in a moment")
}
