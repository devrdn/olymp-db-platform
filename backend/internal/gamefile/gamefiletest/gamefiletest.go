// Package gamefiletest holds what tests of other packages need to look at a
// gamefile.Store from outside: the bytes behind an upload, which the Store
// itself reports to no production caller.
package gamefiletest

import "github.com/devrdn/olymp-db-platform/backend/internal/gamefile"

// StoredBytes is the length of an upload's file on disk, or the Store's own
// error (gamefile.ErrNotFound for an upload it does not hold).
func StoredBytes(s *gamefile.Store, id string) (int64, error) {
	f, err := s.Open(id)
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}
