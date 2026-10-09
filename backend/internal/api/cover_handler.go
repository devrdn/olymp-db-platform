package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/devrdn/db-contest/backend/internal/auth"
	"github.com/devrdn/db-contest/backend/internal/covers"
	"github.com/devrdn/db-contest/backend/internal/platform/httpx"
	"github.com/devrdn/db-contest/backend/internal/rbac"
)

// The two budgets the cover routes spend, and the envelope's slack.
const (
	// CoverUploadsPerMinute is per account. An upload costs a decode and two
	// resamples, the most expensive thing a non-participant can ask for.
	// Refused attempts count too (CLAUDE.md rule 13): a refused upload has
	// already been read and sniffed.
	CoverUploadsPerMinute = 10

	// PublicCoverReadsPerMinute is per address and generous: one front-page
	// visit asks for every card's picture, a school puts a hundred browsers
	// behind one address, and repeat visits hit the immutable cache.
	PublicCoverReadsPerMinute = 300

	coverWindow = time.Minute

	// coverEnvelopeSlack is what the multipart framing adds on top of the
	// picture. The socket ceiling is the domain limit plus this, or a picture
	// of exactly covers.MaxUploadBytes could never be uploaded.
	coverEnvelopeSlack = 64 << 10

	coverFileField        = "file"
	coverAttributionField = "attribution"

	// maxSizeParamLen bounds the `size` query parameter before it is parsed
	// (CLAUDE.md rule 2); four digits cover every width this service answers
	// to.
	maxSizeParamLen = 8
)

// CoverStore is the slice of the covers service these endpoints need.
type CoverStore interface {
	Upload(ctx context.Context, contestID uuid.UUID, actorID uuid.UUID, src io.Reader, attribution string) (covers.Cover, error)
	Remove(ctx context.Context, contestID uuid.UUID) error
	Public(ctx context.Context, contestID uuid.UUID) (covers.Cover, error)
	// ByContest is the staff's read: unlike Public, it answers for a draft too.
	ByContest(ctx context.Context, contestID uuid.UUID) (covers.Cover, error)
	Read(ctx context.Context, hash string, size int) ([]byte, string, error)
}

// CoverHandler serves a contest's picture: the organiser's writes and reads,
// and the visitor's one read.
type CoverHandler struct {
	covers  CoverStore
	limiter *auth.Limiter
	mw      *auth.Middleware
	log     *slog.Logger
}

// NewCoverHandler assembles the cover endpoints.
func NewCoverHandler(store CoverStore, limiter *auth.Limiter, mw *auth.Middleware, log *slog.Logger) *CoverHandler {
	return &CoverHandler{covers: store, limiter: limiter, mw: mw, log: log}
}

// Mount registers the routes. The public read lives under /public/, away from
// /contests where every route requires a session, so one path never has two
// access rules. The API serves the bytes, not the proxy: the cache headers and
// the published check are the application's.
func (h *CoverHandler) Mount(r chi.Router) {
	r.Get("/public/contests/{"+contestIDParam+"}/cover", h.public)

	// The staff read answers for a draft, which the public route refuses;
	// without it an organiser could not see, or remove, their own contest's
	// cover.
	r.Group(func(r chi.Router) {
		r.Use(h.mw.Authenticate, h.mw.RequireContestPermission(rbac.PermissionContestView))
		r.Get("/contests/{"+contestIDParam+"}/cover", h.staff)
		r.Get("/contests/{"+contestIDParam+"}/cover/file", h.staffFile)
	})

	r.Group(func(r chi.Router) {
		r.Use(h.mw.Authenticate, h.mw.RequireContestPermission(rbac.PermissionContestEdit))
		r.Put("/contests/{"+contestIDParam+"}/cover", h.upload)
		r.Delete("/contests/{"+contestIDParam+"}/cover", h.remove)
	})
}

// toCoverResponse is enough for an organiser to show the picture just uploaded
// without fetching it again.
func toCoverResponse(cover covers.Cover) coverResponse {
	return coverResponse{
		Hash: cover.Hash, Attribution: cover.Attribution,
		Width: cover.Width, Height: cover.Height,
	}
}

type coverResponse struct {
	Hash        string `json:"hash"`
	Attribution string `json:"attribution"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
}

func (h *CoverHandler) upload(w http.ResponseWriter, r *http.Request) {
	contestID, ok := contestIDFrom(w, r)
	if !ok {
		return
	}
	identity, _ := auth.IdentityFrom(r.Context())

	// Before the body is touched: reading and decoding a photograph only to
	// refuse it is what this budget prevents.
	if !h.admitUpload(w, r, identity.UserID) {
		return
	}

	// The ceiling is on the socket, because a declared length is only a claim
	// (CLAUDE.md rule 12). This bounds the envelope; covers.Process bounds the
	// picture inside it.
	r.Body = http.MaxBytesReader(w, r.Body, covers.MaxUploadBytes+coverEnvelopeSlack)
	// #nosec G120 -- the body is bounded by the MaxBytesReader above; the
	// scanner cannot see that line.
	if err := r.ParseMultipartForm(covers.MaxUploadBytes); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			// The transport ceiling tripped; to the uploader it is the same
			// refusal as the domain's.
			h.fail(w, r, covers.ErrTooLarge)
			return
		}
		httpx.Error(w, r, http.StatusBadRequest, codeInvalidRequest,
			"The request is not a multipart form with a "+coverFileField+" and an "+coverAttributionField)
		return
	}
	// A form that spilled to disk must not leave files behind.
	defer func() { _ = r.MultipartForm.RemoveAll() }()

	file, _, err := r.FormFile(coverFileField)
	if err != nil {
		httpx.Error(w, r, http.StatusBadRequest, codeInvalidRequest,
			"The form carries no "+coverFileField+" part")
		return
	}
	defer func() { _ = file.Close() }()

	cover, err := h.covers.Upload(r.Context(), contestID, identity.UserID, file,
		r.FormValue(coverAttributionField))
	if err != nil {
		h.fail(w, r, err)
		return
	}

	httpx.JSON(w, r, http.StatusOK, toCoverResponse(cover))
}

// remove takes a contest's cover away, leaving the drawn one. The files stay
// for the sweep (covers.Service.Remove), so a failed delete on disk cannot fail
// this request.
func (h *CoverHandler) remove(w http.ResponseWriter, r *http.Request) {
	contestID, ok := contestIDFrom(w, r)
	if !ok {
		return
	}
	if err := h.covers.Remove(r.Context(), contestID); err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.NoContent(w, r)
}

// staff answers which cover this contest has, whatever its status.
func (h *CoverHandler) staff(w http.ResponseWriter, r *http.Request) {
	contestID, ok := contestIDFrom(w, r)
	if !ok {
		return
	}
	cover, err := h.covers.ByContest(r.Context(), contestID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, toCoverResponse(cover))
}

// staffFile serves that cover's bytes to staff. Separate from the public route
// because it answers for a draft. `private`, so a shared cache never holds an
// unpublished contest's picture; a short max-age, because an organiser checks a
// replacement at once.
func (h *CoverHandler) staffFile(w http.ResponseWriter, r *http.Request) {
	contestID, ok := contestIDFrom(w, r)
	if !ok {
		return
	}
	cover, err := h.covers.ByContest(r.Context(), contestID)
	if err != nil {
		h.fail(w, r, err)
		return
	}

	body, contentType, err := h.covers.Read(r.Context(), cover.Hash, requestedCoverSize(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	header := w.Header()
	header.Set("Cache-Control", "private, max-age=30")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Content-Type", contentType)
	w.WriteHeader(http.StatusOK)
	// #nosec G705 -- not a document: covers.Process re-encoded these bytes as a
	// JPEG, the type comes from filestore's closed set of extensions, and the
	// answer carries nosniff.
	_, _ = w.Write(body)
}

// public serves one rendition to a visitor with no session. The address decides
// the caching:
//
//   - with `v=<hash>` it names one exact file, so it is cached immutably for a
//     year; a replaced cover gets a different address. This is the form the
//     pages use.
//   - without it, the address means "this contest's cover", which changes when
//     an organiser replaces it: a minute, with an ETag to make revalidation
//     free.
//
// `nosniff` makes the browser use the stored type. No download disposition is
// set: the bytes are always a JPEG this process encoded, and a cover is
// legitimately opened by its own address.
func (h *CoverHandler) public(w http.ResponseWriter, r *http.Request) {
	// Before the database and the volume, counting refusals.
	if !h.admitPublic(w, r) {
		return
	}
	contestID, err := uuid.Parse(chi.URLParam(r, contestIDParam))
	if err != nil {
		// Not codeInvalidContestID: this public route reveals nothing about
		// what exists.
		h.notFound(w, r)
		return
	}

	cover, err := h.covers.Public(r.Context(), contestID)
	if err != nil {
		h.fail(w, r, err)
		return
	}

	size := requestedCoverSize(r)
	// Read before setting any header, so a failure is never written out
	// carrying a year of caching.
	body, contentType, err := h.covers.Read(r.Context(), cover.Hash, size)
	if err != nil {
		h.fail(w, r, err)
		return
	}

	// The ETag names the hash and the width: two renditions share a hash, and
	// the hash alone would let a cache answer 1600 with 800.
	etag := `"` + cover.Hash + "-" + strconv.Itoa(size) + `"`
	header := w.Header()
	header.Set("Cache-Control", coverCacheControl(r, cover.Hash))
	header.Set("ETag", etag)
	header.Set("X-Content-Type-Options", "nosniff")

	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	header.Set("Content-Type", contentType)
	w.WriteHeader(http.StatusOK)
	// #nosec G705 -- not a document: covers.Process re-encoded these bytes as a
	// JPEG, the type comes from filestore's closed set of extensions, and the
	// answer carries nosniff.
	_, _ = w.Write(body)
}

// coverCacheControl answers how long this address may be kept: forever for
// `v=<hash>`, which never changes, and a minute for the contest-only address,
// which a replacement must travel through.
func coverCacheControl(r *http.Request, hash string) string {
	if r.URL.Query().Get("v") == hash {
		return "public, max-age=31536000, immutable"
	}
	return "public, max-age=60"
}

// requestedCoverSize is the width the visitor asked for. Missing means the
// largest rendition. A value not in covers.Sizes passes through and the service
// refuses it as a missing file, so the rule lives in one place. An overlong
// value is refused, not ignored, and is bounded before parsing (CLAUDE.md rule
// 2).
func requestedCoverSize(r *http.Request) int {
	raw := r.URL.Query().Get("size")
	if raw == "" {
		return covers.Sizes[0]
	}
	// -1 is a width nothing is stored at.
	if len(raw) > maxSizeParamLen {
		return -1
	}
	size, err := strconv.Atoi(raw)
	if err != nil {
		return -1
	}
	return size
}

// admitUpload spends one upload of the account's budget. Keyed by account, not
// address: the caller is a known organiser, and an address key would let one
// person's mistake block a whole university's staff.
func (h *CoverHandler) admitUpload(w http.ResponseWriter, r *http.Request, userID uuid.UUID) bool {
	return h.admit(w, r, "cover_upload:user:"+userID.String(), CoverUploadsPerMinute, codeCoverTooOften,
		"This account has uploaded covers too often; wait before trying again")
}

func (h *CoverHandler) admitPublic(w http.ResponseWriter, r *http.Request) bool {
	return h.admit(w, r, "cover_read:ip:"+addressKey(r), PublicCoverReadsPerMinute, codePublicTooOften,
		"Covers are being asked for too often; wait before asking again")
}

func (h *CoverHandler) admit(w http.ResponseWriter, r *http.Request, subject string, limit int, code httpx.Code, message string) bool {
	allowed, err := h.limiter.Allow(r.Context(), subject, limit, coverWindow)
	if err != nil {
		h.log.ErrorContext(r.Context(), "could not check the cover rate limit", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternalError, "Internal server error")
		return false
	}
	if !allowed {
		w.Header().Set("Retry-After", strconv.Itoa(int(coverWindow/time.Second)))
		httpx.Error(w, r, http.StatusTooManyRequests, code, message)
		return false
	}
	return true
}

func (h *CoverHandler) notFound(w http.ResponseWriter, r *http.Request) {
	httpx.Error(w, r, http.StatusNotFound, codeNotFound, "Resource not found")
}

// fail turns the domain's sentinels into codes (CLAUDE.md rule 1). Anything
// else is the database or the volume: a 500 and a log line.
func (h *CoverHandler) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, covers.ErrNotFound):
		h.notFound(w, r)
	case errors.Is(err, covers.ErrTooLarge):
		httpx.Error(w, r, http.StatusBadRequest, codeCoverTooLarge,
			"The uploaded file is heavier than a cover may be")
	case errors.Is(err, covers.ErrImageKind):
		httpx.Error(w, r, http.StatusBadRequest, codeCoverKind, err.Error())
	case errors.Is(err, covers.ErrImageTooLarge):
		httpx.Error(w, r, http.StatusBadRequest, codeCoverDimensions, err.Error())
	case errors.Is(err, covers.ErrAttributionRequired):
		httpx.Error(w, r, http.StatusBadRequest, codeCoverAttributionRequired, err.Error())
	case errors.Is(err, covers.ErrAttributionTooLong):
		httpx.Error(w, r, http.StatusBadRequest, codeCoverAttributionTooLong, err.Error())
	default:
		if ctxErr := r.Context().Err(); ctxErr != nil {
			// A visitor who closed the tab: not an outage, and there is no
			// connection left to write to.
			h.log.DebugContext(r.Context(), "the visitor left before the cover was served", "error", ctxErr)
			return
		}
		h.log.ErrorContext(r.Context(), "a contest cover could not be served", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternalError, "Internal server error")
	}
}
