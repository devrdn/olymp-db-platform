package provisioning

import (
	"context"
	"fmt"
	"sync"
)

// templateSizes remembers how large a contest's template database measured,
// against the template version that measurement belongs to.
//
// The measurement is `SELECT pg_database_size(...)`, and PostgreSQL answers it
// by walking the database's own directory: an empty PostgreSQL 15 catalogue is
// already 299 files, and a game's segments are hundreds more. That is fine once
// per pool tick and ruinous once per participant request — Service.Quota asks
// for it on every query of every read-write contest, which at three hundred
// participants asking once every twenty seconds is fifteen directory walks a
// second, six to nine thousand stat(2) calls, on the same two-core cluster the
// participants' own queries run on. Worse, it is asked over the maintenance
// pool, whose ten connections are at that same moment being held for minutes at
// a time by `CREATE DATABASE … TEMPLATE` and by the reclaim sweep's run of
// `DROP DATABASE`; there is no timeout middleware in front of the router and no
// WriteTimeout on the server, so a saturated pool does not degrade into
// refusals, it degrades into hung goroutines and hung browser tabs.
//
// What makes a cache correct here rather than a guess is that a template is
// immutable once built: the only way its bytes change is a rebuild, and a
// rebuild bumps the version (postgres.upsertGame). So the version is the whole
// invalidation rule — a measurement is reused exactly while the thing measured
// cannot have changed, which is CLAUDE.md rule 6 read literally.
type templateSizes struct {
	mu sync.Mutex
	// byTemplate is keyed by the template database's own name rather than by
	// the contest: the name is what was measured, and it is what DatabaseSize
	// would be asked about again.
	byTemplate map[string]templateSize
}

// templateSize is one measurement and the version it belongs to.
type templateSize struct {
	version int
	bytes   int64
}

// maxTemplateSizesTracked bounds the map. One entry per live contest is what
// this ever holds in a deployment — a few dozen — so the ceiling is not a
// figure anybody meets by running an olympiad; it is here because the key
// comes from a row an organiser writes and an unbounded map fed from stored
// data is a slow leak rather than a cache. Past it the whole map is dropped
// rather than one entry evicted: choosing a victim needs an ordering this
// does not keep, and the cost of being wrong is one extra measurement per
// template, which is exactly what the uncached code did on every request.
const maxTemplateSizesTracked = 512

// get returns the size recorded for template at version, and whether there was
// one.
func (c *templateSizes) get(template string, version int) (int64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	recorded, ok := c.byTemplate[template]
	if !ok || recorded.version != version {
		return 0, false
	}
	return recorded.bytes, true
}

// put records size for template at version.
func (c *templateSizes) put(template string, version int, size int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.byTemplate == nil {
		c.byTemplate = make(map[string]templateSize)
	}
	if len(c.byTemplate) >= maxTemplateSizesTracked {
		c.byTemplate = make(map[string]templateSize)
	}
	c.byTemplate[template] = templateSize{version: version, bytes: size}
}

// forget drops whatever is recorded for template, whichever version it was
// measured at.
//
// Not part of the invalidation rule — the version already is that — but the
// honest answer for the one moment this package knows a template's bytes are
// about to stop describing anything: Invalidate has just dropped databases made
// from it. Nothing breaks without it; it only keeps a measurement from being
// held for a template that may be gone.
func (c *templateSizes) forget(template string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.byTemplate, template)
}

// templateBytes is how large one copy of this contest costs, measured at most
// once per template version.
//
// Every caller in this package goes through here rather than through
// Cluster.DatabaseSize directly, so that "once per version" is a property of
// the service and not of whichever caller remembered.
func (s *Service) templateBytes(ctx context.Context, contest Contest) (int64, error) {
	if size, ok := s.sizes.get(contest.Template, contest.Version); ok {
		return size, nil
	}
	size, err := s.cluster.DatabaseSize(ctx, contest.Template)
	if err != nil {
		return 0, err
	}
	// A non-positive size is never true of a database that exists — an empty
	// one is megabytes — so it is a template that has gone, and recording it
	// would keep answering with a missing measurement until the next rebuild.
	// Reported to the caller, which decides what a missing template means for
	// what it was doing (copiesThatFit refuses; Quota falls back to its floor).
	if size > 0 {
		s.sizes.put(contest.Template, contest.Version, size)
	}
	return size, nil
}

// roomForOneCopy refuses when copying this contest's template once more would
// take the game cluster past the budget the deployment set.
//
// The same arithmetic copiesThatFit does for the pool, applied where a
// participant actually gets a database. Without it the budget bound only the
// background tender: at a three-gigabyte template and the default 64 GiB,
// copiesThatFit stops the pool at about twenty copies and logs why, and every
// participant from the twenty-first on walked straight past it through Ensure's
// late-registration path — two hundred and eighty more copies, eight hundred and
// forty gigabytes, onto a docker volume with no size of its own, until the
// host's filesystem fills and PostgreSQL stops for everybody rather than for the
// latecomers. Disk comes back only from Reclaim, a day after the contest ends.
//
// It is deliberately not applied to a rebuild of a database that already
// exists (Service.rebuildExisting, Service.Reset): CreateInstance drops the old
// copy before making the new one under the same name, so the cluster does not
// grow, and refusing there would take a database away from somebody who already
// had one.
//
// A zero budget means the deployment set none, and this refuses nothing.
func (s *Service) roomForOneCopy(ctx context.Context, contest Contest) error {
	if s.maxClusterBytes <= 0 {
		return nil
	}
	size, err := s.templateBytes(ctx, contest)
	if err != nil {
		return fmt.Errorf("check the game cluster's room for contest %s: %w", contest.ID, err)
	}
	if size <= 0 {
		return fmt.Errorf("check the game cluster's room for contest %s: the template %s measures %d bytes",
			contest.ID, contest.Template, size)
	}
	used, err := s.cluster.ClusterBytes(ctx)
	if err != nil {
		return fmt.Errorf("check the game cluster's room for contest %s: %w", contest.ID, err)
	}
	if used+size > s.maxClusterBytes {
		return fmt.Errorf("%w: the cluster holds %d bytes of a %d byte budget and one more copy of this contest is %d",
			ErrClusterFull, used, s.maxClusterBytes, size)
	}
	return nil
}
