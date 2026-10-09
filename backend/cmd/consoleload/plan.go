package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type report struct {
	Label        string            `json:"label,omitempty"`
	API          string            `json:"api"`
	Source       string            `json:"source_template,omitempty"`
	Mix          mix               `json:"mix"`
	CopySettings map[string]string `json:"copy_settings,omitempty"`
	Setup        *setupReport      `json:"setup,omitempty"`
	Provision    *provisionReport  `json:"provision,omitempty"`
	FirstQuery   *runSummary       `json:"first_query,omitempty"`
	Runs         []runSummary      `json:"runs"`
	CountsBefore counts            `json:"counts_before"`
	CountsAfter  counts            `json:"counts_after,omitempty"`
	Kept         bool              `json:"kept,omitempty"`
}

func execute(ctx context.Context, st *stores, cfg config, out *os.Root) (err error) {
	rep := &report{Label: cfg.label, API: cfg.api, Source: cfg.sourceTemplate, Mix: cfg.mix}
	if rep.CountsBefore, err = countEverything(ctx, st); err != nil {
		return err
	}

	needed := slices.Max(cfg.participants)
	f := &fixture{}
	if cfg.fixture != "" {
		if f, err = loadFixture(cfg.fixture); err != nil {
			return err
		}
		if len(f.Participants) < needed {
			return fmt.Errorf("the kept fixture has %d participants and this run needs %d", len(f.Participants), needed)
		}
	}

	var parts []*participant
	// Teardown runs however the run ended; only -keep skips it.
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Minute)
		defer cancel()
		for _, p := range parts {
			_ = p.signOut(cleanup, cfg.api)
		}

		if cfg.keep {
			rep.Kept = true
			if saveErr := f.save(out, "fixture.json"); saveErr != nil {
				err = errors.Join(err, saveErr)
			}
			fmt.Printf("the fixture was kept: reuse it with -fixture %s, remove it with a run that omits -keep or with `consoleload sweep`\n",
				filepath.Join(cfg.out, "fixture.json"))
		} else {
			fmt.Println("tearing down")
			if downErr := teardown(cleanup, st, f); downErr != nil {
				err = errors.Join(err, fmt.Errorf("teardown: %w (run `consoleload sweep`)", downErr))
			}
			if cfg.fixture != "" {
				_ = os.Remove(cfg.fixture) // #nosec G703 -- the operator's own -fixture path.
			}
		}
		if after, countErr := countEverything(cleanup, st); countErr == nil {
			rep.CountsAfter = after
		}
		if writeErr := writeReport(out, rep); writeErr != nil {
			err = errors.Join(err, writeErr)
		}
	}()

	if cfg.fixture == "" {
		fmt.Printf("setting up %d participants on a copy of %s\n", needed, cfg.sourceTemplate)
		setupRep, err := setup(ctx, st, f, cfg.sourceTemplate, needed)
		if err != nil {
			return err
		}
		rep.Setup = &setupRep

		depth := needed + cfg.headroom
		fmt.Printf("provisioning %d copies (%d participants + %d headroom) with %d workers\n",
			depth, needed, cfg.headroom, cfg.workers)
		milestones := append(slices.Clone(cfg.participants), depth)
		provisionRep, err := provision(ctx, st, f, depth, cfg.workers, milestones)
		rep.Provision = &provisionRep
		if err != nil {
			return err
		}
		fmt.Printf("provisioned %d copies in %s (%s on the cluster)\n",
			provisionRep.Copies, provisionRep.Elapsed.Round(time.Millisecond), human(provisionRep.CopiesBytes))
	}

	if err := applyCopySettings(ctx, st, f, cfg.copySettings); err != nil {
		return err
	}
	rep.CopySettings = cfg.copySettings

	secrets, err := credentials(ctx, st, f)
	if err != nil {
		return err
	}
	for i, record := range f.Participants[:needed] {
		parts = append(parts, newParticipant(i, record, cfg.seed))
	}
	if err := signInAll(ctx, cfg.api, parts, secrets); err != nil {
		return err
	}
	clear(secrets)

	l := load{api: cfg.api, contest: f.ContestID, mix: cfg.mix, inFlight: &gauge{}}

	// The first query claims each participant's copy, so it is measured
	// apart from the runs.
	first, err := firstQueries(ctx, l, parts)
	if err != nil {
		return err
	}
	rep.FirstQuery = &first
	fmt.Printf("first query: p50 %.0f ms, p95 %.0f ms, max %.0f ms; %s\n",
		first.All.P50, first.All.P95, first.All.Max, joinCounts(first.Outcomes))

	watch := &sampler{
		pids: cfg.watchPIDs, containers: cfg.watchContainers,
		game: st.game, prefixes: databasePrefixes([]uuid.UUID{f.ContestID}),
	}
	for _, n := range cfg.participants {
		for _, shape := range cfg.shapes {
			// So one run's tail does not become the next one's head.
			if !sleep(ctx, cfg.cooldown) {
				return ctx.Err()
			}
			name := fmt.Sprintf("%s-%d", shape, n)
			fmt.Printf("%s: running\n", name)

			rec := watch.start(ctx)
			l.inFlight.restart()
			started := time.Now()
			var outcomes []outcome
			switch shape {
			case shapeSteady:
				outcomes = l.steady(ctx, name, parts[:n], cfg.steadyFor, cfg.thinkMin, cfg.thinkMax)
			case shapeBurst:
				outcomes = l.burst(ctx, name, parts[:n], cfg.burstRounds, cfg.burstGap, cfg.burstWindow)
			}
			ended := time.Now()
			used, active := rec.finish()

			summary := summarise(name, shape, n, started, ended, outcomes)
			summary.Usage, summary.Backends, summary.InFlightMax = used, active, l.inFlight.restart()
			regs := f.registrationIDs()[:n]
			if summary.Journal, err = readJournal(ctx, st.core, regs, started.Add(-time.Second), ended.Add(time.Second)); err != nil {
				return err
			}
			rep.Runs = append(rep.Runs, summary)
			if err := writeOutcomes(out, name, outcomes); err != nil {
				return err
			}
			fmt.Printf("%s: %d queries, p50 %.0f ms, p95 %.0f ms, p99 %.0f ms; %s\n",
				name, summary.Queries, summary.All.P50, summary.All.P95, summary.All.P99, joinCounts(summary.Outcomes))
			if ctx.Err() != nil {
				return ctx.Err()
			}
		}
	}
	return nil
}

func firstQueries(ctx context.Context, l load, parts []*participant) (runSummary, error) {
	const atOnce = 4
	var (
		mu       sync.Mutex
		outcomes []outcome
		wg       sync.WaitGroup
		gate     = make(chan struct{}, atOnce)
	)
	started := time.Now()
	for _, p := range parts {
		wg.Add(1)
		gate <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-gate }()
			sql := templates[KindCheap][0](p.rng)
			o := p.ask(ctx, l.api, l.contest, "first-query", KindCheap, sql)
			mu.Lock()
			outcomes = append(outcomes, o)
			mu.Unlock()
		}()
	}
	wg.Wait()
	summary := summarise("first-query", "first", len(parts), started, time.Now(), outcomes)
	if summary.Outcomes["200"] != len(parts) {
		return summary, fmt.Errorf("not every participant could run a first query: %s (first refusal: %s)",
			joinCounts(summary.Outcomes), firstRefusal(outcomes))
	}
	return summary, nil
}

func firstRefusal(outcomes []outcome) string {
	for _, o := range outcomes {
		if !o.ok() {
			return fmt.Sprintf("%s %s", o.label(), o.Err)
		}
	}
	return "none"
}

// writeOutcomes writes every query of a run as JSON lines.
func writeOutcomes(out *os.Root, name string, outcomes []outcome) error {
	file, err := out.OpenFile(name+".jsonl", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	encoder := json.NewEncoder(file)
	for _, o := range outcomes {
		if err := encoder.Encode(o); err != nil {
			return err
		}
	}
	return nil
}

func writeReport(out *os.Root, rep *report) error {
	body, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	if err := out.WriteFile("report.json", body, 0o600); err != nil {
		return err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# Console load test\n\n")
	if rep.Label != "" {
		fmt.Fprintf(&b, "%s\n\n", rep.Label)
	}
	fmt.Fprintf(&b, "API %s; query mix cheap/mid/expensive %d/%d/%d.\n\n",
		rep.API, rep.Mix[KindCheap], rep.Mix[KindMid], rep.Mix[KindExpensive])
	if len(rep.CopySettings) > 0 {
		settings := make([]string, 0, len(rep.CopySettings))
		for _, name := range sortedKeys(rep.CopySettings) {
			settings = append(settings, name+"="+rep.CopySettings[name])
		}
		fmt.Fprintf(&b, "Set on the test's copies: %s.\n\n", strings.Join(settings, ", "))
	}
	if rep.Setup != nil {
		fmt.Fprintf(&b, "Template copied from %s: %s in %s.\n\n",
			rep.Source, human(rep.Setup.TemplateBytes), rep.Setup.TemplateCopy.Round(time.Millisecond))
	}
	if p := rep.Provision; p != nil {
		fmt.Fprintf(&b, "## Provisioning\n\n%d copies with %d workers in %s; %s of copies; the cluster grew from %s to %s.\n\n",
			p.Copies, p.Workers, p.Elapsed.Round(time.Millisecond), human(p.CopiesBytes),
			human(p.ClusterBytesBefore), human(p.ClusterBytesAfter))
		b.WriteString("| copies in the pool | after |\n|---:|---:|\n")
		keys := make([]int, 0, len(p.Milestones))
		for k := range p.Milestones {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "| %d | %s |\n", k, p.Milestones[k].Round(time.Millisecond))
		}
		b.WriteString("\n")
	}
	if q := rep.FirstQuery; q != nil {
		fmt.Fprintf(&b, "First query per participant (claims the copy): p50 %.0f ms, p95 %.0f ms, max %.0f ms; %s.\n\n",
			q.All.P50, q.All.P95, q.All.Max, joinCounts(q.Outcomes))
	}
	b.WriteString("## Runs\n\n")
	b.WriteString(markdown(rep.Runs))
	b.WriteString("\n## Counts before and after\n\n")
	if rep.CountsAfter != nil {
		b.WriteString(countsTable(rep.CountsBefore, rep.CountsAfter))
	}
	return out.WriteFile("report.md", []byte(b.String()), 0o600)
}
