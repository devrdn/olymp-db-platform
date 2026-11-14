package api_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/devrdn/olymp-db-platform/backend/internal/api"
	"github.com/devrdn/olymp-db-platform/backend/internal/auth"
	"github.com/devrdn/olymp-db-platform/backend/internal/covers"
	"github.com/devrdn/olymp-db-platform/backend/internal/platform/cache"
	"github.com/devrdn/olymp-db-platform/backend/internal/platform/logging"
	"github.com/devrdn/olymp-db-platform/backend/internal/rbac"
	"github.com/devrdn/olymp-db-platform/backend/internal/users"
	"github.com/devrdn/olymp-db-platform/backend/internal/users/userstest"
)

func TestUploadingACoverStoresItAndSaysWhatWasStored(t *testing.T) {
	f := newCoverFixture(t, rbac.PermissionContestAdminAll)
	contest := uuid.New()

	rec := f.upload(t, contest, coverJPEG(t, 2000, 1000), "Photo: A. Organiser")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	body := decode(t, rec)
	if body["width"] != float64(1600) || body["height"] != float64(900) {
		t.Errorf("stored %v x %v, want 1600x900", body["width"], body["height"])
	}
	if hash, _ := body["hash"].(string); hash == "" {
		t.Error("the answer carries no address for the picture")
	}
	if body["attribution"] != "Photo: A. Organiser" {
		t.Errorf("attribution = %v", body["attribution"])
	}
}

func TestAnSVGIsRefusedByTheRoute(t *testing.T) {
	// The cover is shown to every front-page visitor, so an SVG served from
	// this origin would be cross-site scripting on the most public surface.
	f := newCoverFixture(t, rbac.PermissionContestAdminAll)

	rec := f.upload(t, uuid.New(),
		[]byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`),
		"Drawing: nobody")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
	if got := errorCode(t, rec); got != "cover_kind" {
		t.Errorf("code = %q, want cover_kind", got)
	}
}

func TestAPictureClaimingMorePixelsThanItHasIsRefused(t *testing.T) {
	f := newCoverFixture(t, rbac.PermissionContestAdminAll)

	rec := f.upload(t, uuid.New(), coverPNGHeaderClaiming(t, 30000, 30000), "Photo: A. Organiser")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
	if got := errorCode(t, rec); got != "cover_dimensions" {
		t.Errorf("code = %q, want cover_dimensions", got)
	}
}

func TestACoverWithNoCreditLineIsRefused(t *testing.T) {
	f := newCoverFixture(t, rbac.PermissionContestAdminAll)

	rec := f.upload(t, uuid.New(), coverJPEG(t, 800, 450), "   ")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
	if got := errorCode(t, rec); got != "cover_attribution_required" {
		t.Errorf("code = %q, want cover_attribution_required", got)
	}
}

func TestACreditLinePastItsBoundIsRefused(t *testing.T) {
	f := newCoverFixture(t, rbac.PermissionContestAdminAll)

	rec := f.upload(t, uuid.New(), coverJPEG(t, 800, 450),
		strings.Repeat("x", covers.MaxAttributionLen+1))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
	if got := errorCode(t, rec); got != "cover_attribution_too_long" {
		t.Errorf("code = %q, want cover_attribution_too_long", got)
	}
}

func TestABodyPastTheCeilingIsRefusedOnTheSocket(t *testing.T) {
	f := newCoverFixture(t, rbac.PermissionContestAdminAll)
	oversize := bytes.Repeat([]byte{0xAB}, covers.MaxUploadBytes+coverBodySlack)
	copy(oversize, coverJPEG(t, 16, 9))

	rec := f.upload(t, uuid.New(), oversize, "Photo: A. Organiser")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
	if got := errorCode(t, rec); got != "cover_too_large" {
		t.Errorf("code = %q, want cover_too_large", got)
	}
}

func TestUploadingACoverNeedsThePermissionToEditTheContest(t *testing.T) {
	f := newCoverFixture(t) // no permissions at all

	rec := f.upload(t, uuid.New(), coverJPEG(t, 800, 450), "Photo: A. Organiser")

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 (%s)", rec.Code, rec.Body.String())
	}
}

func TestUploadingTooOftenIsRefusedAndTheRefusalsCount(t *testing.T) {
	// A refused upload has already been read and sniffed, so it counts too
	// (CLAUDE.md rule 13).
	f := newCoverFixture(t, rbac.PermissionContestAdminAll)
	contest := uuid.New()

	for range api.CoverUploadsPerMinute {
		f.upload(t, contest, []byte("not a picture"), "Photo: A. Organiser")
	}
	rec := f.upload(t, contest, coverJPEG(t, 800, 450), "Photo: A. Organiser")

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 (%s)", rec.Code, rec.Body.String())
	}
	if got := errorCode(t, rec); got != "cover_too_often" {
		t.Errorf("code = %q, want cover_too_often", got)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("the refusal does not say how long to wait")
	}
}

func TestAnUploadPastTheDecodeSlotsIsToldToComeBackWithoutReachingTheStore(t *testing.T) {
	store, release := newBlockingCoverStore(t)
	f := newCoverFixtureServing(t, store, rbac.PermissionContestAdminAll)
	contest := uuid.New()
	picture := coverJPEG(t, 800, 450)

	held := f.holdDecodeSlots(t, store, contest, picture)
	rec := f.uploadPromptly(t, contest, picture)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (%s)", rec.Code, rec.Body.String())
	}
	if got := errorCode(t, rec); got != "cover_busy" {
		t.Errorf("code = %q, want cover_busy", got)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("the refusal does not say how long to wait")
	}
	if got := store.calls.Load(); got != api.CoverDecodeSlots {
		t.Errorf("the store was reached %d times, want %d: the refused upload reached it", got, api.CoverDecodeSlots)
	}

	release()
	for range api.CoverDecodeSlots {
		if code := <-held; code != http.StatusOK {
			t.Errorf("a held upload answered %d, want 200", code)
		}
	}
	if rec := f.upload(t, contest, picture, "Photo: A. Organiser"); rec.Code != http.StatusOK {
		t.Errorf("an upload after the slots came free answered %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
}

func TestAnUploadStillSendingItsBodyHoldsNoDecodeSlot(t *testing.T) {
	store, _ := newBlockingCoverStore(t)
	f := newCoverFixtureServing(t, store, rbac.PermissionContestAdminAll)
	contest := uuid.New()
	picture := coverJPEG(t, 800, 450)

	for range api.CoverDecodeSlots {
		f.startSlowUpload(t, contest)
	}
	f.holdDecodeSlots(t, store, contest, picture)

	if rec := f.uploadPromptly(t, contest, picture); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status with every slot decoding = %d, want 503 (%s)", rec.Code, rec.Body.String())
	}
}

func TestRemovingACoverLeavesTheContestItsDrawnOne(t *testing.T) {
	f := newCoverFixture(t, rbac.PermissionContestAdminAll)
	contest := uuid.New()
	if rec := f.upload(t, contest, coverJPEG(t, 1600, 900), "Photo: A. Organiser"); rec.Code != http.StatusOK {
		t.Fatalf("upload: status = %d (%s)", rec.Code, rec.Body.String())
	}
	f.repo.published[contest] = true

	rec := f.do(t, http.MethodDelete, "/contests/"+contest.String()+"/cover", "", nil)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (%s)", rec.Code, rec.Body.String())
	}
	if got := f.do(t, http.MethodGet, "/public/contests/"+contest.String()+"/cover", "", nil); got.Code != http.StatusNotFound {
		t.Errorf("the cover is still served: status = %d", got.Code)
	}
}

func TestADraftsCoverIsNotServedToAVisitorWithNoSession(t *testing.T) {
	// The file follows the olympiad's visibility: the statuses the public list
	// shows.
	f := newCoverFixture(t, rbac.PermissionContestAdminAll)
	contest := uuid.New()
	if rec := f.upload(t, contest, coverJPEG(t, 1600, 900), "Photo: A. Organiser"); rec.Code != http.StatusOK {
		t.Fatalf("upload: status = %d (%s)", rec.Code, rec.Body.String())
	}

	rec := f.do(t, http.MethodGet, "/public/contests/"+contest.String()+"/cover", "", nil)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (%s)", rec.Code, rec.Body.String())
	}
	if got := errorCode(t, rec); got != "not_found" {
		t.Errorf("code = %q, want not_found", got)
	}
}

// The contest-only address must not be cached forever: a replacement travels
// through it.
func TestTheAddressThatNamesTheFileIsTheOneCachedForever(t *testing.T) {
	f := newCoverFixture(t, rbac.PermissionContestAdminAll)
	contest := uuid.New()
	upload := f.upload(t, contest, coverJPEG(t, 1600, 900), "Photo: A. Organiser")
	if upload.Code != http.StatusOK {
		t.Fatalf("upload: status = %d (%s)", upload.Code, upload.Body.String())
	}
	f.repo.published[contest] = true

	hash, _ := decode(t, upload)["hash"].(string)
	rec := f.do(t, http.MethodGet,
		"/public/contests/"+contest.String()+"/cover?size=800&v="+hash, "", nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	header := rec.Header()
	if header.Get("Content-Type") != "image/jpeg" {
		t.Errorf("Content-Type = %q, want image/jpeg", header.Get("Content-Type"))
	}
	if header.Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Errorf("Cache-Control = %q", header.Get("Cache-Control"))
	}
	if header.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", header.Get("X-Content-Type-Options"))
	}
	if want := `"` + hash + `-800"`; header.Get("ETag") != want {
		t.Errorf("ETag = %q, want %q", header.Get("ETag"), want)
	}
	// The picture itself, not a page about it.
	if _, err := jpeg.Decode(bytes.NewReader(rec.Body.Bytes())); err != nil {
		t.Errorf("the body does not decode as a JPEG: %v", err)
	}
}

func TestACacheThatAlreadyHasTheCoverIsToldSoWithoutTheBytes(t *testing.T) {
	f := newCoverFixture(t, rbac.PermissionContestAdminAll)
	contest := uuid.New()
	upload := f.upload(t, contest, coverJPEG(t, 1600, 900), "Photo: A. Organiser")
	if upload.Code != http.StatusOK {
		t.Fatalf("upload: status = %d (%s)", upload.Code, upload.Body.String())
	}
	f.repo.published[contest] = true
	hash, _ := decode(t, upload)["hash"].(string)

	rec := f.do(t, http.MethodGet, "/public/contests/"+contest.String()+"/cover?size=1600", "",
		http.Header{"If-None-Match": []string{`"` + hash + `-1600"`}})

	if rec.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304 (%s)", rec.Code, rec.Body.String())
	}
	if rec.Body.Len() != 0 {
		t.Errorf("304 carried %d bytes", rec.Body.Len())
	}
}

func TestTheTwoRenditionsAreDifferentFilesUnderOneHash(t *testing.T) {
	// The ETag names the hash and the width together, or a cache holding the
	// small file would answer a request for the large one with it.
	f := newCoverFixture(t, rbac.PermissionContestAdminAll)
	contest := uuid.New()
	if rec := f.upload(t, contest, coverJPEG(t, 2000, 1200), "Photo: A. Organiser"); rec.Code != http.StatusOK {
		t.Fatalf("upload: status = %d (%s)", rec.Code, rec.Body.String())
	}
	f.repo.published[contest] = true

	large := f.do(t, http.MethodGet, "/public/contests/"+contest.String()+"/cover?size=1600", "", nil)
	small := f.do(t, http.MethodGet, "/public/contests/"+contest.String()+"/cover?size=800", "", nil)

	if large.Code != http.StatusOK || small.Code != http.StatusOK {
		t.Fatalf("statuses = %d and %d, want both 200", large.Code, small.Code)
	}
	if large.Header().Get("ETag") == small.Header().Get("ETag") {
		t.Error("the two renditions share an ETag, so a cache would serve one for the other")
	}
	if small.Body.Len() >= large.Body.Len() {
		t.Errorf("the 800px file is %d bytes and the 1600px one %d", small.Body.Len(), large.Body.Len())
	}
}

func TestAWidthNothingWasStoredAtIsNotFound(t *testing.T) {
	f := newCoverFixture(t, rbac.PermissionContestAdminAll)
	contest := uuid.New()
	if rec := f.upload(t, contest, coverJPEG(t, 1600, 900), "Photo: A. Organiser"); rec.Code != http.StatusOK {
		t.Fatalf("upload: status = %d (%s)", rec.Code, rec.Body.String())
	}
	f.repo.published[contest] = true

	for _, query := range []string{"?size=1601", "?size=nine", "?size=" + strings.Repeat("9", 40)} {
		rec := f.do(t, http.MethodGet, "/public/contests/"+contest.String()+"/cover"+query, "", nil)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404 (%s)", query, rec.Code, rec.Body.String())
		}
	}
}

func TestAContestThatIsNotAnIdentifierIsNotFound(t *testing.T) {
	// The public route answers nothing about what does or does not exist.
	f := newCoverFixture(t, rbac.PermissionContestAdminAll)

	rec := f.do(t, http.MethodGet, "/public/contests/not-a-uuid/cover", "", nil)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (%s)", rec.Code, rec.Body.String())
	}
}

// --- the fixture -----------------------------------------------------------

// coverBodySlack mirrors the handler's own envelope allowance, so the
// oversize test sends something the socket ceiling is sure to refuse.
const coverBodySlack = 128 << 10

type coverFixture struct {
	router http.Handler
	repo   *coverRepo
	files  *coverFiles
	cookie *http.Cookie
}

func newCoverFixture(t *testing.T, permissions ...string) *coverFixture {
	t.Helper()

	covering := &coverRepo{rows: map[uuid.UUID]covers.Cover{}, published: map[uuid.UUID]bool{}}
	files := &coverFiles{stored: map[string][]byte{}}
	f := newCoverFixtureServing(t, covers.NewService(covering, files), permissions...)
	f.repo, f.files = covering, files
	return f
}

// newCoverFixtureServing mounts the routes over any store, for a test that
// needs the store itself to behave in some particular way.
func newCoverFixtureServing(t *testing.T, store api.CoverStore, permissions ...string) *coverFixture {
	t.Helper()

	c := cache.NewMemory(10000)
	t.Cleanup(func() { _ = c.Close() })

	repo := userstest.New()
	repo.GrantRole("staff", permissions...)
	actor := repo.Add(users.User{
		Login: "organizer", FullName: "Organizer", Status: users.StatusActive, Roles: []string{"staff"},
	})

	log := logging.New("error", io.Discard)
	sessions := auth.NewSessionStore(c, time.Hour)
	token, err := sessions.Create(t.Context(), auth.Principal{UserID: actor.ID, Login: actor.Login})
	if err != nil {
		t.Fatalf("session Create() returned error: %v", err)
	}

	mw := auth.NewMiddleware(auth.MiddlewareConfig{
		Sessions: sessions, Users: repo,
		Authorizer: rbac.New(noRoles{}), Cookies: auth.NewCookieWriter(false), Logger: log,
	})

	router := chi.NewRouter()
	api.NewCoverHandler(store, auth.NewLimiter(c), mw, log).Mount(router)

	return &coverFixture{router: router, cookie: &http.Cookie{Name: sessionName, Value: token}}
}

// upload sends one multipart form, as the organiser's panel does.
func (f *coverFixture) upload(t *testing.T, contest uuid.UUID, picture []byte, attribution string) *httptest.ResponseRecorder {
	t.Helper()

	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, f.uploadRequest(t, contest, picture, attribution))
	return rec
}

func (f *coverFixture) uploadRequest(t *testing.T, contest uuid.UUID, picture []byte, attribution string) *http.Request {
	t.Helper()

	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, err := form.CreateFormFile("file", "cover.jpg")
	if err != nil {
		t.Fatalf("build the form: %v", err)
	}
	if _, err := part.Write(picture); err != nil {
		t.Fatalf("write the picture into the form: %v", err)
	}
	if err := form.WriteField("attribution", attribution); err != nil {
		t.Fatalf("write the attribution into the form: %v", err)
	}
	if err := form.Close(); err != nil {
		t.Fatalf("close the form: %v", err)
	}

	req := httptest.NewRequest(http.MethodPut, "/contests/"+contest.String()+"/cover", &body)
	req.Header.Set("Content-Type", form.FormDataContentType())
	req.AddCookie(f.cookie)
	return req
}

func (f *coverFixture) do(t *testing.T, method, path, body string, header http.Header) *httptest.ResponseRecorder {
	t.Helper()

	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	for name, values := range header {
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}
	// The cookie too: a signed-in organiser on the front page is an ordinary
	// visitor, and the route must not change because a session is present.
	req.AddCookie(f.cookie)
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec
}

// coverRepo is the covers table in memory, with the contest visibility the real
// repository joins for.
type coverRepo struct {
	rows      map[uuid.UUID]covers.Cover
	published map[uuid.UUID]bool
}

func (r *coverRepo) Save(_ context.Context, cover covers.Cover) error {
	r.rows[cover.ContestID] = cover
	return nil
}

func (r *coverRepo) ByContest(_ context.Context, contestID uuid.UUID) (covers.Cover, error) {
	cover, ok := r.rows[contestID]
	if !ok {
		return covers.Cover{}, covers.ErrNotFound
	}
	return cover, nil
}

func (r *coverRepo) PublicByContest(ctx context.Context, contestID uuid.UUID) (covers.Cover, error) {
	if !r.published[contestID] {
		return covers.Cover{}, covers.ErrNotFound
	}
	return r.ByContest(ctx, contestID)
}

func (r *coverRepo) Delete(_ context.Context, contestID uuid.UUID) error {
	delete(r.rows, contestID)
	return nil
}

// blockingCoverStore holds every upload until release is closed, as a slow
// decode would. Only Upload is implemented.
type blockingCoverStore struct {
	api.CoverStore
	calls   atomic.Int32
	entered chan struct{}
	release chan struct{}
}

// newBlockingCoverStore returns the store and its release, which the test's
// cleanup also calls so no upload is left waiting.
func newBlockingCoverStore(t *testing.T) (*blockingCoverStore, func()) {
	t.Helper()
	store := &blockingCoverStore{
		entered: make(chan struct{}, api.CoverDecodeSlots+1),
		release: make(chan struct{}),
	}
	release := sync.OnceFunc(func() { close(store.release) })
	t.Cleanup(release)
	return store, release
}

func (s *blockingCoverStore) Upload(context.Context, uuid.UUID, uuid.UUID, io.Reader, string) (covers.Cover, error) {
	s.calls.Add(1)
	s.entered <- struct{}{}
	<-s.release
	return covers.Cover{Hash: "held"}, nil
}

// coverWait bounds every wait in the slot tests, so a regression fails rather
// than hangs.
const coverWait = 5 * time.Second

// holdDecodeSlots starts as many uploads as there are slots and returns once
// each is inside the store; their status codes arrive on the channel after
// the store is released.
func (f *coverFixture) holdDecodeSlots(t *testing.T, store *blockingCoverStore, contest uuid.UUID, picture []byte) <-chan int {
	t.Helper()
	held := make(chan int, api.CoverDecodeSlots)
	for range api.CoverDecodeSlots {
		req := f.uploadRequest(t, contest, picture, "Photo: A. Organiser")
		go func() {
			rec := httptest.NewRecorder()
			f.router.ServeHTTP(rec, req)
			held <- rec.Code
		}()
		select {
		case <-store.entered:
		case <-time.After(coverWait):
			t.Fatal("an upload with a slot free never reached the store")
		}
	}
	return held
}

// uploadPromptly sends one upload and fails the test if it is still waiting
// after coverWait.
func (f *coverFixture) uploadPromptly(t *testing.T, contest uuid.UUID, picture []byte) *httptest.ResponseRecorder {
	t.Helper()
	answered := make(chan *httptest.ResponseRecorder, 1)
	req := f.uploadRequest(t, contest, picture, "Photo: A. Organiser")
	go func() {
		rec := httptest.NewRecorder()
		f.router.ServeHTTP(rec, req)
		answered <- rec
	}()
	select {
	case rec := <-answered:
		return rec
	case <-time.After(coverWait):
		t.Fatal("the upload waited instead of being answered")
		return nil
	}
}

// startSlowUpload sends the start of a form and leaves the rest of its body
// unsent until the test ends.
func (f *coverFixture) startSlowUpload(t *testing.T, contest uuid.UUID) {
	t.Helper()
	body, sending := io.Pipe()
	form := multipart.NewWriter(sending)
	req := httptest.NewRequest(http.MethodPut, "/contests/"+contest.String()+"/cover", body)
	req.Header.Set("Content-Type", form.FormDataContentType())
	req.AddCookie(f.cookie)

	served := make(chan struct{})
	go func() {
		defer close(served)
		f.router.ServeHTTP(httptest.NewRecorder(), req)
		// A handler that answered without reading must not leave the write
		// below waiting.
		_ = body.Close()
	}()
	t.Cleanup(func() {
		_ = sending.CloseWithError(io.ErrUnexpectedEOF)
		<-served
	})

	// A pipe write returns only once the handler has read it.
	if _, err := form.CreateFormFile("file", "cover.jpg"); err != nil {
		t.Fatalf("the handler did not start reading the body: %v", err)
	}
}

// coverFiles stands in for the directory on the volume.
type coverFiles struct{ stored map[string][]byte }

func (f *coverFiles) Put(_ context.Context, key string, _ string, body []byte) error {
	f.stored[key] = body
	return nil
}

func (f *coverFiles) Get(_ context.Context, key string) ([]byte, string, error) {
	body, ok := f.stored[key]
	if !ok {
		return nil, "", errors.New("no such file")
	}
	return body, "image/jpeg", nil
}

// coverJPEG is a real picture, so the route exercises the decoder rather than
// a byte string that happens to start correctly.
func coverJPEG(t *testing.T, width, height int) []byte {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			img.Set(x, y, color.RGBA{R: uint8(x % 251), G: uint8(y % 241), B: 0x40, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
	return buf.Bytes()
}

// coverPNGHeaderClaiming is a PNG signature and one IHDR chunk, with no pixel
// data at all: the decompression bomb this route refuses from the header.
func coverPNGHeaderClaiming(t *testing.T, width, height int) []byte {
	t.Helper()

	header := make([]byte, 13)
	binary.BigEndian.PutUint32(header[0:4], uint32(width))
	binary.BigEndian.PutUint32(header[4:8], uint32(height))
	header[8] = 8 // bit depth
	header[9] = 2 // colour type: truecolour

	chunk := make([]byte, 0, 8+len(header)+4)
	chunk = binary.BigEndian.AppendUint32(chunk, uint32(len(header)))
	chunk = append(chunk, "IHDR"...)
	chunk = append(chunk, header...)
	chunk = binary.BigEndian.AppendUint32(chunk, crc32.ChecksumIEEE(chunk[4:]))

	return append([]byte("\x89PNG\r\n\x1a\n"), chunk...)
}

// Without the hash the address keeps its name when the picture is replaced, so
// a year of immutable caching would hide the replacement.
func TestTheAddressWithoutTheHashIsNotCachedForever(t *testing.T) {
	f := newCoverFixture(t, rbac.PermissionContestAdminAll)
	contest := uuid.New()
	if rec := f.upload(t, contest, coverJPEG(t, 1600, 900), "Photo: A. Organiser"); rec.Code != http.StatusOK {
		t.Fatalf("upload: status = %d (%s)", rec.Code, rec.Body.String())
	}
	f.repo.published[contest] = true

	rec := f.do(t, http.MethodGet, "/public/contests/"+contest.String()+"/cover?size=800", "", nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); strings.Contains(got, "immutable") {
		t.Errorf("Cache-Control = %q: this address is not immutable", got)
	}
	if rec.Header().Get("ETag") == "" {
		t.Error("no ETag: without one the short cache costs a body on every revalidation")
	}
}

// Caching headers belong to the answer, and until the file is read there is no
// answer.
func TestARefusedSizeCarriesNoCachingOfItsOwn(t *testing.T) {
	f := newCoverFixture(t, rbac.PermissionContestAdminAll)
	contest := uuid.New()
	if rec := f.upload(t, contest, coverJPEG(t, 1600, 900), "Photo: A. Organiser"); rec.Code != http.StatusOK {
		t.Fatalf("upload: status = %d (%s)", rec.Code, rec.Body.String())
	}
	f.repo.published[contest] = true

	rec := f.do(t, http.MethodGet, "/public/contests/"+contest.String()+"/cover?size=999", "", nil)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); strings.Contains(got, "31536000") {
		t.Errorf("Cache-Control = %q on a refusal", got)
	}
}

// The public route refuses a draft to everybody, so without a read of their own
// an organiser would see the drawn cover and have no way to remove the real
// one.
func TestAnOrganiserReadsTheCoverOfTheirOwnDraft(t *testing.T) {
	f := newCoverFixture(t, rbac.PermissionContestAdminAll)
	contest := uuid.New()
	upload := f.upload(t, contest, coverJPEG(t, 1600, 900), "Photo: A. Organiser")
	if upload.Code != http.StatusOK {
		t.Fatalf("upload: status = %d (%s)", upload.Code, upload.Body.String())
	}
	// Not published: the state the panel is used in.
	f.repo.published[contest] = false

	meta := f.do(t, http.MethodGet, "/contests/"+contest.String()+"/cover", "", nil)
	if meta.Code != http.StatusOK {
		t.Fatalf("metadata: status = %d, want 200 (%s)", meta.Code, meta.Body.String())
	}
	if got, _ := decode(t, meta)["attribution"].(string); got != "Photo: A. Organiser" {
		t.Errorf("attribution = %q", got)
	}

	file := f.do(t, http.MethodGet, "/contests/"+contest.String()+"/cover/file?size=800", "", nil)
	if file.Code != http.StatusOK {
		t.Fatalf("file: status = %d, want 200 (%s)", file.Code, file.Body.String())
	}
	if _, err := jpeg.Decode(bytes.NewReader(file.Body.Bytes())); err != nil {
		t.Errorf("the body does not decode as a JPEG: %v", err)
	}
	// Behind a session, so no shared cache may keep it.
	if got := file.Header().Get("Cache-Control"); !strings.Contains(got, "private") {
		t.Errorf("Cache-Control = %q, want private: this answer belongs to one account", got)
	}
}

func TestAStrangerDoesNotReadADraftsCoverThroughTheStaffRoute(t *testing.T) {
	f := newCoverFixture(t) // no permissions at all
	contest := uuid.New()
	f.repo.published[contest] = false

	for _, path := range []string{"/contests/" + contest.String() + "/cover", "/contests/" + contest.String() + "/cover/file"} {
		rec := f.do(t, http.MethodGet, path, "", nil)
		if rec.Code != http.StatusForbidden && rec.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 403 or 404", path, rec.Code)
		}
	}
}
