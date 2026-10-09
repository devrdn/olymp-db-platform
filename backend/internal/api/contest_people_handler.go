package api

import (
	"net/http"

	"github.com/devrdn/db-contest/backend/internal/auth"
	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/platform/httpx"
	"github.com/devrdn/db-contest/backend/internal/rbac"
	"github.com/google/uuid"
)

// The people around a contest: staff and participants. Separate permissions:
// running a contest does not include appointing who else runs it, and managing
// a roster includes neither.

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
	id, ok := contestIDFrom(w, r)
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
	contestID, ok := contestIDFrom(w, r)
	if !ok {
		return
	}
	userID, ok := h.memberID(w, r)
	if !ok {
		return
	}

	var req grantRequest
	if !decodeBody(w, r, &req) {
		return
	}
	// An unstated role means manager, the only one this endpoint hands out;
	// anything else, "owner" included, is refused by the service.
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
	contestID, ok := contestIDFrom(w, r)
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
	// reference.
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
	id, ok := contestIDFrom(w, r)
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

// addParticipantsRequest is a roster of identifiers, logins, or both. Logins
// because rosters are pasted from spreadsheets of student numbers.
type addParticipantsRequest struct {
	UserIDs []string `json:"user_ids"`
	Logins  []string `json:"logins"`
}

// importResponse reports partial success: one mistyped login must not reject
// the other rows, and the importer must see which failed and why.
type importResponse struct {
	Added   int              `json:"added"`
	Skipped []skippedPayload `json:"skipped"`
}

type skippedPayload struct {
	Ref    string `json:"ref"`
	Reason string `json:"reason"`
}

func (h *ContestsHandler) addParticipants(w http.ResponseWriter, r *http.Request) {
	id, ok := contestIDFrom(w, r)
	if !ok {
		return
	}

	var req addParticipantsRequest
	if !decodeBody(w, r, &req) {
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

// PersonResponse is what a picker needs to tell two accounts apart: name,
// login, email and the identifier the grant and add endpoints take. Not
// UserResponse: no status, roles or password state.
//
// The email is an accepted trade-off: this search runs under
// participant.manage, so any contest's staff can see every account's email, but
// neither a login nor a full name alone reliably tells two people apart (see
// contests.Person). Email is omitted when the account has none, so a missing
// address is not read as a blank one.
type PersonResponse struct {
	UserID   string `json:"user_id"`
	Login    string `json:"login"`
	FullName string `json:"full_name"`
	Email    string `json:"email,omitempty"`
}

func toPersonResponse(p contests.Person) PersonResponse {
	return PersonResponse{UserID: p.UserID.String(), Login: p.Login, FullName: p.FullName, Email: p.Email}
}

type directoryResponse struct {
	Items []PersonResponse `json:"items"`
}

// directorySearch is the picker behind the staff and participant forms: a few
// characters of a login, name or email find the person meant. Not scoped to the
// URL's contest beyond the route's permission check: any account may be staffed
// on or join any contest.
func (h *ContestsHandler) directorySearch(w http.ResponseWriter, r *http.Request) {
	found, err := h.service.SearchPeople(r.Context(), r.URL.Query().Get("q"), intParam(r, "limit"))
	if err != nil {
		h.fail(w, r, err)
		return
	}

	items := make([]PersonResponse, 0, len(found))
	for _, p := range found {
		items = append(items, toPersonResponse(p))
	}
	httpx.JSON(w, r, http.StatusOK, directoryResponse{Items: items})
}

func (h *ContestsHandler) removeParticipant(w http.ResponseWriter, r *http.Request) {
	contestID, ok := contestIDFrom(w, r)
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
	contestID, ok := contestIDFrom(w, r)
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

// enroll signs the caller up. Open to any authenticated account; the contest's
// enrollment type, schedule and network restriction decide.
func (h *ContestsHandler) enroll(w http.ResponseWriter, r *http.Request) {
	id, ok := contestIDFrom(w, r)
	if !ok {
		return
	}

	identity, _ := auth.IdentityFrom(r.Context())
	enrolled, err := h.service.Enroll(r.Context(), contests.EnrollCommand{
		UserID:    identity.UserID,
		ContestID: id,
		Address:   clientAddress(r),
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusCreated, toParticipantResponse(enrolled))
}
