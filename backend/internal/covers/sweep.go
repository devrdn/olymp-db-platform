package covers

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"time"
)

// OrphanGrace is how long a file on the volume is left alone whatever the
// database says about it.
//
// Upload writes the renditions before it commits the row, because a row
// pointing at a file that was never written is a broken cover on the front
// page while a file no row points at is only work for this sweep. The cost of
// that order is a window in which a perfectly good picture is referred to by
// nothing, and a sweep that ran inside the window would delete the cover an
// organiser had just chosen. An hour is far longer than that window can
// plausibly be and far shorter than the interval anyone runs this at.
const OrphanGrace = time.Hour

// OrphanFiles is the volume as this sweep is allowed to see it.
//
// Declared here, by the consumer, and satisfied structurally by
// internal/platform/filestore: the domain names what it needs and neither
// package imports the other (CLAUDE.md, Go layout rules 3 and 7). Narrower
// than it might be in one way that matters — there is no Get. This job
// decides what to remove from a file's name and its age, never from its
// contents, so it cannot be made to read a picture at all.
type OrphanFiles interface {
	// List names every file on the volume that the store would serve, with
	// its size and the time it was last written.
	List(ctx context.Context) ([]fs.FileInfo, error)
	// Delete removes one file by key.
	Delete(ctx context.Context, key string) error
}

// OrphanHashes is the core database's account of which pictures are still
// spoken for: every hash any contest_covers row names, in one read.
//
// Every hash, not this contest's: a file is named by the hash of its content,
// so two contests that uploaded the same picture share one file, and removing
// a cover from one of them must not take the other's picture away. The
// question this sweep asks is whether any row at all names the hash.
type OrphanHashes interface {
	ReferencedHashes(ctx context.Context) ([]string, error)
}

// OrphanFile is one rendition on the volume that nothing refers to any more.
type OrphanFile struct {
	// Key is the file's name, as Key built it.
	Key string
	// Hash is the picture it is a rendition of, which is what made it an
	// orphan: no row names it.
	Hash string
	// SizeBytes is what it holds — the reason to remove it, and the number an
	// operator weighs before saying yes.
	SizeBytes int64
	// ModTime is when it was written. Printed with the plan because the rule
	// that keeps a recent file is otherwise invisible to whoever approves one.
	ModTime time.Time
}

// OrphanSweepResult is one removal pass's outcome.
type OrphanSweepResult struct {
	// Removed counts files actually gone from the volume; Failed counts the
	// ones the volume refused.
	Removed, Failed int
	// FreedBytes is what the removed files held, as measured when the plan
	// was drawn up.
	FreedBytes int64
}

// OrphanSweeper finds — and, when told to, removes — cover files that no
// contest refers to any more.
//
// Nothing in the product will ever remove one. Replacing a cover rewrites the
// row and removing one deletes it; in both cases the files stay, deliberately,
// so that a volume refusing a delete cannot fail an organiser's edit (design
// spec §4). What that leaves is growth with no bound on a volume that is also
// part of the backup, and this is what collects it.
//
// It is an operator's job, run by hand from cmd/gameorphans alongside the
// sweep for orphaned game databases, and deliberately not something the API
// offers: deciding that a file is safe to destroy needs somebody who can look
// at the volume.
type OrphanSweeper struct {
	files OrphanFiles
	rows  OrphanHashes
	// now is the clock, so that the grace period is testable without a test
	// that waits an hour. Nothing outside this package sets it.
	now func() time.Time
}

// NewOrphanSweeper assembles the sweep.
func NewOrphanSweeper(files OrphanFiles, rows OrphanHashes) *OrphanSweeper {
	return &OrphanSweeper{files: files, rows: rows, now: time.Now}
}

// Find draws up the plan: every file on the volume that no row names and that
// is old enough to judge.
//
// The rows are read first, and a failure to read them stops the sweep rather
// than being carried past. That order is the whole safety of this job: the
// set of referenced hashes is what keeps files, so an empty set through an
// error rather than through an empty table is a plan to delete every cover in
// the installation.
//
// A name this service never wrote is not a candidate at all, however old it
// is — the same rule the game-database sweep keeps about a database the core
// database has never heard of. An operator's own file in the directory, or a
// rendition written under a naming scheme this code no longer knows, is left
// for a person to look at.
//
// The result is a plan, not an action. cmd/gameorphans prints it and stops
// unless it was told to go ahead.
func (s *OrphanSweeper) Find(ctx context.Context) ([]OrphanFile, error) {
	referenced, err := s.rows.ReferencedHashes(ctx)
	if err != nil {
		return nil, fmt.Errorf("read the referenced cover hashes: %w", err)
	}
	spokenFor := make(map[string]bool, len(referenced))
	for _, hash := range referenced {
		spokenFor[strings.ToLower(hash)] = true
	}

	files, err := s.files.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list the cover volume: %w", err)
	}

	cutoff := s.now().Add(-OrphanGrace)
	var orphans []OrphanFile
	for _, file := range files {
		if file.IsDir() {
			continue
		}
		hash, ok := hashOfKey(file.Name())
		switch {
		case !ok, spokenFor[hash]:
			continue
		case file.ModTime().After(cutoff):
			// Possibly an upload in flight: the bytes are on the volume and
			// the row is not committed yet. See OrphanGrace.
			continue
		}
		orphans = append(orphans, OrphanFile{
			Key: file.Name(), Hash: hash, SizeBytes: file.Size(), ModTime: file.ModTime(),
		})
	}

	// A stable order, so that two runs of the plan are comparable line by
	// line and the list an operator approved is the list they read.
	sort.Slice(orphans, func(i, j int) bool { return orphans[i].Key < orphans[j].Key })
	return orphans, nil
}

// Remove deletes the files it is given, one at a time, and reports what
// happened.
//
// It takes the plan rather than making one, so that what an operator approved
// is exactly what runs: drawing the plan up a second time between the printed
// list and the deletion would let a cover uploaded in between become part of
// a list nobody read.
//
// One refusal does not stop the rest and every one of them is counted and
// returned. A volume that has gone read-only refuses all of them, and a sweep
// that reported success over it would be run again next month to report the
// same thing.
func (s *OrphanSweeper) Remove(ctx context.Context, orphans []OrphanFile) (OrphanSweepResult, error) {
	var result OrphanSweepResult
	var failures []error
	for _, orphan := range orphans {
		if err := ctx.Err(); err != nil {
			return result, errors.Join(append(failures, err)...)
		}
		if err := s.files.Delete(ctx, orphan.Key); err != nil {
			result.Failed++
			failures = append(failures, fmt.Errorf("remove %s: %w", orphan.Key, err))
			continue
		}
		result.Removed++
		result.FreedBytes += orphan.SizeBytes
	}
	return result, errors.Join(failures...)
}

// hashOfKey reads back what Key wrote: the hash of a rendition's own file
// name, and whether the name is one this service writes at all.
//
// It accepts any width rather than only the ones in Sizes. A rendition stored
// under a width this code no longer produces is exactly the kind of file this
// sweep exists to collect, and a check against the current list would make
// changing that list a way to strand files forever.
func hashOfKey(key string) (string, bool) {
	if path.Ext(key) != ".jpg" {
		return "", false
	}
	hash, width, found := strings.Cut(strings.TrimSuffix(key, ".jpg"), "-")
	if !found || width == "" || !validHash(hash) {
		return "", false
	}
	for _, r := range width {
		if r < '0' || r > '9' {
			return "", false
		}
	}
	return hash, true
}
