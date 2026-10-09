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

type provisionReport struct {
	Copies        int           `json:"copies"`
	Workers       int           `json:"workers"`
	TemplateBytes int64         `json:"template_bytes"`
	Elapsed       time.Duration `json:"elapsed_ns"`
	// Milestones maps a copy count to how long the pool took to hold it.
	Milestones         map[int]time.Duration `json:"milestones_ns"`
	ClusterBytesBefore int64                 `json:"cluster_bytes_before"`
	ClusterBytesAfter  int64                 `json:"cluster_bytes_after"`
	CopiesBytes        int64                 `json:"copies_bytes"`
}

// applyCopySettings runs ALTER DATABASE … SET on the fixture's own copies, so
// a cluster-wide setting can be measured without changing the cluster.
// DEFAULT removes a setting.
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
		// Both go into the statement text, so only plain values pass.
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

// provision fills the contest's pool through provisioning.Service.TopUp with
// the tender's workers and depth, so the measurement is repeatable. The pool
// ends as deep as the tender wants, so its next tick copies nothing mid-run.
func provision(ctx context.Context, st *stores, f *fixture, depth, workers int, milestones []int) (provisionReport, error) {
	report := provisionReport{Workers: workers, Milestones: map[int]time.Duration{}}

	games := postgres.NewGameInstances(st.core)
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

	// Milestones are read off the pool's depth while TopUp works.
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
