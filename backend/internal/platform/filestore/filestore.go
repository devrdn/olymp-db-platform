// Package filestore keeps small files (contest covers) in one directory on a
// volume and hands them back by key. It does not know what the bytes are,
// whom they belong to, or who may read them; that is the covers package and
// internal/api. An object-store implementation would be a new type here.
//
// Two security properties hold:
//
//   - A key is a name, never a path. checkKey allows one unqualified file name
//     of bounded length, from an explicit character set, with an extension
//     this package serves.
//   - A write is all or nothing: temp file in the same directory, sync, one
//     rename. Files are named by content hash and never rewritten, so a
//     truncated one would be served short forever.
package filestore

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Sentinels for every refusal this package can hand to a caller (CLAUDE.md
// security rule 1).
var (
	ErrNotFound = errors.New("file not found")

	// ErrBadKey is a key that is not a name this store will use (see
	// checkKey).
	ErrBadKey = errors.New("key is not a valid file name")

	// ErrTooLarge is a body, or a file already on the volume, beyond
	// MaxFileBytes.
	ErrTooLarge = errors.New("file exceeds the maximum size")
)

// MaxFileBytes bounds one file on write and on read (CLAUDE.md security rule
// 2). It matches the largest cover upload; on read it stops Get loading a
// large file an operator dropped onto the volume by hand.
const MaxFileBytes = 8 << 20

// maxKeyLen bounds a key. Current keys are about 70 characters.
const maxKeyLen = 128

// servedTypes is the allowlist of extensions and the content type each is
// served as. Files are served from the application's origin, so a stored .html
// or .svg would be script running as the site. It also avoids
// mime.TypeByExtension, which depends on the host's tables.
var servedTypes = map[string]string{
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".png":  "image/png",
	".webp": "image/webp",
}

// Store is one directory on disk, addressed by key. It holds no state beyond
// the path, so other processes may touch the directory freely.
type Store struct {
	dir string
}

// New creates dir if missing and probes it with a write, so an unwritable
// directory fails at start-up rather than on the day of the olympiad.
func New(dir string) (*Store, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("filestore: the directory must be named")
	}
	// 0o700: the application serves the files; the directory is not published.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create the file directory %s: %w", dir, err)
	}
	store := &Store{dir: dir}
	if err := store.Ping(context.Background()); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *Store) Dir() string { return s.dir }

// Put writes body under key, replacing whatever was there, as one step.
//
// This store ignores contentType and answers Get from the key's extension. The
// parameter is part of the port for an object-store implementation, which
// keeps it as metadata.
func (s *Store) Put(ctx context.Context, key string, contentType string, body []byte) error {
	_ = contentType
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := checkKey(key); err != nil {
		return err
	}
	if len(body) > MaxFileBytes {
		return fmt.Errorf("%w: %d bytes, limit %d", ErrTooLarge, len(body), MaxFileBytes)
	}

	// In the same directory, so the rename stays within one file system and
	// is atomic.
	tmp, err := os.CreateTemp(s.dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("create a temporary file in %s: %w", s.dir, err)
	}
	tmpName := tmp.Name()
	// After a successful rename this is a no-op, hence the dropped error.
	defer func() { _ = os.Remove(tmpName) }()

	if err := writeAndSync(tmp, body); err != nil {
		return fmt.Errorf("write %s: %w", tmpName, err)
	}
	// Stated rather than inherited from CreateTemp's default.
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return fmt.Errorf("set the mode of %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, filepath.Join(s.dir, key)); err != nil {
		return fmt.Errorf("move %s into place: %w", tmpName, err)
	}
	// Sync the directory entry too, so a power loss cannot leave the database
	// pointing at a name that never reached the volume. A file system that
	// cannot sync a directory is not an error.
	if dir, err := os.Open(s.dir); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}

// Get returns the file stored under key and the content type its extension
// implies.
func (s *Store) Get(ctx context.Context, key string) ([]byte, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if err := checkKey(key); err != nil {
		return nil, "", err
	}
	path := filepath.Join(s.dir, key)

	// Size checked from metadata before allocating (CLAUDE.md rule 12).
	info, err := os.Stat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil, "", fmt.Errorf("%w: %s", ErrNotFound, key)
	case err != nil:
		return nil, "", fmt.Errorf("stat %s: %w", key, err)
	case info.IsDir():
		// A directory under a valid key was not written by this store.
		return nil, "", fmt.Errorf("%w: %s", ErrNotFound, key)
	case info.Size() > MaxFileBytes:
		return nil, "", fmt.Errorf("%w: %s is %d bytes, limit %d", ErrTooLarge, key, info.Size(), MaxFileBytes)
	}

	body, err := os.ReadFile(path) // #nosec G304 -- checkKey allows one unqualified name under s.dir.
	if errors.Is(err, os.ErrNotExist) {
		// Removed between the stat above and this read.
		return nil, "", fmt.Errorf("%w: %s", ErrNotFound, key)
	}
	if err != nil {
		return nil, "", fmt.Errorf("read %s: %w", key, err)
	}
	return body, servedTypes[strings.ToLower(filepath.Ext(key))], nil
}

// Delete removes the file stored under key. A missing key is not an error.
func (s *Store) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := checkKey(key); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(s.dir, key)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", key, err)
	}
	return nil
}

// List reports every file in the directory that this store would serve, for
// the orphan sweep.
//
// It returns fs.FileInfo so a domain package can declare its own interface
// without importing this one (CLAUDE.md layout rules 3 and 7); the
// modification time tells an upload in flight from an orphan. It names only
// what checkKey accepts, so this store's own .tmp-* and .probe-* files never
// reach a caller that deletes what it is given.
func (s *Store) List(ctx context.Context) ([]fs.FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("read the file directory %s: %w", s.dir, err)
	}

	files := make([]fs.FileInfo, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || checkKey(entry.Name()) != nil {
			continue
		}
		info, err := entry.Info()
		if errors.Is(err, os.ErrNotExist) {
			// Removed since the directory read.
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("stat %s: %w", entry.Name(), err)
		}
		// Checked again after stat: a file system may report DT_UNKNOWN.
		if info.IsDir() {
			continue
		}
		files = append(files, info)
	}
	return files, nil
}

// Ping reports whether the directory can still be written to. It writes and
// removes a probe file, because a read-only or full volume is invisible to a
// stat.
func (s *Store) Ping(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	probe, err := os.CreateTemp(s.dir, ".probe-*")
	if err != nil {
		return fmt.Errorf("write to the file directory %s: %w", s.dir, err)
	}
	name := probe.Name()
	defer func() { _ = os.Remove(name) }()

	if err := writeAndSync(probe, []byte("probe")); err != nil {
		return fmt.Errorf("write to the file directory %s: %w", s.dir, err)
	}
	return nil
}

// writeAndSync writes body to f, syncs and closes it. Without the sync, a
// renamed file could survive a crash as a name with no bytes behind it.
func writeAndSync(f *os.File, body []byte) error {
	write := func() error {
		if _, err := f.Write(body); err != nil {
			return err
		}
		return f.Sync()
	}
	err := write()
	// A write that only fails at close is still a failed write.
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}

// checkKey accepts exactly one unqualified file name. It is an allowlist of
// characters rather than a search for escaping patterns, which cannot be
// enumerated reliably.
func checkKey(key string) error {
	if key == "" {
		return fmt.Errorf("%w: it is empty", ErrBadKey)
	}
	if len(key) > maxKeyLen {
		return fmt.Errorf("%w: %d characters, limit %d", ErrBadKey, len(key), maxKeyLen)
	}
	if key == "." || key == ".." || strings.HasPrefix(key, ".") {
		// Any leading dot: it also marks this package's temp and probe files.
		return fmt.Errorf("%w: %q is not a file name", ErrBadKey, key)
	}
	for _, r := range key {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.', r == '-', r == '_':
		default:
			return fmt.Errorf("%w: %q contains %q", ErrBadKey, key, r)
		}
	}
	if _, ok := servedTypes[strings.ToLower(filepath.Ext(key))]; !ok {
		return fmt.Errorf("%w: %q does not end in an extension this store serves", ErrBadKey, key)
	}
	return nil
}
