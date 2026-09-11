package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/devrdn/db-contest/backend/internal/postgres"
	"github.com/devrdn/db-contest/backend/internal/provisioning"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
	"github.com/jackc/pgx/v5"
)

// provisionReport is how long the copies took and what they cost.
type provisionReport struct {
	Copies        int   `json:"copies"`
	Workers       int   `json:"workers"`
	TemplateBytes int64 `json:"template_bytes"`
	// Elapsed is the whole top-up; Milestones is how long it took for the
	// pool to hold a given number of copies, which is the answer to "how long
	// before thirty participants each have one".
	Elapsed    time.Duration         `json:"elapsed_ns"`
	Milestones map[int]time.Duration `json:"milestones_ns"`
	// ClusterBytesBefore and After are the whole cluster; CopiesBytes is the
	// sum of this contest's copies alone.
	ClusterBytesBefore int64 `json:"cluster_bytes_before"`
	ClusterBytesAfter  int64 `json:"cluster_bytes_after"`
	CopiesBytes        int64 `json:"copies_bytes"`
}

// applyCopySettings sets run-time parameters on the fixture's own copies —
// ALTER DATABASE … SET, which every connection the Query Runner opens to them
// afterwards starts with. It is how a setting the game cluster would carry
// for everybody (max_parallel_workers_per_gather, say) can be measured
// without changing the cluster: only the harness's databases see it, and
// they are dropped at the end. DEFAULT as the value removes the setting.
func applyCopySettings(ctx context.Context, st *stores, f *fixture, settings map[string]string) error {
	if len(settings) == 0 {
		return nil
	}
	rows, err := st.core.Query(ctx, `SELECT db_name FROM game_instances WHERE contest_id = $1`, f.ContestID)
	if err != nil {
		return err
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, name := range sortedKeys(settings) {
		value := settings[name]
		// Both halves end up in the statement's text: a parameter cannot
		// name a setting. Plain identifiers and plain values only.
		if !sqlpolicy.PlainIdentifier(name) || !plainSettingValue(value) {
			return fmt.Errorf("-copy-setting %s=%s: only plain names and values", name, value)
		}
		assignment := name + " TO " + value
		if value != "DEFAULT" {
			assignment = name + " TO '" + value + "'"
		}
		for _, database := range names {
			if _, err := st.game.Exec(ctx, `ALTER DATABASE `+sqlpolicy.QuoteIdentifier(database)+` SET `+assignment); err != nil {
				return fmt.Errorf("set %s on %s: %w", name, database, err)
			}
		}
	}
	return nil
}

func plainSettingValue(v string) bool {
	if v == "" || len(v) > 64 {
		return false
	}
	for _, r := range v {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '.') {
			return false
		}
	}
	return true
}

// provision fills the contest's pool the way the API's pool tender would.
//
// Through the product's own provisioning.Service.TopUp, with the same
// number of workers and the same depth — the roster plus GAME_POOL_DEPTH of
// headroom (provisioning.RosterDepth) — so that what is measured is what the
// tender does before a contest opens. Doing it here rather than waiting for
// the tender is what makes the measurement repeatable: the tender runs at
// start-up and then every ten minutes, and a harness that cannot restart the
// API cannot choose when.
//
// It also leaves the pool exactly as deep as the tender wants it, so the
// tender's next tick finds nothing to do and cannot start copying databases
// in the middle of a measured run.
func provision(ctx context.Context, st *stores, f *fixture, depth, workers int, milestones []int) (provisionReport, error) {
	report := provisionReport{Workers: workers, Milestones: map[int]time.Duration{}}

	games := postgres.NewGameInstances(st.core)
	// The contest as the API sees it: template, version and the policy the
	// copies are settled for, read by the same query the console uses.
	contest, err := games.Game(ctx, f.ContestID)
	if err != nil {
		return report, fmt.Errorf("read the contest's game: %w", err)
	}

	if report.TemplateBytes, err = st.provisioner.DatabaseSize(ctx, contest.Template); err != nil {
		return report, err
	}
	if report.ClusterBytesBefore, err = st.provisioner.ClusterBytes(ctx); err != nil {
		return report, err
	}

	service := provisioning.New(games, st.provisioner).WithWorkers(workers)

	// Milestones are read off the pool's own depth while TopUp works, rather
	// than by timing each copy, because TopUp does not report them one by one
	// and a harness that reimplemented it would be measuring itself.
	var (
		mu      sync.Mutex
		started = time.Now()
		done    = make(chan struct{})
		watched sync.WaitGroup
	)
	watched.Add(1)
	go func() {
		defer watched.Done()
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
			}
			have, err := games.SpareCount(ctx, contest.ID, contest.Version)
			if err != nil {
				continue
			}
			mu.Lock()
			for _, m := range milestones {
				if _, seen := report.Milestones[m]; !seen && have >= m {
					report.Milestones[m] = time.Since(started)
				}
			}
			mu.Unlock()
		}
	}()

	made, err := service.TopUp(ctx, contest, depth)
	report.Elapsed = time.Since(started)
	close(done)
	watched.Wait()
	report.Copies = made
	if err != nil {
		return report, fmt.Errorf("provision the copies (%d made): %w", made, err)
	}

	// A milestone the ticker missed because TopUp finished between two ticks
	// was reached by the end.
	for _, m := range milestones {
		if _, seen := report.Milestones[m]; !seen && made >= m {
			report.Milestones[m] = report.Elapsed
		}
	}

	if report.ClusterBytesAfter, err = st.provisioner.ClusterBytes(ctx); err != nil {
		return report, err
	}
	rows, err := st.core.Query(ctx, `SELECT db_name FROM game_instances WHERE contest_id = $1`, f.ContestID)
	if err != nil {
		return report, err
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return report, err
	}
	sizes, err := st.provisioner.DatabaseSizes(ctx, names)
	if err != nil {
		return report, err
	}
	for _, size := range sizes {
		report.CopiesBytes += size
	}
	return report, nil
}
