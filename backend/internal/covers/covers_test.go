package covers_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/devrdn/db-contest/backend/internal/covers"
	"github.com/google/uuid"
)

func TestAnUploadWritesEverySizeAndRecordsWhatItWrote(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	contest := uuid.New()

	cover, err := f.service.Upload(ctx, contest, uuid.New(),
		bytes.NewReader(jpegOf(t, 1200, 900)), "Photo: A. Organiser")
	if err != nil {
		t.Fatalf("Upload() = %v", err)
	}

	if cover.Hash == "" {
		t.Error("the cover has no name, so nothing can address it")
	}
	if cover.Width != 1600 || cover.Height != 900 {
		t.Errorf("recorded %dx%d, want the largest rendition's 1600x900", cover.Width, cover.Height)
	}
	for _, size := range covers.Sizes {
		if _, ok := f.files.stored[covers.Key(cover.Hash, size)]; !ok {
			t.Errorf("nothing was written for %dpx", size)
		}
	}
	stored, err := f.service.ByContest(ctx, contest)
	if err != nil {
		t.Fatalf("ByContest() = %v", err)
	}
	if stored.Attribution != "Photo: A. Organiser" {
		t.Errorf("attribution = %q", stored.Attribution)
	}
}

func TestAnUploadWithNoCreditLineIsRefusedBeforeThePictureIsRead(t *testing.T) {
	// Refused before the decode, not after it: resampling a photograph for a
	// request that was always going to be refused is the expensive half of
	// this endpoint spent on nothing.
	f := newFixture()

	_, err := f.service.Upload(context.Background(), uuid.New(), uuid.New(),
		&refusingReader{t: t}, "   ")

	if !errors.Is(err, covers.ErrAttributionRequired) {
		t.Fatalf("Upload() = %v, want ErrAttributionRequired", err)
	}
}

func TestACreditLineHasABound(t *testing.T) {
	// The column is unbounded text and the body limit bounds the request, not
	// the field (CLAUDE.md, security rule 2).
	f := newFixture()

	_, err := f.service.Upload(context.Background(), uuid.New(), uuid.New(),
		bytes.NewReader(jpegOf(t, 320, 180)), strings.Repeat("я", covers.MaxAttributionLen+1))

	if !errors.Is(err, covers.ErrAttributionTooLong) {
		t.Fatalf("Upload() = %v, want ErrAttributionTooLong", err)
	}
}

func TestNothingIsRecordedWhenThePictureIsRefused(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	contest := uuid.New()

	_, err := f.service.Upload(ctx, contest, uuid.New(),
		strings.NewReader(`<svg xmlns="http://www.w3.org/2000/svg"/>`), "Photo: somebody")

	if !errors.Is(err, covers.ErrImageKind) {
		t.Fatalf("Upload() = %v, want ErrImageKind", err)
	}
	if _, err := f.service.ByContest(ctx, contest); !errors.Is(err, covers.ErrNotFound) {
		t.Errorf("ByContest() = %v, want the contest left without a cover", err)
	}
	if len(f.files.stored) != 0 {
		t.Errorf("%d files were written for a refused upload", len(f.files.stored))
	}
}

func TestAWidthThisServiceNeverStoredIsNotAFile(t *testing.T) {
	ctx := context.Background()
	f := newFixture()
	cover, err := f.service.Upload(ctx, uuid.New(), uuid.New(),
		bytes.NewReader(jpegOf(t, 1200, 900)), "Photo: A. Organiser")
	if err != nil {
		t.Fatalf("Upload() = %v", err)
	}

	if _, _, err := f.service.Read(ctx, cover.Hash, 1601); !errors.Is(err, covers.ErrNotFound) {
		t.Errorf("Read(1601) = %v, want ErrNotFound", err)
	}
	if _, _, err := f.service.Read(ctx, "../../etc/passwd", 800); !errors.Is(err, covers.ErrNotFound) {
		t.Errorf("Read() of a name that is not a hash = %v, want ErrNotFound", err)
	}
	if _, _, err := f.service.Read(ctx, cover.Hash, 800); err != nil {
		t.Errorf("Read(800) = %v, want the file that was written", err)
	}
}

func TestRemovingACoverLeavesTheFilesToTheSweep(t *testing.T) {
	// A file system that refuses a delete must not be able to fail an
	// organiser's edit; the sweep that already collects orphaned game
	// databases collects these too (design spec §4).
	ctx := context.Background()
	f := newFixture()
	contest := uuid.New()
	cover, err := f.service.Upload(ctx, contest, uuid.New(),
		bytes.NewReader(jpegOf(t, 1200, 900)), "Photo: A. Organiser")
	if err != nil {
		t.Fatalf("Upload() = %v", err)
	}

	if err := f.service.Remove(ctx, contest); err != nil {
		t.Fatalf("Remove() = %v", err)
	}

	if _, err := f.service.ByContest(ctx, contest); !errors.Is(err, covers.ErrNotFound) {
		t.Errorf("ByContest() = %v, want ErrNotFound", err)
	}
	if _, ok := f.files.stored[covers.Key(cover.Hash, 800)]; !ok {
		t.Error("the handler deleted the file; that is the sweep's job")
	}
}

func TestADraftsCoverIsNotPublic(t *testing.T) {
	// The file belongs to the olympiad, and the olympiad answers to the same
	// selection of statuses the public list makes. Guessing an address is not
	// a way into an unpublished contest.
	ctx := context.Background()
	f := newFixture()
	contest := uuid.New()
	if _, err := f.service.Upload(ctx, contest, uuid.New(),
		bytes.NewReader(jpegOf(t, 1200, 900)), "Photo: A. Organiser"); err != nil {
		t.Fatalf("Upload() = %v", err)
	}

	if _, err := f.service.Public(ctx, contest); !errors.Is(err, covers.ErrNotFound) {
		t.Fatalf("Public() of a draft = %v, want ErrNotFound", err)
	}

	f.repo.published[contest] = true
	if _, err := f.service.Public(ctx, contest); err != nil {
		t.Errorf("Public() of a published contest = %v, want its cover", err)
	}
}

// --- the stores, in memory -------------------------------------------------

type fixture struct {
	repo    *memoryRepo
	files   *memoryFiles
	service *covers.Service
}

func newFixture() *fixture {
	repo := &memoryRepo{rows: map[uuid.UUID]covers.Cover{}, published: map[uuid.UUID]bool{}}
	files := &memoryFiles{stored: map[string][]byte{}}
	return &fixture{repo: repo, files: files, service: covers.NewService(repo, files)}
}

// memoryRepo is the covers table, with the contest's own visibility beside it
// — which is what PublicByContest joins for in PostgreSQL.
type memoryRepo struct {
	rows      map[uuid.UUID]covers.Cover
	published map[uuid.UUID]bool
}

func (r *memoryRepo) Save(_ context.Context, cover covers.Cover) error {
	r.rows[cover.ContestID] = cover
	return nil
}

func (r *memoryRepo) ByContest(_ context.Context, contestID uuid.UUID) (covers.Cover, error) {
	cover, ok := r.rows[contestID]
	if !ok {
		return covers.Cover{}, covers.ErrNotFound
	}
	return cover, nil
}

func (r *memoryRepo) PublicByContest(ctx context.Context, contestID uuid.UUID) (covers.Cover, error) {
	if !r.published[contestID] {
		return covers.Cover{}, covers.ErrNotFound
	}
	return r.ByContest(ctx, contestID)
}

func (r *memoryRepo) Delete(_ context.Context, contestID uuid.UUID) error {
	delete(r.rows, contestID)
	return nil
}

// memoryFiles stands in for the directory on the volume.
type memoryFiles struct{ stored map[string][]byte }

func (f *memoryFiles) Put(_ context.Context, key string, _ string, body []byte) error {
	f.stored[key] = body
	return nil
}

func (f *memoryFiles) Get(_ context.Context, key string) ([]byte, string, error) {
	body, ok := f.stored[key]
	if !ok {
		return nil, "", errors.New("no such file")
	}
	return body, "image/jpeg", nil
}

// refusingReader fails the test if anything reads from it: it stands for the
// upload a refusal must happen before.
type refusingReader struct{ t *testing.T }

func (r *refusingReader) Read([]byte) (int, error) {
	r.t.Helper()
	r.t.Error("the body was read although the request was already refused")
	return 0, errors.New("nothing should have read this")
}
