package api

import (
	"net/http"
	"net/netip"

	"github.com/devrdn/db-contest/backend/internal/auth"
	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/platform/httpx"
	"github.com/devrdn/db-contest/backend/internal/rbac"
	"github.com/google/uuid"
)

// The people around a contest: the staff who run it and the participants who
// take part.
//
// The two are separate permissions on purpose — running a contest does not
// include appointing who else may run it, and managing a roster includes
// neither.

// ManagerResponse is one member of a contest's staff.
type ManagerResponse struct {
	UserID    string `json:"user_id"`
	Login     string `json:"login"`
	FullName  string `json:"full_name"`
	Role      string `json:"role"`
	GrantedAt string `json:"granted_at"`
}

type managerListResponse struct {
	Items []ManagerResponse `json:"items"`
}

func (h *ContestsHandler) listManagers(w http.ResponseWriter, r *http.Request) {
	id, ok := h.contestID(w, r)
	if !ok {
		return
	}

	staff, err := h.service.Managers(r.Context(), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}

	items := make([]ManagerResponse, 0, len(staff))
	for _, m := range staff {
		items = append(items, ManagerResponse{
			UserID:    m.UserID.String(),
			Login:     m.Login,
			FullName:  m.FullName,
			Role:      string(m.Role),
			GrantedAt: m.GrantedAt.UTC().Format(timeLayout),
		})
	}
	httpx.JSON(w, r, http.StatusOK, managerListResponse{Items: items})
}

type grantRequest struct {
	Role string `json:"role"`
}

func (h *ContestsHandler) grantManager(w http.ResponseWriter, r *http.Request) {
	contestID, ok := h.contestID(w, r)
	if !ok {
		return
	}
	userID, ok := h.memberID(w, r)
	if !ok {
		return
	}

	var req grantRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, codeInvalidRequest, err.Error())
		return
	}
	// Manager is the only role this endpoint exists to hand out, so an
	// unstated one means that; anything else, including "owner", is passed
	// through and refused by the service.
	role := rbac.ContestRole(req.Role)
	if role == "" {
		role = rbac.RoleManager
	}

	identity, _ := auth.IdentityFrom(r.Context())
	if err := h.service.GrantManager(r.Context(), identity.UserID, contestID, userID, role); err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.NoContent(w, r)
}

func (h *ContestsHandler) revokeManager(w http.ResponseWriter, r *http.Request) {
	contestID, ok := h.contestID(w, r)
	if !ok {
		return
	}
	userID, ok := h.memberID(w, r)
	if !ok {
		return
	}

	identity, _ := auth.IdentityFrom(r.Context())
	if err := h.service.RevokeManager(r.Context(), identity.UserID, contestID, userID); err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.NoContent(w, r)
}

// ParticipantResponse is one person's involvement in a contest.
type ParticipantResponse struct {
	// RegistrationID is what submissions, game instances and the query journal
	// all hang off; the client needs it everywhere but on this screen.
	RegistrationID string `json:"registration_id"`
	UserID         string `json:"user_id"`
	Login          string `json:"login"`
	FullName       string `json:"full_name"`
	Status         string `json:"status"`
	StartedAt      string `json:"started_at,omitempty"`
	FinishedAt     string `json:"finished_at,omitempty"`
	TotalScore     int    `json:"total_score"`
}

func toParticipantResponse(p contests.Participant) ParticipantResponse {
	return ParticipantResponse{
		RegistrationID: p.ID.String(),
		UserID:         p.UserID.String(),
		Login:          p.Login,
		FullName:       p.FullName,
		Status:         p.Status,
		StartedAt:      formatTime(p.StartedAt),
		FinishedAt:     formatTime(p.FinishedAt),
		TotalScore:     p.TotalScore,
	}
}

type participantListResponse struct {
	Items []ParticipantResponse `json:"items"`
	Total int                   `json:"total"`
}

func (h *ContestsHandler) listParticipants(w http.ResponseWriter, r *http.Request) {
	id, ok := h.contestID(w, r)
	if !ok {
		return
	}

	found, total, err := h.service.Participants(r.Context(), id, contests.ParticipantFilter{
		Query:  r.URL.Query().Get("q"),
		Status: r.URL.Query().Get("status"),
		Limit:  intParam(r, "limit"),
		Offset: intParam(r, "offset"),
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}

	items := make([]ParticipantResponse, 0, len(found))
	for _, p := range found {
		items = append(items, toParticipantResponse(p))
	}
	httpx.JSON(w, r, http.StatusOK, participantListResponse{Items: items, Total: total})
}

// addParticipantsRequest is a roster: identifiers, logins, or both.
//
// Logins because the practical input is pasted out of a spreadsheet, where
// what an organizer has is student numbers rather than internal identifiers.
type addParticipantsRequest struct {
	UserIDs []string `json:"user_ids"`
	Logins  []string `json:"logins"`
}

// importResponse reports a partial success honestly: one mistyped login must
// not reject the other three hundred rows, and the importer has to see which
// ones did not go in and why.
type importResponse struct {
	Added   int              `json:"added"`
	Skipped []skippedPayload `json:"skipped"`
}

type skippedPayload struct {
	Ref    string `json:"ref"`
	Reason string `json:"reason"`
}

func (h *ContestsHandler) addParticipants(w http.ResponseWriter, r *http.Request) {
	id, ok := h.contestID(w, r)
	if !ok {
		return
	}

	var req addParticipantsRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Error(w, r, http.StatusBadRequest, codeInvalidRequest, err.Error())
		return
	}

	userIDs := make([]uuid.UUID, 0, len(req.UserIDs))
	for _, raw := range req.UserIDs {
		parsed, err := uuid.Parse(raw)
		if err != nil {
			httpx.Error(w, r, http.StatusBadRequest, codeInvalidUserID,
				"The roster contains an identifier that is not valid")
			return
		}
		userIDs = append(userIDs, parsed)
	}

	identity, _ := auth.IdentityFrom(r.Context())
	result, err := h.service.AddParticipants(r.Context(), contests.AddParticipantsCommand{
		ActorID:   identity.UserID,
		ContestID: id,
		UserIDs:   userIDs,
		Logins:    req.Logins,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}

	skipped := make([]skippedPayload, 0, len(result.Skipped))
	for _, s := range result.Skipped {
		skipped = append(skipped, skippedPayload{Ref: s.Ref, Reason: s.Reason})
	}
	httpx.JSON(w, r, http.StatusOK, importResponse{Added: result.Added, Skipped: skipped})
}

func (h *ContestsHandler) removeParticipant(w http.ResponseWriter, r *http.Request) {
	contestID, ok := h.contestID(w, r)
	if !ok {
		return
	}
	userID, ok := h.memberID(w, r)
	if !ok {
		return
	}

	identity, _ := auth.IdentityFrom(r.Context())
	if err := h.service.RemoveParticipant(r.Context(), identity.UserID, contestID, userID); err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.NoContent(w, r)
}

func (h *ContestsHandler) disqualifyParticipant(w http.ResponseWriter, r *http.Request) {
	contestID, ok := h.contestID(w, r)
	if !ok {
		return
	}
	userID, ok := h.memberID(w, r)
	if !ok {
		return
	}

	identity, _ := auth.IdentityFrom(r.Context())
	if err := h.service.DisqualifyParticipant(r.Context(), identity.UserID, contestID, userID); err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.NoContent(w, r)
}

// enroll is a student signing themselves up.
//
// Open to any authenticated account: whether it is allowed is the contest's
// own decision — its enrollment type, its schedule and its network
// restriction — rather than a permission somebody has to be granted.
func (h *ContestsHandler) enroll(w http.ResponseWriter, r *http.Request) {
	id, ok := h.contestID(w, r)
	if !ok {
		return
	}

	identity, _ := auth.IdentityFrom(r.Context())
	enrolled, err := h.service.Enroll(r.Context(), contests.EnrollCommand{
		UserID:    identity.UserID,
		ContestID: id,
		Address:   clientAddr(r),
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusCreated, toParticipantResponse(enrolled))
}

// clientAddr is the address the network restriction is checked against.
//
// It comes from the resolver that knows which proxies are trusted, never from
// a header read here. An address that will not parse stays the zero value,
// which a restricted contest refuses: failing open would turn every proxy
// misconfiguration into an open door.
func clientAddr(r *http.Request) netip.Addr {
	addr, err := netip.ParseAddr(httpx.ClientIP(r))
	if err != nil {
		return netip.Addr{}
	}
	return addr
}
