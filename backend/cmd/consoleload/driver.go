package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"sync"
	"time"

	"github.com/devrdn/db-contest/backend/internal/auth"
	"github.com/google/uuid"
)

const (
	shapeSteady = "steady"
	shapeBurst  = "burst"
)

// participant is one synthetic participant with its own session and its own
// connection to the API, as a browser would have.
type participant struct {
	index   int
	record  participantRecord
	client  *http.Client
	session *http.Cookie
	rng     *rand.Rand
}

type outcome struct {
	Run         string        `json:"run"`
	Participant int           `json:"participant"`
	Kind        Kind          `json:"kind"`
	SQL         string        `json:"sql"`
	Sent        time.Time     `json:"sent"`
	Latency     time.Duration `json:"latency_ns"`
	// Status is zero when no response arrived.
	Status int    `json:"status"`
	Code   string `json:"code,omitempty"`
	// StatementMicros is the Query Runner's own measurement; zero for a
	// refusal.
	StatementMicros int64  `json:"statement_micros,omitempty"`
	Rows            int    `json:"rows,omitempty"`
	Err             string `json:"error,omitempty"`
}

func (o outcome) ok() bool { return o.Status == http.StatusOK }

// label is the key refusals are counted under.
func (o outcome) label() string {
	switch {
	case o.Status == 0:
		return "no response"
	case o.Code == "":
		return fmt.Sprintf("%d", o.Status)
	default:
		return fmt.Sprintf("%d %s", o.Status, o.Code)
	}
}

// requestTimeout is far beyond the Query Runner's deadline, so it only fires
// on a hang.
const requestTimeout = 2 * time.Minute

func newParticipant(index int, record participantRecord, seed int64) *participant {
	return &participant{
		index:  index,
		record: record,
		client: &http.Client{
			Timeout: requestTimeout,
			// One connection per participant, kept alive between queries.
			Transport: &http.Transport{MaxIdleConnsPerHost: 1, IdleConnTimeout: 5 * time.Minute},
		},
		// A reproducible load shape, not a security decision.
		rng: rand.New(rand.NewPCG(uint64(seed), uint64(index))), // #nosec G115 G404
	}
}

func (p *participant) signIn(ctx context.Context, api, secret string) error {
	body, _ := json.Marshal(map[string]string{"login": p.record.Login, "password": secret})
	resp, err := p.post(ctx, api+"/api/v1/auth/login", body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("sign in %s: %d %s", p.record.Login, resp.StatusCode, detail)
	}
	for _, c := range resp.Cookies() {
		if c.Name == auth.SessionCookieName {
			// #nosec G124 -- a client keeping a cookie to send back, not a
			// server setting one: the attributes are the API's to decide.
			p.session = &http.Cookie{Name: c.Name, Value: c.Value}
			return nil
		}
	}
	return fmt.Errorf("sign in %s: no %s cookie in the answer", p.record.Login, auth.SessionCookieName)
}

// signOut ends the session so it does not linger in the session store.
func (p *participant) signOut(ctx context.Context, api string) error {
	if p.session == nil {
		return nil
	}
	resp, err := p.post(ctx, api+"/api/v1/auth/logout", nil)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	p.session = nil
	if resp.StatusCode >= 300 {
		return fmt.Errorf("sign out %s: %d", p.record.Login, resp.StatusCode)
	}
	return nil
}

// post sends a JSON body to the API at the operator's -api address.
func (p *participant) post(ctx context.Context, url string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body)) // #nosec G704 -- the URL is the operator's -api address.
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if p.session != nil {
		req.AddCookie(p.session)
	}
	return p.client.Do(req) // #nosec G704 -- the URL is the operator's -api address.
}

func (p *participant) ask(ctx context.Context, api string, contest uuid.UUID, run string, kind Kind, sql string) outcome {
	o := outcome{Run: run, Participant: p.index, Kind: kind, SQL: sql, Sent: time.Now()}
	body, _ := json.Marshal(map[string]string{"sql": sql})

	resp, err := p.post(ctx, fmt.Sprintf("%s/api/v1/contests/%s/query", api, contest), body)
	if err != nil {
		o.Latency = time.Since(o.Sent)
		o.Err = err.Error()
		return o
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	// Latency runs until the whole answer has arrived.
	o.Latency = time.Since(o.Sent)
	o.Status = resp.StatusCode
	if err != nil {
		o.Err = err.Error()
		return o
	}

	if resp.StatusCode == http.StatusOK {
		var answer struct {
			Rows           []json.RawMessage `json:"rows"`
			DurationMicros int64             `json:"duration_micros"`
		}
		if err := json.Unmarshal(payload, &answer); err != nil {
			o.Err = "unreadable answer: " + err.Error()
			return o
		}
		o.Rows, o.StatementMicros = len(answer.Rows), answer.DurationMicros
		return o
	}

	var refusal struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(payload, &refusal) == nil {
		o.Code = refusal.Error.Code
		o.Err = refusal.Error.Message
	}
	return o
}

type load struct {
	api     string
	contest uuid.UUID
	mix     mix
	// inFlight counts queries sent and not yet answered. Its peak, less the
	// cluster's running queries, is the Query Runner's queue length, which
	// the service does not expose.
	inFlight *gauge
}

func (l load) ask(ctx context.Context, p *participant, run string, kind Kind, sql string) outcome {
	l.inFlight.enter()
	defer l.inFlight.leave()
	return p.ask(ctx, l.api, l.contest, run, kind, sql)
}

type gauge struct {
	mu        sync.Mutex
	now, peak int
}

func (g *gauge) enter() {
	g.mu.Lock()
	g.now++
	g.peak = max(g.peak, g.now)
	g.mu.Unlock()
}

func (g *gauge) leave() {
	g.mu.Lock()
	g.now--
	g.mu.Unlock()
}

func (g *gauge) restart() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	peak := g.peak
	g.peak = g.now
	return peak
}

// steady runs a normal round: ask, read, think, repeat, until time is up.
// Starts are spread over one think time so the run does not open as a burst.
func (l load) steady(ctx context.Context, run string, parts []*participant, duration, thinkMin, thinkMax time.Duration) []outcome {
	var (
		mu       sync.Mutex
		outcomes []outcome
		wg       sync.WaitGroup
	)
	stopAt := time.Now().Add(duration)
	think := func(p *participant) time.Duration {
		return thinkMin + time.Duration(p.rng.Int64N(int64(thinkMax-thinkMin)+1))
	}

	for _, p := range parts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			wait := time.Duration(p.rng.Int64N(int64(thinkMax)))
			for {
				if !sleep(ctx, wait) || time.Now().After(stopAt) {
					return
				}
				kind, sql := l.mix.pick(p.rng)
				o := l.ask(ctx, p, run, kind, sql)
				mu.Lock()
				outcomes = append(outcomes, o)
				mu.Unlock()
				wait = think(p)
			}
		}()
	}
	wg.Wait()
	return outcomes
}

func (l load) burst(ctx context.Context, run string, parts []*participant, rounds int, gap, window time.Duration) []outcome {
	var (
		mu       sync.Mutex
		outcomes []outcome
	)
	for round := range rounds {
		if round > 0 && !sleep(ctx, gap) {
			break
		}
		var wg sync.WaitGroup
		start := time.Now()
		for _, p := range parts {
			// Drawn up front, not left to goroutine scheduling.
			offset := time.Duration(0)
			if window > 0 {
				offset = time.Duration(p.rng.Int64N(int64(window)))
			}
			kind, sql := l.mix.pick(p.rng)
			wg.Add(1)
			go func() {
				defer wg.Done()
				if !sleep(ctx, time.Until(start.Add(offset))) {
					return
				}
				o := l.ask(ctx, p, run, kind, sql)
				mu.Lock()
				outcomes = append(outcomes, o)
				mu.Unlock()
			}()
		}
		wg.Wait()
		if ctx.Err() != nil {
			break
		}
	}
	return outcomes
}

// sleep waits d, or less if ctx ends first; false means ctx ended.
func sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// signInAll signs participants in a few at a time, since each sign-in
// verifies an Argon2 hash.
func signInAll(ctx context.Context, api string, parts []*participant, secrets []string) error {
	const atOnce = 4
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		failures []error
		gate     = make(chan struct{}, atOnce)
	)
	for i, p := range parts {
		wg.Add(1)
		gate <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-gate }()
			if err := p.signIn(ctx, api, secrets[i]); err != nil {
				mu.Lock()
				failures = append(failures, err)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return errors.Join(failures...)
}
