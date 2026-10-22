package provisioning

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// templateSizes caches each template's measured size per template version.
//
// pg_database_size walks the database directory (hundreds of files), and
// Service.Quota needs it on every query of a read-write contest, over the
// maintenance pool that CREATE DATABASE and DROP DATABASE hold for minutes.
// A template is immutable once built and a rebuild bumps its version, so the
// version is the whole invalidation rule (CLAUDE.md rule 6).
type templateSizes struct {
	mu sync.Mutex
	// byTemplate is keyed by template database name, the thing measured.
	byTemplate map[string]templateSize
}

type templateSize struct {
	version int
	bytes   int64
}

// maxTemplateSizesTracked bounds the map, whose keys come from stored data.
// Real use is one entry per live contest. Past it the whole map is cleared:
// the cost is one extra measurement per template.
const maxTemplateSizesTracked = 512

func (c *templateSizes) get(template string, version int) (int64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	recorded, ok := c.byTemplate[template]
	if !ok || recorded.version != version {
		return 0, false
	}
	return recorded.bytes, true
}

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

// forget drops whatever is recorded for template. The version already
// invalidates; this only avoids holding a measurement of a template that
// Invalidate may have just made obsolete.
func (c *templateSizes) forget(template string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.byTemplate, template)
}

// templateBytes is the size of one copy of this contest, measured at most
// once per template version. Callers in this package use it instead of
// Cluster.DatabaseSize.
func (s *Service) templateBytes(ctx context.Context, contest Contest) (int64, error) {
	if size, ok := s.sizes.get(contest.Template, contest.Version); ok {
		return size, nil
	}
	size, err := s.cluster.DatabaseSize(ctx, contest.Template)
	if err != nil {
		return 0, err
	}
	// A non-positive size means the template is gone; it is not cached, and
	// the caller decides what that means.
	if size > 0 {
		s.sizes.put(contest.Template, contest.Version, size)
	}
	return size, nil
}

// clusterBytesTTL is how long one measurement of the whole cluster serves
// every caller. ClusterBytes stats every file of every database (hundreds at
// contest start), and roomForOneCopy asks on every participant's own
// pool-miss request, concurrently, exactly then. Ten seconds turns a burst of
// latecomers into one scan while a drop or an outside writer is still seen
// within the time a copy itself takes.
const clusterBytesTTL = 10 * time.Second

// clusterUsage is the cluster's measured size plus the copies this process
// has made since, so the budget cannot be overshot between measurements by
// the copies that were made in the meantime. Replicas do not see each
// other's copies; the TTL bounds that error.
type clusterUsage struct {
	// mu is held across the measurement on purpose: concurrent callers wait
	// for the one scan in flight rather than each starting their own, which
	// is the burst this cache exists for.
	mu         sync.Mutex
	measured   int64
	measuredAt time.Time
	// added is the bytes of copies made since measuredAt.
	added int64
	valid bool
}

// clusterBytes is Cluster.ClusterBytes served from clusterUsage: measured at
// most once per clusterBytesTTL, plus the copies made since. Callers in this
// package use it instead of Cluster.ClusterBytes.
func (s *Service) clusterBytes(ctx context.Context) (int64, error) {
	s.usage.mu.Lock()
	defer s.usage.mu.Unlock()
	now := s.now()
	if s.usage.valid && now.Sub(s.usage.measuredAt) < clusterBytesTTL {
		return s.usage.measured + s.usage.added, nil
	}
	used, err := s.cluster.ClusterBytes(ctx)
	if err != nil {
		return 0, err
	}
	s.usage.measured, s.usage.measuredAt, s.usage.added, s.usage.valid = used, now, 0, true
	return used, nil
}

// noteCopyMade adds one copy of the contest's template to the cached
// measurement. Called after every copy that grows the cluster, never after a
// rebuild, which dropped the old copy first. Without the template's size at
// hand the measurement is discarded instead, so the next reader measures
// again rather than undercount.
func (s *Service) noteCopyMade(contest Contest) {
	s.usage.mu.Lock()
	defer s.usage.mu.Unlock()
	if !s.usage.valid {
		return
	}
	size, ok := s.sizes.get(contest.Template, contest.Version)
	if !ok {
		s.usage.valid = false
		return
	}
	s.usage.added += size
}

// roomForOneCopy refuses when one more copy of this contest's template would
// take the game cluster past the deployment's budget. It is copiesThatFit's
// check applied where a participant gets a database, so late registrations
// cannot bypass the budget the pool respects.
//
// Rebuilds (rebuildExisting, Reset) skip it: the old copy is dropped first, so
// the cluster does not grow. A zero budget refuses nothing.
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
	used, err := s.clusterBytes(ctx)
	if err != nil {
		return fmt.Errorf("check the game cluster's room for contest %s: %w", contest.ID, err)
	}
	if used+size > s.maxClusterBytes {
		return fmt.Errorf("%w: the cluster holds %d bytes of a %d byte budget and one more copy of this contest is %d",
			ErrClusterFull, used, s.maxClusterBytes, size)
	}
	return nil
}
