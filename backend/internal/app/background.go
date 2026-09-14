package app

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/devrdn/db-contest/backend/internal/platform/metrics"
	"github.com/devrdn/db-contest/backend/internal/provisioning"
)

// A periodic job, and the small amount of machinery the service needs to run
// one. Deliberately small: there is one job today, and a scheduler with
// retries, backoff and leader election would be more moving parts than the
// thing it schedules.
type task struct {
	name  string
	every time.Duration
	// atStart runs the job once as the process comes up, before waiting out
	// the first interval.
	//
	// Per job rather than for all of them, because "what has this process
	// missed while it was not running" has a different answer for each. A job
	// whose work somebody is waiting on wants it: an API restarted five
	// minutes before a contest opens must not leave the pool untended until
	// five minutes *after* it opens, because what is on the other end of that
	// is every participant waiting for CREATE DATABASE inside their own page
	// load. A job whose work is bounded by a grace period measured in
	// minutes-to-days does not: nothing is waiting on it, and the same
	// decisions are taken correctly one interval later. See each job below
	// for its own answer.
	atStart bool
	// wake, when set, is a second way to run this job right now, besides its
	// own interval: a send on it (from outside the loop, see
	// provisioning.Tender) runs the job immediately, in addition to whatever
	// tick comes next. nil for every job except the pool tender — a nil
	// channel is never selected, so runPeriodically needs no branch for a
	// job that never sets this.
	wake <-chan struct{}
	run  func(context.Context) error
}

// runPeriodically runs the task until the context is cancelled.
//
// A failure is logged and the next tick still happens. These jobs are
// housekeeping: one that cannot run because the database is briefly away
// should try again in a minute, not take the service down with it — including
// the run at startup, which is one more run of the same job and not a
// condition for coming up.
func runPeriodically(ctx context.Context, log *slog.Logger, t task) {
	ticker := time.NewTicker(t.every)
	defer ticker.Stop()

	// The ticker is started first so a slow first run does not push the
	// second one a whole interval further out than it was configured for.
	if t.atStart {
		// Still asked, because a process cancelled while it was starting
		// should stop rather than do one last piece of housekeeping.
		if ctx.Err() == nil {
			runOnce(ctx, log, t)
		}
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runOnce(ctx, log, t)
		case <-t.wake:
			// Somebody asked for this sooner than the interval — a
			// registration or a scheduler transition, for the one job that
			// sets this (tendPools). Runs the same body a tick would.
			runOnce(ctx, log, t)
		}
	}
}

// runOnce is one run of a job, with its failure logged rather than returned:
// there is nobody above this to hand it to.
func runOnce(ctx context.Context, log *slog.Logger, t task) {
	if err := t.run(ctx); err != nil {
		log.ErrorContext(ctx, "a background job failed", "job", t.name, "error", err)
	}
}

// provisionStatementTimeout bounds one statement on the game cluster's
// provisioning pool. Minutes rather than the core API's seconds, because
// copying a template is disk work whose duration is the template's size; the
// bound exists so a hung cluster is still noticed, not to shape normal work.
const provisionStatementTimeout = 10 * time.Minute

// abandonedAfter is how long a query log row may sit at `running` before it is
// taken to belong to a process that died.
//
// Far above anything a live query can reach: the Query Runner's own deadline
// is five seconds, and admission control bounds the wait in front of it. The
// margin is what stops the sweeper marking a query that is merely slow.
const abandonedAfter = 2 * time.Minute

// sweepQueryLog closes rows abandoned by a process that died mid-query.
//
// The other half of the two-phase journal: rows are written before the query
// runs precisely so a crash leaves evidence, and evidence nobody ever closes
// is a row that says `running` for ever. Without this job the guarantee is
// only half-built, which is worse than not claiming it.
func sweepQueryLog(log *slog.Logger, sweep func(context.Context, time.Duration) (int64, error)) task {
	return task{
		name: "query-log-sweep",
		// At startup as well: the rows this closes were left open by a
		// process that died, and the process that just died is very often the
		// one this replaced. Cheap enough to be uninteresting either way —
		// one UPDATE against query_log_running_idx, a partial index over the
		// rows still at `running`.
		atStart: true,
		every:   time.Minute,
		run: func(ctx context.Context) error {
			swept, err := sweep(ctx, abandonedAfter)
			if err != nil {
				return err
			}
			if swept > 0 {
				// Worth a line at warning level: this number counts processes
				// that died in the middle of a participant's query.
				log.WarnContext(ctx, "closed query log rows left open by a crash", "rows", swept)
			}
			return nil
		},
	}
}

// scheduleTickInterval bounds how long a dead replica can delay a contest's
// published → running or running → finished transition (§8): each tick is a
// fresh competition for contests.Scheduler's advisory lock, never held
// between ticks, so a dead replica costs the next tick nothing but the one it
// missed. Short because this gates what a participant's own screen and
// events channel report the moment a contest's window opens or closes, not
// because the lock itself is expensive to ask for — two indexed UPDATEs and
// one pg_try_advisory_xact_lock, whether or not either UPDATE matches a row.
const scheduleTickInterval = 15 * time.Second

// advanceContestSchedule runs one tick of the background scheduler
// (contests.Scheduler.Advance): move every contest whose window opened or
// closed, or do nothing this tick because another replica already has it.
func advanceContestSchedule(log *slog.Logger, advance func(context.Context) (int, int, error)) task {
	return task{
		name: "contest-schedule",
		// At startup as well. Fifteen seconds is already short, but this is
		// what a participant's own screen and events channel wait on for a
		// contest whose window has just opened, and a restart should not be
		// fifteen seconds of "not started yet" on top of it. One tick is two
		// indexed UPDATEs and one pg_try_advisory_xact_lock whether or not it
		// moves anything.
		atStart: true,
		every:   scheduleTickInterval,
		run: func(ctx context.Context) error {
			started, finished, err := advance(ctx)
			if err != nil {
				return err
			}
			if started > 0 || finished > 0 {
				log.InfoContext(ctx, "advanced the contest schedule", "started", started, "finished", finished)
			}
			return nil
		},
	}
}

// tendPools keeps every live contest's pool stocked and free of stale copies.
//
// The background half of section 4.2, and the reason a participant arriving
// mid-contest does not wait: CREATE DATABASE happens here, on a timer, while
// nothing depends on it. Every minute rather than the ten minutes this used
// to be — Live already narrows this job to contests that are published or
// running, so a shorter cadence costs nothing on every other contest, and a
// tick that finds every pool already deep enough costs one query per live
// contest either way.
//
// Between ticks, tender — when the caller supplies one (see internal/app.go's
// own wiring) — lets two events skip the wait entirely rather than trim it to
// a minute: the scheduler moving a contest to running, and a roster growing
// on one that is published or running (contests.Scheduler and
// contests.Service, both through the same provisioning.Tender). Both fire
// after their own transaction commits and never block on this job actually
// running, which is what makes triggering them safe from a request handler
// and from the scheduler's own advisory-locked tick alike.
//
// How deep is its own roster's answer rather than a flat number — see
// provisioning.Service.RosterDepth, and the two bounds it is cut back to.
//
// It is also where a pool that was refused the depth it asked for becomes
// something an operator can read. The domain decides the refusal and hands it
// back (provisioning.Constrained); this is the half that owns the logger, and
// the line carries the measurements rather than only the outcome — "the pool
// stopped growing" is a support ticket, and the numbers beside it are the
// answer to it.
func tendPools(log *slog.Logger, service *provisioning.Service, limits provisioning.PoolLimits, tender *provisioning.Tender) task {
	var wake <-chan struct{}
	if tender != nil {
		wake = tender.C()
	}
	return task{
		name: "game-pool",
		// The job this was added for. A pool left idle even for a minute
		// after an API restart is every participant, for CREATE DATABASE,
		// inside their own page load — on a ten-connection pool whose
		// statement timeout is ten minutes. A tick that finds every pool
		// already deep enough costs one query per live contest, which is
		// what makes this safe to do at boot rather than something to be
		// careful about.
		atStart: true,
		every:   time.Minute,
		wake:    wake,
		run: func(ctx context.Context) error {
			depth := service.RosterDepth(limits, func(ctx context.Context, contest provisioning.Contest, sizing provisioning.Sizing) {
				// Warning and not info: a pool short of its roster means
				// participants waiting for CREATE DATABASE inside their own
				// page load, and for the disk bound it means the cluster is
				// nearly full — neither is routine.
				log.WarnContext(ctx, "a game pool was not allowed the depth its roster asked for",
					"contest", contest.ID, "bound", string(sizing.Bound),
					"granted", sizing.Depth, "wanted", sizing.Wanted,
					"template_bytes", sizing.TemplateBytes,
					"cluster_bytes", sizing.ClusterBytes, "budget_bytes", sizing.Budget)
			})
			made, dropped, err := service.Tend(ctx, depth)
			if made > 0 || dropped > 0 {
				log.InfoContext(ctx, "tended the game pools", "created", made, "dropped", dropped)
			}
			return err
		},
	}
}

// stuckOverdueBucket turns a raw Overdue duration into the granularity the
// de-duplication below actually cares about: whole days past stuckAfter.
// Overdue grows every tick a database stays stuck, so comparing the raw
// duration between ticks would never see two ticks agree — this is what lets
// "still stuck, ten minutes more overdue than last tick" read as unchanged
// while "still stuck, a full day more overdue" reads as a change worth a
// fresh line.
func stuckOverdueBucket(overdue time.Duration) int {
	return int(overdue / (24 * time.Hour))
}

// reclaimInstances drops every participant database whose contest finished
// longer ago than its grace period, and marks its row 'dropped' — the
// background half of §2.4. reclaim is provisioning.Service.Reclaim, taken as
// a function so the wrapping here (the log line, the metrics pair) can be
// proven without standing up a real Service; Reclaim's own rules — the grace
// period, leaving a busy database for the next tick, one failure not
// stopping the rest — are exercised where they are declared
// (internal/provisioning/reclaim_test.go and, for the busy-database refusal
// against a real cluster, internal/gamedb/provisioner_test.go).
//
// The same ten-minute cadence as tendPools: a grace period is configured in
// minutes at the shortest, so nothing meaningful is lost by checking on the
// same schedule the pool is already tended on rather than a faster one.
func reclaimInstances(log *slog.Logger, reclaim func(context.Context, int) (provisioning.ReclaimResult, error), graceMin int, counters *metrics.GameReclaimCounters) task {
	// stuckLogged remembers, for every database this process has already
	// warned about, the stuckOverdueBucket it was in at the last warning —
	// what lets the run closure below log a database once when it becomes
	// stuck and again only when that fact actually changes, rather than once
	// every ten minutes for as long as it stays busy (144 lines a day per
	// database otherwise: visible stops being useful and becomes noise an
	// operator learns to ignore, which is the same as silence). Safe
	// unlocked: runPeriodically (this file) never runs two ticks of the same
	// task concurrently, so nothing else ever touches this map.
	stuckLogged := make(map[string]int)

	return task{
		name: "game-reclaim",
		// The one job in this file that deliberately does *not* run at
		// startup, and the only one whose tick drops databases.
		//
		// Nothing waits on it. Its input is bounded by a grace period an
		// organizer configures in minutes and this installation defaults to a
		// day (GAME_INSTANCE_GRACE_MIN), so every decision it takes at boot
		// it takes identically ten minutes later — there is no participant,
		// and no organiser, on the other end of the difference. Against that
		// nothing, running it at boot buys a DROP DATABASE storm competing
		// with tendPools' CREATE DATABASE above for the same provisioning
		// pool at the one moment the process has the least idea what is going
		// on: a restart during a contest, or a replica rolling. A crash loop
		// would repeat that on every boot.
		//
		// So it keeps its tick. See tendPools above for the shape of a job
		// where the opposite is true.
		every: 10 * time.Minute,
		run: func(ctx context.Context) error {
			result, err := reclaim(ctx, graceMin)
			counters.AddInstances(result.Reclaimed, result.Skipped, result.Failed)
			counters.AddTemplates(result.TemplatesReclaimed, result.TemplatesFailed)
			if result.Reclaimed > 0 || result.Skipped > 0 || result.Failed > 0 ||
				result.TemplatesReclaimed > 0 || result.TemplatesFailed > 0 {
				// Worth a line even on plain success: this is the number an
				// organizer who cannot find a database has no other way to
				// notice moved at all, short of reading the audit trail one
				// contest at a time. Skipped is included here on purpose —
				// "reclaimed=0 failed=0" used to read as "nothing to do" even
				// when every candidate was left busy for the next tick.
				log.InfoContext(ctx, "reclaimed game instances",
					"reclaimed", result.Reclaimed, "skipped", result.Skipped, "failed", result.Failed,
					"templates_reclaimed", result.TemplatesReclaimed, "templates_failed", result.TemplatesFailed)
			}
			// Named separately from the summary line above rather than
			// folded into it: a database stuck this long past its grace is
			// not "one more of the routine skips a busy tick always has", it
			// is the specific thing an operator should go look at. But it is
			// named once per fact, not once per tick — see stuckLogged above
			// for why a repeat with nothing changed is silent.
			seen := make(map[string]bool, len(result.Stuck))
			for _, s := range result.Stuck {
				seen[s.Database] = true
				bucket := stuckOverdueBucket(s.Overdue)
				if last, warned := stuckLogged[s.Database]; !warned || bucket != last {
					log.WarnContext(ctx, "a game instance has been busy long past its grace deadline",
						"database", s.Database, "contest", s.ContestID, "overdue", s.Overdue.Round(time.Minute).String())
					stuckLogged[s.Database] = bucket
				}
			}
			// Anything no longer stuck — reclaimed, or simply back under
			// stuckAfter — starts clean. If it becomes stuck again later that
			// is a new fact, worth its own first warning, not silence because
			// this process once warned about the same database before.
			for database := range stuckLogged {
				if !seen[database] {
					delete(stuckLogged, database)
				}
			}
			return err
		},
	}
}

// buildGamesEvery is how often the builder looks for a game waiting to be
// built.
//
// Short, unlike every other job in this file, because on the other end of it
// is an organiser who has just pressed save and is watching a status. Each
// tick that finds nothing is one indexed row read; a tick that finds
// something does minutes of work and the next one simply finds nothing while
// it runs.
const buildGamesEvery = 5 * time.Second

// staleBuildMargin is the room the stale cut-off leaves beyond the budget the
// script itself is given.
//
// GAME_BUILD_TIMEOUT bounds one call to gamedb.Provisioner.runScript, not the
// whole of BuildTemplate: the DROP and CREATE DATABASE that bracket it, the
// two connections it opens, the grants and the hardening afterwards are all
// outside that budget and bounded instead by the provisioning pool's own
// statement timeout. Dropping and recreating a template holding a
// multi-gigabyte dump is disk work of the same order as one of those
// statements, so the margin is one of them rather than a round number picked
// beside it.
const staleBuildMargin = provisionStatementTimeout

// staleBuildAfter is how long a game may sit in `building` before another tick
// takes it back.
//
// It is derived from the build's own budget rather than declared beside it,
// because the two have to be consistent and a constant cannot stay consistent
// with a value a deployment sets (CLAUDE.md rule 11): GAME_BUILD_TIMEOUT
// defaults to thirty minutes, and the fifteen-minute constant this replaces
// meant a build of a three-gigabyte dump was reclaimed while it was still
// streaming. What that costs is not a wasted tick — the second BuildTemplate
// begins by dropping the half-filled template out from under the first, so the
// first fails with PostgreSQL's "terminating connection due to administrator
// command" shown to the organiser as though their SQL had been refused, its
// own failure path drops the database the second one is now filling, and the
// next tick fifteen minutes later starts the same again. A game that does not
// build inside the cut-off never reaches `ready` at all.
//
// This is not "how long a build may take": a build that is still running holds
// nothing that stops a second one starting beside it, so the figure has to
// outlast any build that can still be alive. That it now does costs a slower
// recovery of a build a crash really did abandon — the whole budget plus the
// margin rather than a flat fifteen minutes — which is the right way round:
// waiting is recoverable, and racing two builds over one template is not.
//
// The alternative considered was a heartbeat, the running build touching
// `updated_at` so a dead build could be told from a long one directly. It buys
// the faster recovery back, and costs a goroutine per build, a repository
// write every tick of it, and a new way for a build to be declared dead (the
// heartbeat failing) that has nothing to do with the build. Deriving the
// cut-off needs none of that and is provable in one assertion, which is what
// background_test.go makes.
func staleBuildAfter(buildTimeout time.Duration) time.Duration {
	return buildTimeout + staleBuildMargin
}

// buildGames builds one waiting game per tick.
//
// The job exists because building creates a database and runs an author's
// whole script inside it, which is not something to hold an HTTP request open
// for — and because an API that dies mid-build has to leave the work
// recoverable rather than a row stuck in `building` for ever.
//
// build is provisioning.Games.Build, taken as a function for the same reason
// reclaimInstances and sweepQueryLog take theirs: what this file owns is the
// wrapping — the cut-off it asks for, the log lines — and that has to be
// provable without a game cluster behind it. buildTimeout is the deployment's
// GAME_BUILD_TIMEOUT, the same value internal/app hands the provisioner, so
// that the one number decides both how long a build may run and how long
// before another tick assumes it is dead.
func buildGames(
	log *slog.Logger,
	build func(context.Context, time.Duration) (provisioning.Template, error),
	buildTimeout time.Duration,
) task {
	stale := staleBuildAfter(buildTimeout)
	return task{
		name: "game-build",
		// At startup as well. A game left in `building` by the process that
		// died is only picked up again by a tick of this job, and on the
		// other end of it is an organiser watching a status: five seconds
		// sooner is five seconds, but a boot that finds a stale build is
		// exactly the case this recovery exists for.
		atStart: true,
		every:   buildGamesEvery,
		run: func(ctx context.Context) error {
			built, err := build(ctx, stale)
			if errors.Is(err, provisioning.ErrNoGame) {
				// Nothing waiting, which is what almost every tick finds.
				return nil
			}
			if built.Status == provisioning.TemplateFailed {
				// The organiser sees this on their own screen; the line is
				// here so an operator reading the log knows why a contest
				// cannot be published. `error` is the text the organiser was
				// given, which for a failure that was not their script is
				// only provisioning.BuildFailedInternally — the untouched
				// cause comes back as err just below, and the log is now the
				// one place it exists.
				log.WarnContext(ctx, "a game failed to build",
					"contest", built.ContestID, "version", built.Version, "error", built.BuildError)
			}
			if err != nil {
				return err
			}
			if built.Status == provisioning.TemplateFailed {
				return nil
			}
			log.InfoContext(ctx, "built a game",
				"contest", built.ContestID, "version", built.Version, "database", built.Database)
			return nil
		},
	}
}

// abandonedUploadsEvery is how often the janitor looks for an upload nobody
// is coming back to, and for a file on the volume no row names at all. Ten
// minutes, the same cadence tendPools and reclaimInstances already tick on:
// nothing here is urgent — an abandoned upload is measured in
// GAME_UPLOAD_ABANDONED_AFTER, hours at the shortest sensible setting — and a
// faster tick would only mean walking the upload directory more often for no
// participant or organiser waiting on the answer.
const abandonedUploadsEvery = 10 * time.Minute

// abandonedUploads frees the disk an incomplete or forgotten upload is
// holding: a 'receiving' row nobody has appended to in a while is aborted,
// and a file the volume holds that no row names at all — the more dangerous
// half, invisible to every other query this package makes — is removed too.
// See provisioning.Games.SweepUploads for why both sweeps live together.
func abandonedUploads(log *slog.Logger, games *provisioning.Games, olderThan time.Duration) task {
	return task{
		name: "game-upload-sweep",
		// Not at startup: nothing here is urgent, the same reasoning
		// reclaimInstances gives for skipping its own atStart — an
		// abandoned upload found ten minutes into the process's life is
		// found exactly as correctly as one found at the instant it comes
		// up, and a boot-time sweep of a whole directory competes with
		// whatever else a restart is already doing.
		every: abandonedUploadsEvery,
		run: func(ctx context.Context) error {
			result, err := games.SweepUploads(ctx, olderThan)
			if result.Abandoned > 0 || result.OrphanFiles > 0 {
				log.InfoContext(ctx, "swept abandoned uploads",
					"abandoned", result.Abandoned, "orphan_files", result.OrphanFiles)
			}
			return err
		},
	}
}
