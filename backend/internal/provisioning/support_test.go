package provisioning_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/devrdn/db-contest/backend/internal/platform/storage/storagetest"
	"github.com/devrdn/db-contest/backend/internal/provisioning"
	"github.com/devrdn/db-contest/backend/internal/sqlpolicy"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Service tests use the real repository, since claiming and versioning are
// properties of the SQL. The cluster is faked; internal/gamedb tests the real one.
var testPool *pgxpool.Pool

// TestMain opens the pool through storagetest, which refuses any database that
// is not a test database, since these tests commit fixtures.
func TestMain(m *testing.M) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := storagetest.OpenCore(ctx, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot use the core test database: %v\n", err)
		os.Exit(1)
	}
	if pool == nil {
		os.Exit(m.Run())
	}
	testPool = pool

	// No game row that existed before the run may change status by the end:
	// a test must not damage rows it did not create.
	before := gameRowStatuses(ctx)

	code := m.Run()

	if damaged := statusesChangedSince(before); len(damaged) > 0 {
		fmt.Fprintf(os.Stderr,
			"these tests changed the status of %d database row(s) they did not create:\n", len(damaged))
		for _, line := range damaged {
			fmt.Fprintf(os.Stderr, "  %s\n", line)
		}
		fmt.Fprintln(os.Stderr,
			"a row marked 'dropped' over a database that is still on the game cluster is never "+
				"reclaimed again (Reclaimable skips it), so this is real damage to the installation — "+
				"see withRollback below")
		code = 1
	}

	pool.Close()
	os.Exit(code)
}

// settleFor is how old a row must be to count as pre-existing. Other packages
// share the test database and legitimately change rows they just created.
const settleFor = 10 * time.Second

// gameRowStatuses reads the status of every settled game row in the whole
// database (instances by db_name, templates by template_db), not only rows a
// test made, because the damage it catches is to other rows.
func gameRowStatuses(ctx context.Context) map[string]string {
	rows, err := testPool.Query(ctx, `
		SELECT db_name, status FROM game_instances WHERE created_at < $1
		UNION ALL
		SELECT template_db, status FROM game_templates WHERE updated_at < $1`,
		time.Now().Add(-settleFor))
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot read the installation's game rows: %v\n", err)
		os.Exit(1)
	}
	defer rows.Close()

	found := map[string]string{}
	for rows.Next() {
		var name, status string
		if err := rows.Scan(&name, &status); err != nil {
			fmt.Fprintf(os.Stderr, "cannot read the installation's game rows: %v\n", err)
			os.Exit(1)
		}
		found[name] = status
	}
	if err := rows.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "cannot read the installation's game rows: %v\n", err)
		os.Exit(1)
	}
	return found
}

// statusesChangedSince names every row of before whose status has moved. A
// row that disappeared is ordinary cleanup and is not reported.
func statusesChangedSince(before map[string]string) []string {
	if len(before) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	after := gameRowStatuses(ctx)

	var damaged []string
	for name, was := range before {
		if now, still := after[name]; still && now != was {
			damaged = append(damaged, fmt.Sprintf("%s: %s -> %s", name, was, now))
		}
	}
	sort.Strings(damaged)
	return damaged
}

// cluster records what it was asked to do, and can be told to refuse.
type cluster struct {
	mu      sync.Mutex
	made    []string
	dropped []string
	fail    error

	// creating and peak make the worker bound observable.
	creating, peak int
	slow           time.Duration

	// busy makes DropIdle answer "not dropped, no error" (a live connection);
	// failIdleDrop makes it answer a real error.
	busy         map[string]bool
	failIdleDrop map[string]error
	idleDropped  []string

	// sized and sizeCalls record DatabaseSizes calls; sizesFail makes it fail.
	sized     []string
	sizeCalls int
	sizesFail error

	// dropFail makes the forcing Drop refuse.
	dropFail error

	// idleCalls records every DropIdle call and its outcome. Reclaim sweeps
	// the whole database, so a test asks about its own row (outcomeOf) rather
	// than trusting aggregate counts.
	idleCalls []idleCall

	// templateBytes, clusterBytes and clusterBytesFail stage the byte budget's
	// inputs; clusterReads counts ClusterBytes calls.
	templateBytes    int64
	clusterBytes     int64
	clusterBytesFail error
	clusterReads     int
	// templateReads counts DatabaseSize calls, which are expensive on a real
	// cluster, so tests assert how often they happen.
	templateReads int
}

type idleCall struct {
	name    string
	dropped bool
	err     error
}

func (c *cluster) CreateInstance(_ context.Context, _, instance string, _ sqlpolicy.Policy) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.fail != nil {
		return c.fail
	}

	c.creating++
	if c.creating > c.peak {
		c.peak = c.creating
	}
	slow := c.slow
	c.mu.Unlock()

	// Held open so concurrent creations overlap.
	time.Sleep(slow)

	c.mu.Lock()
	c.creating--
	c.made = append(c.made, instance)
	return nil
}

func (c *cluster) highWater() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.peak
}

func (c *cluster) DatabaseSize(context.Context, string) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.templateReads++
	if c.templateBytes > 0 {
		return c.templateBytes, nil
	}
	return 1 << 20, nil
}

func (c *cluster) templateSizeReads() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.templateReads
}

func (c *cluster) clusterByteReads() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.clusterReads
}

func (c *cluster) setTemplateBytes(size int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.templateBytes = size
}

func (c *cluster) fillTo(used int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.clusterBytes = used
}

func (c *cluster) ClusterBytes(context.Context) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.clusterReads++
	if c.clusterBytesFail != nil {
		return 0, c.clusterBytesFail
	}
	return c.clusterBytes, nil
}

func (c *cluster) DatabaseSizes(_ context.Context, names []string) (map[string]int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.sized = append(c.sized, names...)
	c.sizeCalls++
	if c.sizesFail != nil {
		return nil, c.sizesFail
	}
	sizes := make(map[string]int64, len(names))
	for _, name := range names {
		sizes[name] = 1 << 20
	}
	return sizes, nil
}

func (c *cluster) sizeReads() (names []string, calls int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.sized...), c.sizeCalls
}

func (c *cluster) Drop(_ context.Context, name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.dropFail != nil {
		return c.dropFail
	}
	c.dropped = append(c.dropped, name)
	return nil
}

// droppedByForce reports whether the forcing Drop, not DropIdle, removed name.
func droppedByForce(c *cluster, name string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, dropped := range c.dropped {
		if dropped == name {
			return true
		}
	}
	return false
}

func (c *cluster) counts() (made, dropped int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.made), len(c.dropped)
}

// DropIdle records successes in idleDropped, apart from dropped, so Reclaim's
// drops can be told from Invalidate's.
func (c *cluster) DropIdle(_ context.Context, name string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err, ok := c.failIdleDrop[name]; ok {
		c.idleCalls = append(c.idleCalls, idleCall{name: name, err: err})
		return false, err
	}
	if c.busy[name] {
		c.idleCalls = append(c.idleCalls, idleCall{name: name})
		return false, nil
	}
	c.idleDropped = append(c.idleDropped, name)
	c.idleCalls = append(c.idleCalls, idleCall{name: name, dropped: true})
	return true, nil
}

// outcomeOf returns DropIdle's last answer about name, and whether it was
// asked at all.
func (c *cluster) outcomeOf(name string) (dropped bool, err error, called bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := len(c.idleCalls) - 1; i >= 0; i-- {
		if c.idleCalls[i].name == name {
			return c.idleCalls[i].dropped, c.idleCalls[i].err, true
		}
	}
	return false, nil, false
}

func (c *cluster) idleDrops() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.idleDropped...)
}

func (c *cluster) markBusy(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.busy == nil {
		c.busy = map[string]bool{}
	}
	c.busy[name] = true
}

func (c *cluster) failIdleDropOf(name string, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failIdleDrop == nil {
		c.failIdleDrop = map[string]error{}
	}
	c.failIdleDrop[name] = err
}

var errRollback = errors.New("rolling back the test transaction")

// withRollback runs body inside a core-database transaction that is always
// rolled back, and hands body that transaction's context.
//
// Reclaim sweeps every reclaimable row in the database, not only the test's,
// and a row wrongly marked 'dropped' is never reclaimed again. The service
// joins this transaction through storage.QuerierFrom, so the sweep runs the
// production path (CLAUDE.md rule 10) while none of its writes survive.
func withRollback(t *testing.T, body func(ctx context.Context)) {
	t.Helper()
	if testPool == nil {
		t.Skip("CORE_DB_DSN is not set; run `make test-db`")
	}

	err := storage.NewUnitOfWork(testPool).Do(context.Background(), func(ctx context.Context) error {
		body(ctx)
		return errRollback
	})
	if err != nil && !errors.Is(err, errRollback) {
		t.Fatalf("test transaction failed: %v", err)
	}
}

// contestFor sets up a contest and the given number of registrations, removed
// again when the test ends. It writes through storage.QuerierFrom, so inside
// withRollback the fixture shares the test's transaction.
func contestFor(t *testing.T, ctx context.Context, registrations int) (provisioning.Contest, []uuid.UUID) {
	t.Helper()

	if testPool == nil {
		t.Skip("CORE_DB_DSN is not set; run `make test-db`")
	}
	q := storage.QuerierFrom(ctx, testPool)

	var author uuid.UUID
	err := q.QueryRow(ctx,
		`INSERT INTO users (login, full_name, password_hash) VALUES ($1, 'Author', 'x')
		 RETURNING id`, "prov-author-"+uuid.NewString()[:8]).Scan(&author)
	if err != nil {
		t.Fatalf("create author: %v", err)
	}

	var id uuid.UUID
	if err := q.QueryRow(ctx,
		`INSERT INTO contests (created_by) VALUES ($1) RETURNING id`, author).Scan(&id); err != nil {
		t.Fatalf("create contest: %v", err)
	}
	// Only does anything outside withRollback.
	t.Cleanup(func() {
		clean, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, _ = testPool.Exec(clean, `DELETE FROM contests WHERE id = $1`, id)
		_, _ = testPool.Exec(clean, `DELETE FROM users WHERE id = $1`, author)
	})

	var people []uuid.UUID
	for range registrations {
		var user, registration uuid.UUID
		if err := q.QueryRow(ctx,
			`INSERT INTO users (login, full_name, password_hash) VALUES ($1, 'Player', 'x')
			 RETURNING id`, "prov-player-"+uuid.NewString()[:8]).Scan(&user); err != nil {
			t.Fatalf("create player: %v", err)
		}
		if err := q.QueryRow(ctx,
			`INSERT INTO registrations (contest_id, user_id) VALUES ($1, $2) RETURNING id`,
			id, user).Scan(&registration); err != nil {
			t.Fatalf("create registration: %v", err)
		}
		people = append(people, registration)
	}

	return provisioning.Contest{
		ID: id, Template: "game_tpl_test", Version: 1, Policy: sqlpolicy.ReadOnly(),
	}, people
}

// markTemplateReady stores the 'ready' template row a successful build would
// leave. It is removed by contestFor's cleanup through the cascade.
func markTemplateReady(t *testing.T, ctx context.Context, contest uuid.UUID, database string) {
	t.Helper()
	if testPool == nil {
		t.Skip("CORE_DB_DSN is not set; run `make test-db`")
	}
	if _, err := storage.QuerierFrom(ctx, testPool).Exec(ctx,
		`INSERT INTO game_templates (contest_id, template_db, init_script, status, version)
		 VALUES ($1, $2, 'SELECT 1', 'ready', 1)`, contest, database); err != nil {
		t.Fatalf("create template: %v", err)
	}
}

// templateStore is the game's row in memory, so a test controls its starting
// state and reads what the service left behind.
type templateStore struct {
	mu       sync.Mutex
	template provisioning.Template
	present  bool
	policy   sqlpolicy.Policy
	claims   int
	claimErr error
	// templateErr and policyErr, when set, are storage failures returned by
	// Template and Policy.
	templateErr error
	policyErr   error
	finished    []finish
	uploads     map[uuid.UUID]provisioning.Upload
	tableData   map[uuid.UUID]provisioning.TableData
	// deleteRowErr, when set, is what DeleteTableDataRow answers. The fake
	// does not reimplement the database's CHECK on deleted_rows; the postgres
	// tests prove that one.
	deleteRowErr error
	// tableDataReads, when armed, holds ReadyTableData callers until the
	// expected number arrive, making two simultaneous "add row" requests
	// deterministic.
	tableDataReads *arrivalGate
}

// arrivalGate releases every caller at once, as soon as the expected number
// of them have arrived. A caller arriving after that passes straight through,
// so a gate armed for one part of a test does not hold up the rest of it.
type arrivalGate struct {
	mu        sync.Mutex
	remaining int
	open      chan struct{}
}

func (g *arrivalGate) arrive() {
	g.mu.Lock()
	if g.remaining > 0 {
		g.remaining--
		if g.remaining == 0 {
			close(g.open)
		}
	}
	g.mu.Unlock()
	<-g.open
}

func (s *templateStore) gateTableDataReads(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tableDataReads = &arrivalGate{remaining: n, open: make(chan struct{})}
}

// directly is a unit of work with no transaction behind it.
type directly struct{}

func (directly) Do(ctx context.Context, fn func(context.Context) error) error { return fn(ctx) }

type finish struct {
	version int
	err     string
}

func (s *templateStore) SaveScript(_ context.Context, contestID uuid.UUID, database, script string) (provisioning.Template, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	version := 1
	if s.present {
		version = s.template.Version + 1
	}
	s.template = provisioning.Template{
		ContestID: contestID, Database: database, Version: version,
		Status: provisioning.TemplatePending, Source: provisioning.SourceEditor, Script: script,
	}
	s.present = true
	return s.template, nil
}

func (s *templateStore) SaveDefinition(
	_ context.Context, contestID uuid.UUID, database string, definition provisioning.Definition,
) (provisioning.Template, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	version := 1
	if s.present {
		version = s.template.Version + 1
	}
	s.template = provisioning.Template{
		ContestID: contestID, Database: database, Version: version,
		Status: provisioning.TemplatePending, Source: provisioning.SourceBuilder, Definition: definition,
	}
	s.present = true
	return s.template, nil
}

func (s *templateStore) Template(context.Context, uuid.UUID) (provisioning.Template, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.templateErr != nil {
		return provisioning.Template{}, s.templateErr
	}
	if !s.present {
		return provisioning.Template{}, provisioning.ErrNoGame
	}
	return s.template, nil
}

// TemplateStatus matches the real query: the script's length in place of the
// script, and no definition.
func (s *templateStore) TemplateStatus(ctx context.Context, contestID uuid.UUID) (provisioning.Template, error) {
	template, err := s.Template(ctx, contestID)
	if err != nil {
		return provisioning.Template{}, err
	}
	template.ScriptBytes = len(template.Script)
	template.Script, template.Definition = "", provisioning.Definition{}
	return template, nil
}

func (s *templateStore) ClaimBuild(context.Context, time.Duration) (provisioning.Template, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.claimErr != nil {
		return provisioning.Template{}, s.claimErr
	}
	s.claims++
	s.template.UpdatedAt = time.Now()
	claimed := s.template
	claimed.Status = provisioning.TemplateBuilding
	return claimed, nil
}

func (s *templateStore) FinishBuild(
	_ context.Context, _ uuid.UUID, version int, buildError string, claimedAt time.Time,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finished = append(s.finished, finish{version: version, err: buildError})
	// Mirrors the real repository: only a successful build clears the mark,
	// and only when nothing moved it past the claim.
	if buildError == "" && s.template.DataChangedAt != nil && !s.template.DataChangedAt.After(claimedAt) {
		s.template.DataChangedAt = nil
	}
	return nil
}

// markBuilt puts the template where a successful build leaves it.
func markBuilt(store *templateStore) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.template.Status = provisioning.TemplateReady
}

func (s *templateStore) MarkTableDataChanged(_ context.Context, _ uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	at := time.Now()
	s.template.DataChangedAt = &at
	return nil
}

func (s *templateStore) RequestBuild(_ context.Context, _ uuid.UUID) (provisioning.Template, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.present {
		return provisioning.Template{}, provisioning.ErrNoGame
	}
	if s.template.Status != provisioning.TemplateReady && s.template.Status != provisioning.TemplateFailed {
		return provisioning.Template{}, provisioning.ErrBuildInProgress
	}
	s.template.Status = provisioning.TemplatePending
	s.template.Version++
	s.template.BuildError = ""
	return s.template, nil
}

func (s *templateStore) Policy(context.Context, uuid.UUID) (sqlpolicy.Policy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.policyErr != nil {
		return sqlpolicy.Policy{}, s.policyErr
	}
	return s.policy, nil
}

func (s *templateStore) BeginUpload(_ context.Context, id, contestID uuid.UUID, filename string, declaredBytes int64) (provisioning.Upload, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, u := range s.uploads {
		if u.ContestID == contestID && u.Status == provisioning.UploadReceiving {
			return provisioning.Upload{}, provisioning.ErrUploadInProgress
		}
	}
	if s.uploads == nil {
		s.uploads = map[uuid.UUID]provisioning.Upload{}
	}
	u := provisioning.Upload{
		ID: id, ContestID: contestID, Filename: filename, DeclaredBytes: declaredBytes,
		Status: provisioning.UploadReceiving,
	}
	s.uploads[id] = u
	return u, nil
}

func (s *templateStore) Upload(_ context.Context, id uuid.UUID) (provisioning.Upload, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.uploads[id]
	if !ok {
		return provisioning.Upload{}, provisioning.ErrUploadNotFound
	}
	return u, nil
}

func (s *templateStore) CurrentUpload(_ context.Context, contestID uuid.UUID) (provisioning.Upload, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, u := range s.uploads {
		if u.ContestID == contestID && u.Status == provisioning.UploadReceiving {
			return u, nil
		}
	}
	return provisioning.Upload{}, provisioning.ErrUploadNotFound
}

func (s *templateStore) UpdateReceived(_ context.Context, id uuid.UUID, receivedBytes int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.uploads[id]
	if !ok {
		return provisioning.ErrUploadNotFound
	}
	u.ReceivedBytes = receivedBytes
	s.uploads[id] = u
	return nil
}

func (s *templateStore) CompleteUpload(
	_ context.Context, contestID, id uuid.UUID, database string, summary provisioning.UploadSummary, previous *uuid.UUID,
) (provisioning.Template, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.uploads[id]
	if !ok {
		return provisioning.Template{}, provisioning.ErrUploadNotFound
	}
	u.Status, u.SHA256, u.Lines, u.ReceivedBytes = provisioning.UploadComplete, summary.SHA256, summary.Lines, summary.Bytes
	s.uploads[id] = u
	if previous != nil {
		if p, ok := s.uploads[*previous]; ok {
			p.Status = provisioning.UploadAborted
			s.uploads[*previous] = p
		}
	}

	version := 1
	if s.present {
		version = s.template.Version + 1
	}
	s.template = provisioning.Template{
		ContestID: contestID, Database: database, Version: version,
		Status: provisioning.TemplatePending, Source: provisioning.SourceFile, UploadID: &id,
	}
	s.present = true
	return s.template, nil
}

func (s *templateStore) AbortUpload(_ context.Context, id uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.uploads[id]
	if !ok {
		return provisioning.ErrUploadNotFound
	}
	u.Status = provisioning.UploadAborted
	s.uploads[id] = u
	return nil
}

func (s *templateStore) AbandonedUploads(_ context.Context, cutoff time.Time, limit int) ([]provisioning.Upload, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []provisioning.Upload
	for _, u := range s.uploads {
		if u.Status == provisioning.UploadReceiving && u.UpdatedAt.Before(cutoff) {
			out = append(out, u)
			if len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

func (s *templateStore) UploadInUse(_ context.Context, id uuid.UUID) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.uploads[id]
	if !ok {
		return false, nil
	}
	if u.Status == provisioning.UploadReceiving {
		return true, nil
	}
	return s.present && s.template.UploadID != nil && *s.template.UploadID == id, nil
}

func (s *templateStore) BeginTableData(_ context.Context, id, contestID uuid.UUID, table string, declaredBytes int64) (provisioning.TableData, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.tableData {
		if d.ContestID == contestID && strings.EqualFold(d.Table, table) && d.Status == provisioning.TableDataReceiving {
			return provisioning.TableData{}, provisioning.ErrTableDataInProgress
		}
	}
	if s.tableData == nil {
		s.tableData = map[uuid.UUID]provisioning.TableData{}
	}
	// Stamped like the column's DEFAULT now(): a zero time would make every
	// row look abandoned to the janitor.
	d := provisioning.TableData{
		ID: id, ContestID: contestID, Table: table, DeclaredBytes: declaredBytes,
		Status: provisioning.TableDataReceiving, UpdatedAt: time.Now(),
	}
	s.tableData[id] = d
	return d, nil
}

// ageTableData moves one row's updated_at back past a janitor cutoff.
func (s *templateStore) ageTableData(id uuid.UUID, by time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.tableData[id]
	d.UpdatedAt = time.Now().Add(-by)
	s.tableData[id] = d
}

func (s *templateStore) TableDataByID(_ context.Context, id uuid.UUID) (provisioning.TableData, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.tableData[id]
	if !ok {
		return provisioning.TableData{}, provisioning.ErrTableDataNotFound
	}
	return d, nil
}

func (s *templateStore) CurrentTableData(_ context.Context, contestID uuid.UUID, table string) (provisioning.TableData, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.tableData {
		if d.ContestID == contestID && strings.EqualFold(d.Table, table) && d.Status == provisioning.TableDataReceiving {
			return d, nil
		}
	}
	return provisioning.TableData{}, provisioning.ErrTableDataNotFound
}

func (s *templateStore) ReadyTableData(_ context.Context, contestID uuid.UUID, table string) (provisioning.TableData, error) {
	s.mu.Lock()
	gate := s.tableDataReads
	found, err := provisioning.TableData{}, error(provisioning.ErrTableDataNotFound)
	for _, d := range s.tableData {
		if d.ContestID == contestID && strings.EqualFold(d.Table, table) && d.Status == provisioning.TableDataComplete {
			found, err = d, nil
			break
		}
	}
	s.mu.Unlock()

	// Released outside the lock, after the answer is taken, so every held
	// caller leaves with the same snapshot.
	if gate != nil {
		gate.arrive()
	}
	return found, err
}

func (s *templateStore) UpdateTableDataReceived(_ context.Context, id uuid.UUID, receivedBytes int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.tableData[id]
	if !ok {
		return provisioning.ErrTableDataNotFound
	}
	d.ReceivedBytes = receivedBytes
	s.tableData[id] = d
	return nil
}

func (s *templateStore) CompleteTableData(
	_ context.Context, contestID uuid.UUID, table string, id uuid.UUID, receivedBytes, lines int64, previous *uuid.UUID,
) (provisioning.TableData, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.tableData[id]
	if !ok {
		return provisioning.TableData{}, provisioning.ErrTableDataNotFound
	}
	d.Status, d.ReceivedBytes, d.Lines = provisioning.TableDataComplete, receivedBytes, lines
	s.tableData[id] = d
	if previous != nil {
		if p, ok := s.tableData[*previous]; ok {
			p.Status = provisioning.TableDataAborted
			s.tableData[*previous] = p
		}
	}
	return d, nil
}

func (s *templateStore) CreateReadyTableData(
	_ context.Context, id, contestID uuid.UUID, table string, bytes, lines int64,
) (provisioning.TableData, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.tableData {
		if d.ContestID == contestID && strings.EqualFold(d.Table, table) && d.Status == provisioning.TableDataComplete {
			return provisioning.TableData{}, provisioning.ErrTableDataInProgress
		}
	}
	if s.tableData == nil {
		s.tableData = map[uuid.UUID]provisioning.TableData{}
	}
	d := provisioning.TableData{
		ID: id, ContestID: contestID, Table: table, DeclaredBytes: bytes, ReceivedBytes: bytes, Lines: lines,
		Status: provisioning.TableDataComplete,
	}
	s.tableData[id] = d
	return d, nil
}

func (s *templateStore) AppendTableDataRow(_ context.Context, id uuid.UUID, receivedBytes, lines int64) (provisioning.TableData, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.tableData[id]
	if !ok || d.Status != provisioning.TableDataComplete {
		return provisioning.TableData{}, provisioning.ErrTableDataChanged
	}
	// GREATEST, as in the real statement: neither figure goes backwards.
	d.ReceivedBytes, d.Lines = max(d.ReceivedBytes, receivedBytes), max(d.Lines, lines)
	s.tableData[id] = d
	return d, nil
}

func (s *templateStore) AbortTableData(_ context.Context, id uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.tableData[id]
	if !ok {
		return provisioning.ErrTableDataNotFound
	}
	d.Status = provisioning.TableDataAborted
	s.tableData[id] = d
	return nil
}

func (s *templateStore) DiscardTableData(_ context.Context, contestID uuid.UUID) ([]uuid.UUID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ids []uuid.UUID
	for id, d := range s.tableData {
		if d.ContestID != contestID {
			continue
		}
		if d.Status != provisioning.TableDataReceiving && d.Status != provisioning.TableDataComplete {
			continue
		}
		d.Status = provisioning.TableDataAborted
		s.tableData[id] = d
		ids = append(ids, id)
	}
	return ids, nil
}

func (s *templateStore) DeleteTableDataRow(_ context.Context, id uuid.UUID, row int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.tableData[id]
	if !ok {
		return provisioning.ErrTableDataNotFound
	}
	if s.deleteRowErr != nil {
		return s.deleteRowErr
	}
	for _, r := range d.DeletedRows {
		if r == row {
			return provisioning.ErrTableRowAlreadyDeleted
		}
	}
	d.DeletedRows = append(append([]int64{}, d.DeletedRows...), row)
	s.tableData[id] = d
	return nil
}

func (s *templateStore) AbandonedTableData(_ context.Context, cutoff time.Time, limit int) ([]provisioning.TableData, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []provisioning.TableData
	for _, d := range s.tableData {
		if d.Status == provisioning.TableDataReceiving && d.UpdatedAt.Before(cutoff) {
			out = append(out, d)
			if len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

func (s *templateStore) TableDataInUse(_ context.Context, id uuid.UUID) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.tableData[id]
	if !ok {
		return false, nil
	}
	return d.Status == provisioning.TableDataReceiving || d.Status == provisioning.TableDataComplete, nil
}

// buildCluster records what it was asked to build and can be told to refuse.
type buildCluster struct {
	mu      sync.Mutex
	names   []string
	scripts []string
	policy  sqlpolicy.Policy
	fail    error
	// tableData records LoadTableData calls keyed "database.table";
	// tableDataFail makes it fail.
	tableData     map[string]string
	tableDataFail error
	dropped       []string
}

func (c *buildCluster) BuildTemplate(_ context.Context, name string, script io.Reader, policy sqlpolicy.Policy) error {
	// Consumed in full, as a real build would, to record the bytes that
	// reached it.
	data, err := io.ReadAll(script)
	if err != nil {
		return fmt.Errorf("buildCluster: read script: %w", err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.names, c.scripts, c.policy = append(c.names, name), append(c.scripts, string(data)), policy
	return c.fail
}

func (c *buildCluster) LoadTableData(_ context.Context, database, table string, columns []string, data io.Reader) error {
	body, err := io.ReadAll(data)
	if err != nil {
		return fmt.Errorf("buildCluster: read table data: %w", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tableDataFail != nil {
		return c.tableDataFail
	}
	if c.tableData == nil {
		c.tableData = map[string]string{}
	}
	c.tableData[database+"."+table] = string(body)
	return nil
}

func (c *buildCluster) Drop(_ context.Context, name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.dropped = append(c.dropped, name)
	return nil
}

type authoring struct {
	editable bool
	err      error
}

func (a authoring) GameEditable(context.Context, uuid.UUID) (bool, error) { return a.editable, a.err }

func games(editable bool) (*provisioning.Games, *templateStore, *buildCluster) {
	store := &templateStore{policy: sqlpolicy.ReadOnly()}
	cluster := &buildCluster{}
	return provisioning.NewGames(store, cluster, authoring{editable: editable}), store, cluster
}
