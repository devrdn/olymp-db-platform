// Package filestore keeps small files in one directory on a volume and hands
// them back by key.
//
// It answers: where do a contest's cover pictures live, how does a whole file
// appear at once rather than in pieces, and how is a key kept from naming
// anything outside the directory it belongs to. It deliberately does not
// answer: what the bytes are, which contest they belong to, who may read
// them, or what cache headers they are served with. Those belong to the
// domain package that owns covers and to internal/api — this package knows
// one directory and nothing above it.
//
// It is infrastructure (CLAUDE.md, Go layout rule 7) and imports no domain
// package. The consumer declares the narrow interface it needs and this
// Store satisfies it structurally; nothing here is shared by importing.
//
// The design decision behind it is recorded in
// docs/ARCHITECTURE.md §9.7: a directory
// on a volume rather than a table in the database or an object store, which
// adds no service, no credentials and no new way to fail to start. The cost
// is named there too — a second API replica has a different disk — and this
// package is the seam that pays it, since an object-store implementation of
// the same port is a new type here and no change at all in the domain.
//
// Two properties hold, and both are security properties rather than
// niceties:
//
//   - A key is a name, never a path. Keys arrive as content hashes chosen by
//     code, but code that can be made to choose "../../etc/cron.d/x" turns an
//     upload into a write anywhere this process can reach. checkKey allows
//     one unqualified file name of a bounded length, built from an explicit
//     set of characters, ending in an extension this package will serve.
//   - A write is all or nothing. The bytes land in a temporary file in the
//     same directory, are flushed to disk, and are moved into place with one
//     rename. A reader therefore sees the whole file or no file — never a
//     prefix of one. That matters more here than it usually does: a file is
//     named by the hash of its content, so nothing will ever overwrite a
//     truncated one, and a picture served short would be served short
//     forever.
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

// Sentinels for every refusal this package can hand to a caller (CLAUDE.md,
// security rule 1). None is a bare errors.New at the call site: each is named
// here so a handler's fail switch can map it to a status code rather than
// collapsing a caller's own mistake into "internal error".
var (
	// ErrNotFound is a key the directory does not hold.
	ErrNotFound = errors.New("file not found")

	// ErrBadKey is a key that is not a name this store will use: empty, too
	// long, carrying a path separator or any character outside the allowed
	// set, or ending in an extension this store does not serve.
	ErrBadKey = errors.New("key is not a valid file name")

	// ErrTooLarge is a body — or a file already on the volume — beyond
	// MaxFileBytes.
	ErrTooLarge = errors.New("file exceeds the maximum size")
)

// MaxFileBytes bounds one file, in both directions (CLAUDE.md, security rule
// 2). 8 MiB matches the largest upload the cover route accepts, and every
// file this store actually holds is a re-encoded JPEG two orders of magnitude
// below it: the ceiling is there to stop a caller that has lost track of what
// it is writing, and to keep Get from reading an arbitrarily large file — one
// an operator dropped onto the volume by hand — into the memory of the
// process serving the olympiad.
const MaxFileBytes = 8 << 20

// maxKeyLen bounds a key. A content hash plus a size suffix and an extension
// is about 70 characters; 128 leaves room for a naming scheme to grow and
// stays far below every file system's own limit.
const maxKeyLen = 128

// servedTypes is the complete set of extensions this store accepts and the
// content type each one is served as. It is an allowlist for a reason: the
// files land in a directory the application serves from its own origin, and
// a stored .html or .svg would be script running as the site. Deciding the
// set here, once, also keeps the answer off mime.TypeByExtension, which
// reads a table from the machine the process happens to run on.
var servedTypes = map[string]string{
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".png":  "image/png",
	".webp": "image/webp",
}

// Store is one directory on disk, addressed by key.
//
// It holds no state beyond the path: every call goes to the file system, so
// nothing here has to be invalidated when another process — a backup, an
// operator — touches the same directory.
type Store struct {
	dir string
}

// New prepares dir and returns a Store over it.
//
// The directory is created when it is missing, so an operator who mounted a
// volume but never made the directory inside it gets a working service. It is
// then probed for writability, because a directory that cannot be written to
// must refuse at start-up and not on the day of the olympiad — and a
// read-only mount stats perfectly well, so nothing short of a write proves
// anything.
func New(dir string) (*Store, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("filestore: the directory must be named")
	}
	// 0o700: nothing outside this process has business in here. The
	// application serves the files; the directory is not published.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create the file directory %s: %w", dir, err)
	}
	store := &Store{dir: dir}
	if err := store.Ping(context.Background()); err != nil {
		return nil, err
	}
	return store, nil
}

// Dir is the directory the store writes to, for a log line at start-up.
func (s *Store) Dir() string { return s.dir }

// Put writes body under key, replacing whatever was there, as one step.
//
// contentType states what the caller believes it is writing. This store does
// not keep it: a second file beside each picture is a second thing to write,
// to back up, and to disagree with itself, so Get answers from the key's own
// extension instead. The parameter stays part of the port because an
// implementation that is not a directory — the object store the design spec
// names for the day a second replica exists — stores it as object metadata
// and needs it at exactly this call.
func (s *Store) Put(ctx context.Context, key string, contentType string, body []byte) error {
	_ = contentType // see the doc comment above.
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := checkKey(key); err != nil {
		return err
	}
	if len(body) > MaxFileBytes {
		return fmt.Errorf("%w: %d bytes, limit %d", ErrTooLarge, len(body), MaxFileBytes)
	}

	// In the same directory, so the rename below is a rename within one file
	// system and therefore atomic. A temporary file elsewhere — /tmp, say —
	// would make it a copy, which is exactly the torn write this avoids.
	tmp, err := os.CreateTemp(s.dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("create a temporary file in %s: %w", s.dir, err)
	}
	tmpName := tmp.Name()
	// Removes the temporary file on every path that does not rename it away.
	// After a successful rename the name no longer exists and this is a
	// no-op, which is why the error is dropped.
	defer func() { _ = os.Remove(tmpName) }()

	if err := writeAndSync(tmp, body); err != nil {
		return fmt.Errorf("write %s: %w", tmpName, err)
	}
	// CreateTemp makes the file 0o600; this is the same mode stated
	// deliberately rather than inherited, so a change in the standard
	// library's default cannot quietly widen it.
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return fmt.Errorf("set the mode of %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, filepath.Join(s.dir, key)); err != nil {
		return fmt.Errorf("move %s into place: %w", tmpName, err)
	}
	// The file's own contents are already on disk; this makes the directory
	// entry that names them durable too, so a power loss cannot leave the
	// database pointing at a cover whose name never reached the volume. A
	// file system that cannot sync a directory is not an error here — the
	// bytes are safe either way, and refusing the write would be worse.
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

	// The size is read from the file's metadata before any of it is
	// allocated (CLAUDE.md, security rule 12): checking after os.ReadFile
	// would be a limit applied to the bytes it exists to keep out.
	info, err := os.Stat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil, "", fmt.Errorf("%w: %s", ErrNotFound, key)
	case err != nil:
		return nil, "", fmt.Errorf("stat %s: %w", key, err)
	case info.IsDir():
		// Nothing this package writes is a directory, so one under a valid
		// key was put there by something else. Reporting it as absent is
		// both true for this store's purposes and free of detail about the
		// volume.
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

// Delete removes the file stored under key.
//
// A key that is not there is not an error: the caller is a sweep for files no
// contest refers to any more, and it has no use for the difference between
// "nothing to do" and "already done".
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

// List reports every file in the directory that this store would serve.
//
// It exists for one caller: the sweep that removes files no contest refers to
// any more. That shapes two decisions.
//
// It returns fs.FileInfo rather than a type of this package's own, so that a
// domain package can declare the narrow interface it needs over a standard
// library type and this Store satisfies it without either importing the other
// (CLAUDE.md, Go layout rules 3 and 7). The size and the modification time
// come with it, and the second is what tells an upload in flight apart from
// an orphan.
//
// It names only what checkKey accepts, so a listing a sweep acts on cannot
// contain something that sweep must not delete. A subdirectory, a file an
// operator left behind under another extension, and above all this package's
// own .tmp-* and .probe-* files are left out: a temporary file is a write in
// progress, and handing it to a caller that deletes what it is given would
// make the sweep the one thing able to tear a Put.
//
// The whole directory is read in one call. The volume holds two files per
// uploaded cover, so this is thousands of entries on a large installation and
// not a size that needs paging; a sweep that had to page would also have to
// decide what a file appearing between pages means.
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
			// Removed between the directory read and this stat. A file that
			// is already gone is nothing for the caller to do.
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("stat %s: %w", entry.Name(), err)
		}
		// Not entry.IsDir() twice over: the entry's own answer comes from the
		// directory read, and on a file system that reports DT_UNKNOWN it is
		// the stat above that knows.
		if info.IsDir() {
			continue
		}
		files = append(files, info)
	}
	return files, nil
}

// Ping reports whether the directory can still be written to.
//
// It writes and removes a probe file rather than stat-ing the directory,
// because the failure worth catching — a volume that came back read-only, or
// one that is full — is invisible to a stat. It leaves nothing behind: this
// runs on every readiness probe, and a file per probe would fill the volume
// it is checking.
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

// writeAndSync writes body to f, flushes it to the disk and closes f. The
// sync is what makes Put's rename meaningful: renaming a file whose contents
// are still only in the page cache would survive a crash as a name with no
// bytes behind it.
func writeAndSync(f *os.File, body []byte) error {
	write := func() error {
		if _, err := f.Write(body); err != nil {
			return err
		}
		return f.Sync()
	}
	err := write()
	// Closed on every path, and its own error reported when nothing worse
	// happened first: a write that only fails at close is still a failed
	// write.
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}

// checkKey accepts exactly one unqualified file name.
//
// The rule is an allowlist, not a search for the patterns that are known to
// escape: "is there a ../ in it" is a question with more right answers than
// anyone can enumerate once separators, symbolic links and encodings are in
// play, while "is every character one of these, and is the whole thing a
// plain name" has one. A content hash with a size suffix and an extension
// passes; nothing that could name a second directory does.
func checkKey(key string) error {
	if key == "" {
		return fmt.Errorf("%w: it is empty", ErrBadKey)
	}
	if len(key) > maxKeyLen {
		return fmt.Errorf("%w: %d characters, limit %d", ErrBadKey, len(key), maxKeyLen)
	}
	if key == "." || key == ".." || strings.HasPrefix(key, ".") {
		// A leading dot is refused outright rather than only "." and "..":
		// it is also what this package's own temporary and probe files are
		// named with, and a key can never be allowed to name one of those.
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
