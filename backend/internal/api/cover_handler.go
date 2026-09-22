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

// The two budgets this feature spends, and the slack the envelope gets.
const (
	// CoverUploadsPerMinute is per account, and small on purpose. Uploading
	// a cover is a deliberate act an organiser performs once per olympiad;
	// the budget exists because the work behind it is a decode and two
	// resamples of a photograph, which is the most expensive thing a
	// non-participant can ask this service to do. Refused attempts count
	// too (CLAUDE.md, security rule 13): a limit that only counts the
	// successes is a limit on the wrong thing, since a refused upload has
	// already been read off the socket and sniffed.
	CoverUploadsPerMinute = 10

	// PublicCoverReadsPerMinute is per address. Generous, because one visit
	// to the front page asks for every card's picture at once and a school
	// puts a hundred browsers behind one address — and because every answer
	// carries a year of immutable caching, so the second visit asks for
	// none of them.
	PublicCoverReadsPerMinute = 300

	coverWindow = time.Minute

	// coverEnvelopeSlack is what the multipart framing may add on top of the
	// picture: the boundaries, the part headers and the credit line. The
	// ceiling on the socket has to be the domain's limit plus this, or a
	// picture of exactly covers.MaxUploadBytes could never be uploaded at
	// all.
	coverEnvelopeSlack = 64 << 10

	// coverFileField and coverAttributionField are the two parts of the form.
	coverFileField        = "file"
	coverAttributionField = "attribution"

	// maxSizeParamLen bounds the `size` query parameter before it is parsed
	// (CLAUDE.md, security rule 2). It never reaches storage, but it arrives
	// in a request line bounded only by the server's header limit, and a
	// four-digit number is every value this service will ever answer to.
	maxSizeParamLen = 8
)

// CoverStore is the slice of the covers service these endpoints need.
//
// Declared here and narrow, as every other handler's store is: internal/api
// adapts HTTP to the domain and has no business holding methods it never
// calls.
type CoverStore interface {
	Upload(ctx context.Context, contestID uuid.UUID, actorID uuid.UUID, src io.Reader, attribution string) (covers.Cover, error)
	Remove(ctx context.Context, contestID uuid.UUID) error
	Public(ctx context.Context, contestID uuid.UUID) (covers.Cover, error)
	Read(ctx context.Context, hash string, size int) ([]byte, string, error)
}

// CoverHandler serves a contest's picture: the organiser's two writes and the
// visitor's one read.
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

// Mount registers the three routes.
//
// The public read is under /public/, beside the landing page's other two and
// away from /contests, whose every other route requires a session: two access
// rules on one path is the mistake nobody notices later. The writes sit on
// /contests with the rest of what editing a contest means.
//
// The API serves the bytes rather than the reverse proxy publishing the
// directory (design spec §1): the path to a file is the store's business, and
// the cache headers and the check that the contest is published are the
// application's.
func (h *CoverHandler) Mount(r chi.Router) {
	r.Get("/public/contests/{"+contestIDParam+"}/cover", h.public)

	r.Group(func(r chi.Router) {
		r.Use(h.mw.Authenticate, h.mw.RequireContestPermission(rbac.PermissionContestEdit))
		r.Put("/contests/{"+contestIDParam+"}/cover", h.upload)
		r.Delete("/contests/{"+contestIDParam+"}/cover", h.remove)
	})
}

// coverResponse is what an organiser gets back: enough to show the picture
// they have just uploaded without asking for it again.
type coverResponse struct {
	Hash        string `json:"hash"`
	Attribution string `json:"attribution"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
}

// upload replaces a contest's cover.
func (h *CoverHandler) upload(w http.ResponseWriter, r *http.Request) {
	contestID, ok := h.contestID(w, r)
	if !ok {
		return
	}
	identity, _ := auth.IdentityFrom(r.Context())

	// Before the body is touched at all. Reading eight mebibytes off the
	// socket and decoding a photograph in order to then refuse the request is
	// the work this budget exists to stop.
	if !h.admitUpload(w, r, identity.UserID) {
		return
	}

	// The ceiling on the socket, not on a decoded value: the declared length
	// of a body is the sender's claim, not a fact (CLAUDE.md, security rule
	// 12). covers.Process bounds the picture inside the envelope as well, and
	// this bounds the envelope.
	r.Body = http.MaxBytesReader(w, r.Body, covers.MaxUploadBytes+coverEnvelopeSlack)
	if err := r.ParseMultipartForm(covers.MaxUploadBytes); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			// The transport's ceiling tripped rather than the domain's. It is
			// the same thing to the person uploading, and telling the two
			// apart would mean explaining our two limits.
			h.fail(w, r, covers.ErrTooLarge)
			return
		}
		httpx.Error(w, r, http.StatusBadRequest, codeInvalidRequest,
			"The request is not a multipart form with a "+coverFileField+" and an "+coverAttributionField)
		return
	}
	// The parts are held in memory under the ceiling above, but a form that
	// spilled anything to disk must not leave it there.
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

	httpx.JSON(w, r, http.StatusOK, coverResponse{
		Hash: cover.Hash, Attribution: cover.Attribution,
		Width: cover.Width, Height: cover.Height,
	})
}

// remove takes a contest's cover away, leaving it the drawn one.
//
// The files stay on the volume for the sweep (covers.Service.Remove): a file
// system refusing a delete must not be able to fail this request.
func (h *CoverHandler) remove(w http.ResponseWriter, r *http.Request) {
	contestID, ok := h.contestID(w, r)
	if !ok {
		return
	}
	if err := h.covers.Remove(r.Context(), contestID); err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.NoContent(w, r)
}

// public serves one rendition to a visitor with no session.
//
// The caching is decided by the address, and the address has two forms:
//
//   - with `v=<hash>` it names one exact file, so it is answered with a year
//     of immutable caching — the picture at that address can never change,
//     and a replaced cover is a different address the page links to instead.
//     This is the form the pages use, the same way the installation's own
//     pictures carry their hash (`imageHref`);
//   - without it, the address is only "this contest's cover", which changes
//     the moment an organiser replaces the picture. A minute, and an ETag to
//     make the revalidation free. An earlier version of this handler sent a
//     year of `immutable` here too, on a comment's word that the address
//     carried the hash — it does not, and a replaced cover would have stayed
//     invisible to everybody who had seen the old one.
//
// The ETag names the file rather than the cover, and:
//   - `nosniff`, so a browser uses the type the store named rather than
//     guessing. Unlike the installation's own pictures, no download
//     disposition is set: these bytes are always a JPEG this process encoded
//     (covers.Process re-encodes whatever arrived), so there is no document
//     here for a browser to render, and a cover is a picture people
//     legitimately open by its own address.
func (h *CoverHandler) public(w http.ResponseWriter, r *http.Request) {
	// Before the database and before the volume, and counting refusals.
	if !h.admitPublic(w, r) {
		return
	}
	contestID, err := uuid.Parse(chi.URLParam(r, contestIDParam))
	if err != nil {
		// Not codeInvalidContestID: this route is open to the whole internet
		// and answers nothing about what does or does not exist.
		h.notFound(w, r)
		return
	}

	cover, err := h.covers.Public(r.Context(), contestID)
	if err != nil {
		h.fail(w, r, err)
		return
	}

	size := requestedCoverSize(r)
	// Read before any header is set. A refusal — a width nothing is stored
	// at, or a volume that did not answer — must not be the thing that
	// carries a year of caching, and setting the headers first is how that
	// happens: the failure is written out with whatever was already on the
	// response.
	body, contentType, err := h.covers.Read(r.Context(), cover.Hash, size)
	if err != nil {
		h.fail(w, r, err)
		return
	}

	// The ETag names the exact file, which is the hash and the width
	// together: the two renditions of one cover are one hash and two
	// different pictures, and an ETag of the hash alone would let a cache
	// answer a request for 1600 with the 800 it already has.
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
	_, _ = w.Write(body)
}

// coverCacheControl answers how long this address may be kept.
//
// An address that names the file — `v=<hash>` — can be kept forever, because
// nothing at it will ever be different. An address that names only the
// contest is worth a minute: it is what an organiser's replacement has to
// travel through, and a picture nobody can refresh is worse than a picture
// fetched again.
func coverCacheControl(r *http.Request, hash string) string {
	if r.URL.Query().Get("v") == hash {
		return "public, max-age=31536000, immutable"
	}
	return "public, max-age=60"
}

// requestedCoverSize is the width the visitor asked for.
//
// A missing parameter is the largest rendition: a caller that says nothing
// about what it can use gets the whole picture rather than the small one. A
// parameter that is present but is not one of covers.Sizes is passed through
// unchanged and refused by the service as a file that does not exist, which
// is what it is — deciding that here as well would be the same rule in two
// places.
//
// A parameter too long to be a width is refused rather than ignored, and the
// two are different: a caller that asked for something must not be answered
// with something else. The bound is on the parameter before it is parsed
// (CLAUDE.md, security rule 2), because the request line it arrives in is
// bounded only by the server's own header limit.
func requestedCoverSize(r *http.Request) int {
	raw := r.URL.Query().Get("size")
	if raw == "" {
		return covers.Sizes[0]
	}
	// -1 is a width nothing is stored at, which is the whole answer for
	// anything that is not a number this service could have written.
	if len(raw) > maxSizeParamLen {
		return -1
	}
	size, err := strconv.Atoi(raw)
	if err != nil {
		return -1
	}
	return size
}

// contestID reads the identifier both writes act on.
func (h *CoverHandler) contestID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, contestIDParam))
	if err != nil {
		httpx.Error(w, r, http.StatusBadRequest, auth.CodeInvalidContestID, "Contest identifier is not valid")
		return uuid.Nil, false
	}
	return id, true
}

// admitUpload spends one upload of the account's budget.
//
// Keyed by the account rather than the address, and deliberately so: the
// route is behind a session and a contest permission, so the caller is a
// known organiser, and grouping a whole university's staff under one address
// would let one person's mistake stop their colleagues working.
func (h *CoverHandler) admitUpload(w http.ResponseWriter, r *http.Request, userID uuid.UUID) bool {
	return h.admit(w, r, "cover_upload:user:"+userID.String(), CoverUploadsPerMinute, codeCoverTooOften,
		"This account has uploaded covers too often; wait before trying again")
}

// admitPublic spends one read of the address's budget.
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

// fail turns one of the domain's sentinels into the code the interface
// translates (CLAUDE.md, security rule 1). Anything this switch cannot name
// is the database or the volume, which is a 500 and a line in the log.
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
			// A visitor who closed the tab before the picture arrived. An
			// ordinary event on a page people open and close, not an outage,
			// and there is no longer a connection to write a body to.
			h.log.DebugContext(r.Context(), "the visitor left before the cover was served", "error", ctxErr)
			return
		}
		h.log.ErrorContext(r.Context(), "a contest cover could not be served", "error", err)
		httpx.Error(w, r, http.StatusInternalServerError, httpx.CodeInternalError, "Internal server error")
	}
}
