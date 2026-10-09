package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type usage struct {
	// CPUAvg and CPUMax are in percent of one core.
	CPUAvg  float64 `json:"cpu_avg_pct"`
	CPUMax  float64 `json:"cpu_max_pct"`
	MemMax  int64   `json:"mem_max_bytes"`
	Samples int     `json:"samples"`
	// ReadBytes is what a container read from block devices during the run.
	ReadBytes int64 `json:"read_bytes,omitempty"`
}

type backends struct {
	// ActiveMax and ActiveAvg count connections running a statement;
	// ConnectMax counts all client connections.
	ActiveMax  int     `json:"active_max"`
	ActiveAvg  float64 `json:"active_avg"`
	ConnectMax int     `json:"connections_max"`
	// WorkersMax counts parallel workers, which the Query Runner's semaphore
	// does not see.
	WorkersMax int `json:"parallel_workers_max"`
	Samples    int `json:"samples"`
}

type sampler struct {
	pids       map[string]int
	containers map[string]string
	game       *pgxpool.Pool
	prefixes   []string
}

type recording struct {
	stop func()
	wg   sync.WaitGroup

	mu        sync.Mutex
	processes map[string]*accumulator
	boxes     map[string]*accumulator
	reads     map[string][2]int64 // first and last cumulative read, per container
	active    backends
	activeSum int
}

type accumulator struct {
	sum     float64
	max     float64
	mem     int64
	samples int
}

func (a *accumulator) add(cpu float64, mem int64) {
	a.sum += cpu
	a.max = max(a.max, cpu)
	a.mem = max(a.mem, mem)
	a.samples++
}

func (a *accumulator) usage() usage {
	u := usage{CPUMax: a.max, MemMax: a.mem, Samples: a.samples}
	if a.samples > 0 {
		u.CPUAvg = a.sum / float64(a.samples)
	}
	return u
}

func (s *sampler) start(parent context.Context) *recording {
	ctx, cancel := context.WithCancel(parent)
	r := &recording{
		stop:      cancel,
		processes: map[string]*accumulator{},
		boxes:     map[string]*accumulator{},
		reads:     map[string][2]int64{},
	}

	for name, pid := range s.pids {
		r.processes[name] = &accumulator{}
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			s.watchProcess(ctx, r, name, pid)
		}()
	}
	if len(s.containers) > 0 {
		for name := range s.containers {
			r.boxes[name] = &accumulator{}
		}
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			s.watchContainers(ctx, r)
		}()
	}
	if s.game != nil {
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			s.watchBackends(ctx, r)
		}()
	}
	return r
}

func (r *recording) finish() (map[string]usage, backends) {
	r.stop()
	r.wg.Wait()
	r.mu.Lock()
	defer r.mu.Unlock()

	out := map[string]usage{}
	for name, a := range r.processes {
		out[name] = a.usage()
	}
	for name, a := range r.boxes {
		u := a.usage()
		if pair, ok := r.reads[name]; ok {
			u.ReadBytes = pair[1] - pair[0]
		}
		out[name] = u
	}
	b := r.active
	if b.Samples > 0 {
		b.ActiveAvg = float64(r.activeSum) / float64(b.Samples)
	}
	return out, b
}

// watchProcess samples a process every second. CPU is the difference in
// accumulated CPU time, not ps's decaying average.
func (s *sampler) watchProcess(ctx context.Context, r *recording, name string, pid int) {
	const every = time.Second
	lastCPU, _, err := processTimes(pid)
	if err != nil {
		fmt.Fprintf(os.Stderr, "consoleload: cannot sample %s (pid %d): %v\n", name, pid, err)
		return
	}
	last := time.Now()
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		cpu, rss, err := processTimes(pid)
		if err != nil {
			continue
		}
		now := time.Now()
		percent := 100 * float64(cpu-lastCPU) / float64(now.Sub(last))
		lastCPU, last = cpu, now
		r.mu.Lock()
		r.processes[name].add(percent, rss)
		r.mu.Unlock()
	}
}

// processTimes reads accumulated CPU time and resident memory from /proc, or
// from ps elsewhere (macOS).
func processTimes(pid int) (time.Duration, int64, error) {
	if stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); err == nil {
		// After the parenthesised command (which may contain spaces): utime
		// and stime are fields 12 and 13 in clock ticks, rss is 22 in pages.
		fields := strings.Fields(string(stat[strings.LastIndexByte(string(stat), ')')+1:]))
		if len(fields) < 22 {
			return 0, 0, errors.New("short /proc stat")
		}
		utime, _ := strconv.ParseInt(fields[11], 10, 64)
		stime, _ := strconv.ParseInt(fields[12], 10, 64)
		pages, _ := strconv.ParseInt(fields[21], 10, 64)
		// USER_HZ is 100 on every Linux the service would run on.
		return time.Duration(utime+stime) * 10 * time.Millisecond, pages * int64(os.Getpagesize()), nil
	}

	out, err := exec.Command("ps", "-o", "time=,rss=", "-p", strconv.Itoa(pid)).Output() // #nosec G204
	if err != nil {
		return 0, 0, fmt.Errorf("ps: %w", err)
	}
	fields := strings.Fields(string(out))
	if len(fields) != 2 {
		return 0, 0, fmt.Errorf("ps printed %q", out)
	}
	cpu, err := parseCPUTime(fields[0])
	if err != nil {
		return 0, 0, err
	}
	rss, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return 0, 0, err
	}
	return cpu, rss * 1024, nil
}

// parseCPUTime reads ps's accumulated time: [[dd-]hh:]mm:ss[.cc].
func parseCPUTime(s string) (time.Duration, error) {
	var days int64
	if d, rest, ok := strings.Cut(s, "-"); ok {
		n, err := strconv.ParseInt(d, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("cpu time %q: %w", s, err)
		}
		days, s = n, rest
	}
	parts := strings.Split(s, ":")
	seconds, err := strconv.ParseFloat(parts[len(parts)-1], 64)
	if err != nil {
		return 0, fmt.Errorf("cpu time %q: %w", s, err)
	}
	total := seconds
	multiplier := 60.0
	for i := len(parts) - 2; i >= 0; i-- {
		n, err := strconv.ParseFloat(parts[i], 64)
		if err != nil {
			return 0, fmt.Errorf("cpu time %q: %w", s, err)
		}
		total += n * multiplier
		multiplier *= 60
	}
	total += float64(days) * 86400
	return time.Duration(total * float64(time.Second)), nil
}

// watchContainers samples containers with docker stats; one call covers all
// of them and takes about two seconds.
func (s *sampler) watchContainers(ctx context.Context, r *recording) {
	byContainer := map[string]string{}
	args := []string{"stats", "--no-stream", "--format", "{{.Name}}\t{{.CPUPerc}}\t{{.MemUsage}}\t{{.BlockIO}}"}
	for name, container := range s.containers {
		byContainer[container] = name
		args = append(args, container)
	}
	for ctx.Err() == nil {
		out, err := exec.CommandContext(ctx, "docker", args...).Output()
		if err != nil {
			if ctx.Err() == nil {
				fmt.Fprintf(os.Stderr, "consoleload: docker stats: %v\n", err)
				sleep(ctx, time.Second)
			}
			continue
		}
		scanner := bufio.NewScanner(strings.NewReader(string(out)))
		for scanner.Scan() {
			fields := strings.Split(scanner.Text(), "\t")
			if len(fields) != 4 {
				continue
			}
			name, ok := byContainer[strings.TrimPrefix(fields[0], "/")]
			if !ok {
				continue
			}
			cpu, _ := strconv.ParseFloat(strings.TrimSuffix(fields[1], "%"), 64)
			memText, _, _ := strings.Cut(fields[2], "/")
			mem, _ := parseSize(memText)
			readText, _, _ := strings.Cut(fields[3], "/")
			read, _ := parseSize(readText)

			r.mu.Lock()
			r.boxes[name].add(cpu, mem)
			pair, seen := r.reads[name]
			if !seen {
				pair[0] = read
			}
			pair[1] = read
			r.reads[name] = pair
			r.mu.Unlock()
		}
	}
}

// parseSize reads docker's sizes: "512.3MiB", "1.2GB", "0B".
func parseSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	units := []struct {
		suffix string
		scale  float64
	}{
		{"KiB", 1 << 10}, {"MiB", 1 << 20}, {"GiB", 1 << 30}, {"TiB", 1 << 40},
		{"kB", 1e3}, {"KB", 1e3}, {"MB", 1e6}, {"GB", 1e9}, {"TB", 1e12}, {"B", 1},
	}
	for _, u := range units {
		if number, ok := strings.CutSuffix(s, u.suffix); ok {
			n, err := strconv.ParseFloat(strings.TrimSpace(number), 64)
			if err != nil {
				return 0, fmt.Errorf("size %q: %w", s, err)
			}
			return int64(n * u.scale), nil
		}
	}
	return 0, fmt.Errorf("size %q has no unit", s)
}

// watchBackends counts, several times a second, the cluster's connections to
// this contest's databases and how many are running a statement.
func (s *sampler) watchBackends(ctx context.Context, r *recording) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		var active, connected, workers int
		err := s.game.QueryRow(ctx, `
			SELECT count(*) FILTER (WHERE backend_type = 'client backend' AND state = 'active'),
			       count(*) FILTER (WHERE backend_type = 'client backend'),
			       count(*) FILTER (WHERE backend_type = 'parallel worker')
			FROM pg_stat_activity
			WHERE EXISTS (SELECT 1 FROM unnest($1::text[]) AS p WHERE left(datname, length(p)) = p)`,
			s.prefixes).Scan(&active, &connected, &workers)
		if err != nil {
			continue
		}
		r.mu.Lock()
		r.active.ActiveMax = max(r.active.ActiveMax, active)
		r.active.ConnectMax = max(r.active.ConnectMax, connected)
		r.active.WorkersMax = max(r.active.WorkersMax, workers)
		r.active.Samples++
		r.activeSum += active
		r.mu.Unlock()
	}
}
