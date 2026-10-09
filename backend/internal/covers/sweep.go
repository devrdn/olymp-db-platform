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
// database says. Upload writes files before it commits the row, so a new
// cover is briefly referenced by nothing; the grace keeps the sweep off it.
const OrphanGrace = time.Hour

// OrphanFiles is the volume as this sweep may see it (CLAUDE.md layout rule
// 3). It has no Get: the sweep decides by name and age, never by contents.
type OrphanFiles interface {
	List(ctx context.Context) ([]fs.FileInfo, error)
	Delete(ctx context.Context, key string) error
}

// OrphanHashes returns every hash any contest_covers row names, in one read.
// Contests that uploaded the same picture share one file, so a file is an
// orphan only when no row at all names its hash.
type OrphanHashes interface {
	ReferencedHashes(ctx context.Context) ([]string, error)
}

type OrphanFile struct {
	Key       string
	Hash      string
	SizeBytes int64
	// ModTime is printed with the plan so the grace rule is visible.
	ModTime time.Time
}

type OrphanSweepResult struct {
	Removed, Failed int
	// FreedBytes is measured when the plan was drawn up.
	FreedBytes int64
}

// OrphanSweeper finds, and when told to removes, cover files no contest refers
// to. The product never deletes a cover file (so a refused delete cannot fail
// an edit); this collects them. It is run by an operator from cmd/gameorphans,
// not offered by the API.
type OrphanSweeper struct {
	files OrphanFiles
	rows  OrphanHashes
	now   func() time.Time
}

func NewOrphanSweeper(files OrphanFiles, rows OrphanHashes) *OrphanSweeper {
	return &OrphanSweeper{files: files, rows: rows, now: time.Now}
}

// Find draws up the plan: every file on the volume that no row names and that
// is older than OrphanGrace. It does not delete anything.
//
// The rows are read first and a failure stops the sweep: an empty set through
// an error would plan to delete every cover. A name this service never wrote
// is never a candidate.
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
			// Possibly an upload in flight (see OrphanGrace).
			continue
		}
		orphans = append(orphans, OrphanFile{
			Key: file.Name(), Hash: hash, SizeBytes: file.Size(), ModTime: file.ModTime(),
		})
	}

	// A stable order, so two runs of the plan compare line by line.
	sort.Slice(orphans, func(i, j int) bool { return orphans[i].Key < orphans[j].Key })
	return orphans, nil
}

// Remove deletes the files it is given, one at a time. It takes the plan
// rather than making one, so exactly what the operator approved runs. A
// refusal does not stop the rest, and every one is counted.
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

// hashOfKey reads back what Key wrote, and whether the name is one this
// service writes. It accepts any width, not only Sizes, so files of a width no
// longer produced can still be collected.
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
