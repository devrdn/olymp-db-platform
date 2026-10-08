// Package conteststest provides in-memory implementations of the storage the
// contests package declares, plus a fixture that assembles a service from
// them.
//
// It also publishes the contracts every implementation of those interfaces
// answers to (the *_contract.go files): the in-memory stores here run them, and
// so does internal/postgres against the real repositories, which is what keeps
// the stores the service tests trust honest about what production does.
//
// It exists so the contest rules — the lifecycle, the publish gate, the
// enrollment and staffing rules — are exercised against real behaviour rather
// than assertions on mock calls, and without a database. The HTTP handlers use
// the same fixture, so what they are tested against is the real service.
//
// None of these types is safe for concurrent use: a fixture belongs to one
// test.
package conteststest

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/rbac"
	"github.com/devrdn/db-contest/backend/internal/users"
	"github.com/google/uuid"
)

// Contests is an in-memory contests.Repository, held to the same answers as
// postgres.Contests by ContestRepositoryContract, which both run.
type Contests struct {
	byID map[uuid.UUID]contests.Contest
	// order preserves insertion order, so listings are deterministic.
	order []uuid.UUID
	// racesTo stages one concurrent status change; see SetStatusRaces.
	racesTo string
	// Clock is what Create stamps CreatedAt and UpdatedAt with, and what
	// Update and SetStatus stamp UpdatedAt with, the way the table's default
	// and the statements stamp them with the database's own now(). Nil leaves
	// them as they are.
	Clock func() time.Time
	// managers and registrations answer the two filters that depend on other
	// tables, Filter.ManagedBy and Filter.VisibleTo; see Rosters.
	managers      *Managers
	registrations *Registrations
}

var _ contests.Repository = (*Contests)(nil)

// NewContests returns an empty contest store.
func NewContests() *Contests {
	return &Contests{byID: map[uuid.UUID]contests.Contest{}}
}

// Rosters tells List where to look up who staffs a contest and who is
// registered for it, which the real listing joins from the managers and the
// registrations tables. Until it is called nobody staffs anything and
// nobody is registered, so a ManagedBy listing is empty and a VisibleTo
// listing holds only the open contests.
func (r *Contests) Rosters(managers *Managers, registrations *Registrations) {
	r.managers, r.registrations = managers, registrations
}

// Count reports how many contests are stored.
func (r *Contests) Count() int { return len(r.byID) }

// Exists reports whether the contest is stored: what the stores of the rows
// that hang off a contest ask in place of the real schema's foreign key.
func (r *Contests) Exists(id uuid.UUID) bool {
	_, ok := r.byID[id]
	return ok
}

// store keeps a copy of the contest, so nothing the caller does to its own
// value afterwards reaches the stored one.
func (r *Contests) store(c contests.Contest) {
	if _, exists := r.byID[c.ID]; !exists {
		r.order = append(r.order, c.ID)
	}
	r.byID[c.ID] = cloneContest(c)
}

func (r *Contests) now() (time.Time, bool) {
	if r.Clock == nil {
		return time.Time{}, false
	}
	return r.Clock(), true
}

// Put stores a contest as given, for tests that need a particular state.
func (r *Contests) Put(c contests.Contest) contests.Contest {
	if c.ID == uuid.Nil {
		c.ID = uuid.New()
	}
	// The column's own default (migration 30), so a seed that predates the
	// leaderboard reads back the way a stored row does.
	if c.LeaderboardNames == "" {
		c.LeaderboardNames = contests.LeaderboardNamesLogin
	}
	// Same reasoning, for the column migration 31 adds: a seed that never
	// mentions the ICPC penalty reads back the way a stored row does, with
	// the column's own default of 20 rather than a bare Go zero value.
	if c.ICPCPenaltyMin == 0 {
		c.ICPCPenaltyMin = contests.DefaultICPCPenaltyMin
	}
	r.store(c)
	return c
}

// cloneContest copies everything a caller could write to through a contest.
// The real repository hands out rows it has just read and keeps nothing of
// what it is given, so a caller editing a contest it holds has not changed
// the stored one until it saves.
func cloneContest(c contests.Contest) contests.Contest {
	c.DurationMin = clonePointer(c.DurationMin)
	c.StartsAt = clonePointer(c.StartsAt)
	c.EndsAt = clonePointer(c.EndsAt)
	c.AllowedCIDRs = slices.Clone(c.AllowedCIDRs)
	c.Settings.EnrollmentDeadline = clonePointer(c.Settings.EnrollmentDeadline)
	c.LeaderboardFreezeMin = clonePointer(c.LeaderboardFreezeMin)
	c.LeaderboardRevealedAt = clonePointer(c.LeaderboardRevealedAt)
	c.Languages = slices.Clone(c.Languages)
	if c.Translations != nil {
		translations := make(map[string]contests.Translation, len(c.Translations))
		for lang, t := range c.Translations {
			translations[lang] = t
		}
		c.Translations = translations
	}
	return c
}

func clonePointer[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// served is what a read hands out: a copy of the stored contest with its
// languages in the order the real projection gives them, the default first
// and the rest by code.
func served(c contests.Contest) contests.Contest {
	c = cloneContest(c)
	slices.SortFunc(c.Languages, func(a, b contests.ContestLanguage) int {
		if a.IsDefault != b.IsDefault {
			if a.IsDefault {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Code, b.Code)
	})
	return c
}

// Create stores the contest's own columns only: its identity and timestamps
// are assigned here, and its languages and titles have operations of their
// own, so whatever the caller's value carries of either is dropped, as the
// real insert does not write it. The defaults Put applies for a seed are not
// applied either: a stored zero is a zero.
func (r *Contests) Create(_ context.Context, c contests.Contest) (contests.Contest, error) {
	c.ID = uuid.New()
	c.Languages, c.Translations = nil, nil
	c.LeaderboardRevealedAt = nil
	c.CoverHash, c.CoverAttribution = "", ""
	c.CreatedAt, c.UpdatedAt = time.Time{}, time.Time{}
	if now, ok := r.now(); ok {
		c.CreatedAt, c.UpdatedAt = now, now
	}
	r.store(c)
	return served(c), nil
}

func (r *Contests) ByID(_ context.Context, id uuid.UUID) (contests.Contest, error) {
	c, ok := r.byID[id]
	if !ok {
		return contests.Contest{}, contests.ErrNotFound
	}
	return served(c), nil
}

// List filters the way the real query does: the title search, the status, the
// contests a user staffs, and what a participant may see. Newest start first,
// contests with no start last, and the most recently created first among those
// that start together.
func (r *Contests) List(ctx context.Context, f contests.Filter) ([]contests.Contest, int, error) {
	var matched []contests.Contest
	// Walked newest first, so that the stable sort below leaves the later
	// insertion first among contests it cannot tell apart.
	for _, id := range slices.Backward(r.order) {
		c := r.byID[id]
		if f.Status != "" && c.Status != f.Status {
			continue
		}
		if f.Query != "" && !matchesTitle(c, f.Query) {
			continue
		}
		if f.ManagedBy != uuid.Nil && !r.staffs(f.ManagedBy, c.ID) {
			continue
		}
		if f.VisibleTo != uuid.Nil && !r.visibleTo(f.VisibleTo, c) {
			continue
		}
		if f.Enrolled != nil && *f.Enrolled != r.registered(f.VisibleTo, c.ID) {
			continue
		}
		matched = append(matched, served(c))
	}
	slices.SortStableFunc(matched, func(a, b contests.Contest) int {
		switch {
		case a.StartsAt != nil && b.StartsAt != nil && !a.StartsAt.Equal(*b.StartsAt):
			return b.StartsAt.Compare(*a.StartsAt)
		case a.StartsAt == nil && b.StartsAt != nil:
			return 1
		case a.StartsAt != nil && b.StartsAt == nil:
			return -1
		}
		return b.CreatedAt.Compare(a.CreatedAt)
	})

	total := len(matched)
	if f.Offset >= total {
		return nil, total, nil
	}
	end := min(f.Offset+f.Limit, total)
	return matched[f.Offset:end], total, nil
}

// staffs reports whether the user owns or manages the contest.
func (r *Contests) staffs(user, contest uuid.UUID) bool {
	if r.managers == nil {
		return false
	}
	return slices.ContainsFunc(r.managers.byContest[contest], func(m contests.Manager) bool { return m.UserID == user })
}

// registered reports whether the user is registered for the contest, in any
// status of the contest and of the registration.
func (r *Contests) registered(user, contest uuid.UUID) bool {
	if r.registrations == nil || user == uuid.Nil {
		return false
	}
	for _, p := range r.registrations.byID {
		if p.ContestID == contest && p.UserID == user {
			return true
		}
	}
	return false
}

// visibleTo is what a participant may see: the contests they are on, once
// those are no longer drafts, plus open ones still taking signups. A draft
// is nobody's business but its authors'.
func (r *Contests) visibleTo(user uuid.UUID, c contests.Contest) bool {
	switch c.Status {
	case contests.StatusPublished, contests.StatusRunning:
		return c.Enrollment == contests.EnrollmentOpen || r.registered(user, c.ID)
	case contests.StatusFinished:
		return r.registered(user, c.ID)
	}
	return false
}

func matchesTitle(c contests.Contest, query string) bool {
	for _, t := range c.Translations {
		if strings.Contains(strings.ToLower(t.Title), strings.ToLower(query)) {
			return true
		}
	}
	return false
}

// Update saves the contest's own columns and nothing else: its status moves
// through SetStatus, its languages and titles have operations of their own,
// and its author, its creation time and the moment a leaderboard was revealed
// are not the caller's to rewrite.
func (r *Contests) Update(_ context.Context, c contests.Contest) error {
	stored, ok := r.byID[c.ID]
	if !ok {
		return contests.ErrNotFound
	}
	stored.Enrollment, stored.QuestionMode = c.Enrollment, c.QuestionMode
	stored.Progression, stored.Scoring = c.Progression, c.Scoring
	stored.ICPCPenaltyMin = c.ICPCPenaltyMin
	stored.Timing, stored.DurationMin = c.Timing, c.DurationMin
	stored.StartsAt, stored.EndsAt = c.StartsAt, c.EndsAt
	stored.AllowedCIDRs, stored.Settings = c.AllowedCIDRs, c.Settings
	stored.LeaderboardFreezeMin, stored.LeaderboardNames = c.LeaderboardFreezeMin, c.LeaderboardNames
	if now, ok := r.now(); ok {
		stored.UpdatedAt = now
	}
	r.store(stored)
	return nil
}

func (r *Contests) SetStatus(_ context.Context, id uuid.UUID, from, to string) error {
	// The race, staged. Set by SetStatusRaces, it stands for a concurrent
	// request that committed between the caller's read and this write.
	if r.racesTo != "" {
		c, ok := r.byID[id]
		if ok {
			c.Status = r.racesTo
			r.byID[id] = c
		}
		r.racesTo = ""
	}

	c, ok := r.byID[id]
	if !ok {
		return contests.ErrNotFound
	}
	if c.Status != from {
		return contests.ErrStatusChanged
	}
	c.Status = to
	if now, ok := r.now(); ok {
		c.UpdatedAt = now
	}
	r.byID[id] = c
	return nil
}

// SetStatusRaces makes the next SetStatus find the contest already moved to
// status, as a concurrent writer would have left it. One shot: the point is to
// stage the collision, not to keep the store lying.
func (r *Contests) SetStatusRaces(status string) { r.racesTo = status }

func (r *Contests) Delete(_ context.Context, id uuid.UUID) error {
	if _, ok := r.byID[id]; !ok {
		return contests.ErrNotFound
	}
	delete(r.byID, id)
	r.order = slices.DeleteFunc(r.order, func(stored uuid.UUID) bool { return stored == id })
	return nil
}

func (r *Contests) ReplaceLanguages(_ context.Context, id uuid.UUID, langs []contests.ContestLanguage) error {
	c, ok := r.byID[id]
	if !ok {
		return contests.ErrNotFound
	}
	c.Languages = slices.Clone(langs)
	r.byID[id] = c
	return nil
}

func (r *Contests) ReplaceTranslations(_ context.Context, id uuid.UUID, translations []contests.Translation) error {
	c, ok := r.byID[id]
	if !ok {
		return contests.ErrNotFound
	}
	c.Translations = make(map[string]contests.Translation, len(translations))
	for _, t := range translations {
		c.Translations[t.Lang] = t
	}
	r.byID[id] = c
	return nil
}

// LockContest is a no-op beyond refusing to run outside a unit of work, as
// the real one does, and checking the contest exists: the real cross-session
// locking this stands in for is exercised against a real database
// (internal/postgres/contests_test.go), and this fixture's UnitOfWork already
// runs every call sequentially in one goroutine, so there is no concurrent
// second caller for an in-memory lock to matter against.
func (r *Contests) LockContest(ctx context.Context, id uuid.UUID) error {
	if !inTx(ctx) {
		return errors.New("locking a contest must run inside a transaction")
	}
	if _, ok := r.byID[id]; !ok {
		return contests.ErrNotFound
	}
	return nil
}

// Stories is an in-memory contests.StoryRepository, held to the same answers
// as postgres.Stories by StoryRepositoryContract, which both run.
type Stories struct {
	byContest map[uuid.UUID]contests.Story
	// Clock is what Save stamps UpdatedAt with, the way the table stamps it
	// with the database's own now(). Nil leaves it as it is.
	Clock func() time.Time
	// Err, when set, is what ByContest returns instead of a lookup — a
	// database away, which a caller must propagate, not mistake for a
	// contest that simply has no story yet. Save reads its result back
	// through ByContest, as the real one does, so Save returns it too, after
	// storing the story.
	Err error
	// ContestExists is what Save asks about the contest a story belongs to,
	// standing in for the real table's foreign key. Nil takes every contest
	// as there: a story store standing alone in a test has nothing to ask.
	ContestExists func(id uuid.UUID) bool
}

var (
	_ contests.StoryRepository = (*Stories)(nil)
	_ contests.StoryText       = (*Stories)(nil)
)

// NewStories returns an empty story store.
func NewStories() *Stories {
	return &Stories{byContest: map[uuid.UUID]contests.Story{}}
}

func (r *Stories) ByContest(_ context.Context, contestID uuid.UUID) (contests.Story, error) {
	if r.Err != nil {
		return contests.Story{}, r.Err
	}
	story, ok := r.byContest[contestID]
	if !ok {
		return contests.Story{}, contests.ErrStoryNotFound
	}
	// The text is the caller's to keep, as a row decoded afresh is.
	story.Bodies = maps(story.Bodies)
	return story, nil
}

func (r *Stories) BodyIn(ctx context.Context, contestID uuid.UUID, lang string) (string, error) {
	story, err := r.ByContest(ctx, contestID)
	if err != nil {
		return "", err
	}
	body, ok := story.Body(lang)
	if !ok {
		return "", contests.ErrStoryNotFound
	}
	return body, nil
}

func (r *Stories) Save(ctx context.Context, contestID uuid.UUID, bodies map[string]string) (contests.Story, error) {
	if r.ContestExists != nil && !r.ContestExists(contestID) {
		return contests.Story{}, contests.ErrNotFound
	}
	story, ok := r.byContest[contestID]
	if !ok {
		story = contests.Story{ID: uuid.New(), ContestID: contestID}
	}
	story.Bodies = maps(bodies)
	if r.Clock != nil {
		story.UpdatedAt = r.Clock()
	}
	r.byContest[contestID] = story
	return r.ByContest(ctx, contestID)
}

func (r *Stories) Delete(_ context.Context, contestID uuid.UUID) error {
	delete(r.byContest, contestID)
	return nil
}

// Questions is an in-memory contests.QuestionRepository.
type Questions struct {
	byID map[uuid.UUID]contests.Question
	// ContestExists is what Create asks about the contest a question is
	// added to, standing in for the real insert finding the contest row it
	// locks. Nil takes every contest as there: a question store standing
	// alone in a test has no contests to ask.
	ContestExists func(id uuid.UUID) bool
}

var (
	_ contests.QuestionRepository        = (*Questions)(nil)
	_ contests.VisibleQuestionRepository = (*Questions)(nil)
)

// NewQuestions returns an empty question store.
func NewQuestions() *Questions {
	return &Questions{byID: map[uuid.UUID]contests.Question{}}
}

// Put stores a question as given.
func (r *Questions) Put(q contests.Question) contests.Question {
	if q.ID == uuid.Nil {
		q.ID = uuid.New()
	}
	r.byID[q.ID] = cloneQuestion(q)
	return q
}

// cloneQuestion copies everything a caller could write to through a
// question. The real repository hands out rows it has just read and keeps
// nothing of what it is given, so a caller editing a question it holds has
// not changed the stored one until it saves; sharing maps and slices with
// the store would make a test see an edit that production never would.
func cloneQuestion(q contests.Question) contests.Question {
	if q.MaxAttempts != nil {
		attempts := *q.MaxAttempts
		q.MaxAttempts = &attempts
	}
	q.ChoiceIDs = slices.Clone(q.ChoiceIDs)
	q.Texts = cloneTexts(q.Texts)
	q.Answers = slices.Clone(q.Answers)
	return q
}

func cloneTexts(texts map[string]contests.QuestionText) map[string]contests.QuestionText {
	if texts == nil {
		return nil
	}
	out := make(map[string]contests.QuestionText, len(texts))
	for lang, text := range texts {
		text.Choices = maps(text.Choices)
		out[lang] = text
	}
	return out
}

func (r *Questions) List(_ context.Context, contestID uuid.UUID) ([]contests.Question, error) {
	var found []contests.Question
	for _, q := range r.byID {
		if q.ContestID == contestID {
			found = append(found, cloneQuestion(q))
		}
	}
	slices.SortFunc(found, func(a, b contests.Question) int { return a.Ord - b.Ord })
	return found, nil
}

// ForContest implements contests.VisibleQuestionRepository the same way the
// real repository's query does: only is_visible questions, in display order,
// and only those carrying a body in lang — a missing translation drops the
// question from the result rather than serving an empty one.
func (r *Questions) ForContest(ctx context.Context, contestID uuid.UUID, lang string) ([]contests.VisibleQuestion, error) {
	all, err := r.List(ctx, contestID)
	if err != nil {
		return nil, err
	}

	var found []contests.VisibleQuestion
	for _, q := range all {
		if !q.IsVisible {
			continue
		}
		text, ok := q.Texts[lang]
		if !ok {
			continue
		}
		found = append(found, contests.VisibleQuestion{
			ID: q.ID, Kind: q.Kind, Points: q.Points, MaxAttempts: q.MaxAttempts,
			ChoiceIDs: q.ChoiceIDs, BodyMD: text.BodyMD, Choices: text.Choices,
		})
	}
	return found, nil
}

func (r *Questions) ByID(_ context.Context, questionID uuid.UUID) (contests.Question, error) {
	q, ok := r.byID[questionID]
	if !ok {
		return contests.Question{}, contests.ErrQuestionNotFound
	}
	return cloneQuestion(q), nil
}

// Create stores the question's own fields only: its position and identifier
// are assigned here, and its text and answers have operations of their own,
// so whatever the caller's value carries of either is dropped, as the real
// insert does not write it.
func (r *Questions) Create(ctx context.Context, q contests.Question) (contests.Question, error) {
	// The real insert locks the contest row first, which only holds inside a
	// transaction, so it refuses to run outside one.
	if !inTx(ctx) {
		return contests.Question{}, errors.New("adding a question must run inside a transaction")
	}
	if r.ContestExists != nil && !r.ContestExists(q.ContestID) {
		return contests.Question{}, contests.ErrNotFound
	}
	existing, _ := r.List(ctx, q.ContestID)
	q.ID = uuid.New()
	q.Ord = len(existing) + 1
	q.Texts, q.Answers = nil, nil
	return cloneQuestion(r.Put(q)), nil
}

func (r *Questions) Update(_ context.Context, q contests.Question) error {
	stored, ok := r.byID[q.ID]
	if !ok {
		return contests.ErrQuestionNotFound
	}
	// Position, contest, text and answers are not Update's to change,
	// exactly as in the real repository.
	q.ContestID, q.Ord, q.Texts, q.Answers = stored.ContestID, stored.Ord, stored.Texts, stored.Answers
	r.byID[q.ID] = cloneQuestion(q)
	return nil
}

func (r *Questions) Delete(ctx context.Context, questionID uuid.UUID) error {
	q, ok := r.byID[questionID]
	if !ok {
		return contests.ErrQuestionNotFound
	}
	delete(r.byID, questionID)

	// Close the gap, so positions stay 1..n as the real repository keeps them.
	remaining, _ := r.List(ctx, q.ContestID)
	for i, other := range remaining {
		other.Ord = i + 1
		r.byID[other.ID] = other
	}
	return nil
}

func (r *Questions) Reorder(ctx context.Context, contestID uuid.UUID, ordered []uuid.UUID) error {
	// The real reorder defers its uniqueness check to the end of the
	// transaction, which it cannot do outside one, so it refuses first.
	if !inTx(ctx) {
		return errors.New("reordering questions must run inside a transaction")
	}
	for i, id := range ordered {
		q, ok := r.byID[id]
		if !ok || q.ContestID != contestID {
			return contests.ErrQuestionNotFound
		}
		q.Ord = i + 1
		r.byID[id] = q
	}
	return nil
}

func (r *Questions) ReplaceTexts(_ context.Context, questionID uuid.UUID, texts map[string]contests.QuestionText) error {
	q, ok := r.byID[questionID]
	if !ok {
		return contests.ErrQuestionNotFound
	}
	q.Texts = cloneTexts(texts)
	r.byID[questionID] = q
	return nil
}

func (r *Questions) ReplaceAnswers(_ context.Context, questionID uuid.UUID, answers []contests.Answer) error {
	q, ok := r.byID[questionID]
	if !ok {
		return contests.ErrQuestionNotFound
	}
	// Each answer gets an identity of its own and belongs to this question,
	// and they are kept in the order of their values. The real query orders
	// by the database's collation; byte order agrees with it for the
	// lower-case ASCII values the contract uses, not in general.
	stored := make([]contests.Answer, len(answers))
	for i, a := range answers {
		a.ID, a.QuestionID = uuid.New(), questionID
		stored[i] = a
	}
	slices.SortStableFunc(stored, func(a, b contests.Answer) int { return strings.Compare(a.Value, b.Value) })
	q.Answers = stored
	r.byID[questionID] = q
	return nil
}

// Managers is an in-memory contests.ManagerRepository, held to the same
// answers as postgres.ContestManagers by ManagerRepositoryContract, which both
// run.
type Managers struct {
	byContest map[uuid.UUID][]contests.Manager
	// Accounts resolves the login and name carried on every staff entry, the
	// way the real repository joins them in at read time. Nil leaves what the
	// entry was stored with.
	Accounts AccountLookup
	// Clock is what Grant stamps GrantedAt with, the way the table's default
	// stamps it with the database's own now(), whatever time the caller
	// carries. Nil keeps the time the caller carries.
	Clock func() time.Time
	// Lookups counts Get and List calls, so a test can tell how many staff
	// lookups a bulk operation made.
	Lookups int
}

var _ contests.ManagerRepository = (*Managers)(nil)

// NewManagers returns an empty staff store.
func NewManagers() *Managers {
	return &Managers{byContest: map[uuid.UUID][]contests.Manager{}}
}

// named returns the entry as a read presents it: with the account's own login
// and name when the store can find them.
func (r *Managers) named(ctx context.Context, m contests.Manager) contests.Manager {
	if r.Accounts != nil {
		m.Login, m.FullName = r.Accounts(ctx, m.UserID)
	}
	return m
}

func (r *Managers) List(ctx context.Context, contestID uuid.UUID) ([]contests.Manager, error) {
	r.Lookups++
	staff := slices.Clone(r.byContest[contestID])
	for i, m := range staff {
		staff[i] = r.named(ctx, m)
	}
	slices.SortFunc(staff, func(a, b contests.Manager) int {
		if a.Role == b.Role {
			return strings.Compare(a.Login, b.Login)
		}
		if a.Role == rbac.RoleOwner {
			return -1
		}
		return 1
	})
	return staff, nil
}

func (r *Managers) Get(ctx context.Context, contestID, userID uuid.UUID) (contests.Manager, error) {
	r.Lookups++
	for _, m := range r.byContest[contestID] {
		if m.UserID == userID {
			return r.named(ctx, m), nil
		}
	}
	return contests.Manager{}, contests.ErrManagerNotFound
}

func (r *Managers) Grant(_ context.Context, m contests.Manager) error {
	if r.Clock != nil {
		m.GrantedAt = r.Clock()
	}
	staff := r.byContest[m.ContestID]
	for i, existing := range staff {
		if existing.UserID == m.UserID {
			staff[i] = m
			r.byContest[m.ContestID] = staff
			return nil
		}
	}
	r.byContest[m.ContestID] = append(staff, m)
	return nil
}

func (r *Managers) Revoke(_ context.Context, contestID, userID uuid.UUID) error {
	staff := r.byContest[contestID]
	kept := slices.DeleteFunc(slices.Clone(staff), func(m contests.Manager) bool { return m.UserID == userID })
	if len(kept) == len(staff) {
		return contests.ErrManagerNotFound
	}
	r.byContest[contestID] = kept
	return nil
}

// AccountLookup names an account, standing in for the join the real
// repository does. Without it a participant or a staff entry would come back
// with an empty login, which is not what the endpoint returns and not what a
// test should be allowed to pass against.
type AccountLookup func(ctx context.Context, id uuid.UUID) (login, fullName string)

// PermissionLookup reports what an account may do, standing in for the join
// the real repository makes through the role tables. Without it every account
// reads as holding nothing, which would let a test pass a publish gate the
// real one refuses.
type PermissionLookup func(ctx context.Context, id uuid.UUID) []string

// Registrations is an in-memory contests.RegistrationRepository, held to the
// same answers as postgres.Registrations by RegistrationRepositoryContract,
// which both run.
type Registrations struct {
	byID map[uuid.UUID]contests.Participant
	// Accounts resolves the login and name carried on every participant.
	Accounts AccountLookup
	// Permissions resolves what a participant's account may do, for
	// RegisteredWithPermission.
	Permissions PermissionLookup
	// Clock is what Add stamps CreatedAt with, the way the table's default
	// stamps it with the database's own now(). Nil leaves CreatedAt zero.
	Clock func() time.Time
	// ContestExists and UserExists are what Add asks about the contest and
	// the account it registers, standing in for the real table's foreign
	// keys. Nil takes every contest, or every account, as there: a
	// registration store standing alone in a test has nothing to ask, and
	// the fixture asks only about the contest (see NewFixture).
	ContestExists func(id uuid.UUID) bool
	UserExists    func(id uuid.UUID) bool
	// work holds the registrations the store reports as having a record
	// behind them (PutWork).
	work map[uuid.UUID]bool
	// HasWorkInTx records whether the last HasWork call ran inside the
	// fixture's unit of work. A deletion that asks whether a registration has
	// a record behind it, and then opens a transaction to delete it, decided
	// on a state that is no longer the one it writes against.
	HasWorkInTx bool
	// MissLookups makes ByUser report "not found" even when the row is there,
	// which is what a caller sees when a concurrent writer registered the same
	// person between the lookup and the write. It exists so that path can be
	// exercised without two goroutines.
	MissLookups bool
}

var _ contests.RegistrationRepository = (*Registrations)(nil)

// NewRegistrations returns an empty registration store.
func NewRegistrations() *Registrations {
	return &Registrations{byID: map[uuid.UUID]contests.Participant{}, work: map[uuid.UUID]bool{}}
}

// Put stores a participation as given, naming the account when it can, so a
// seeded participant reads the same as one that went through Add.
func (r *Registrations) Put(p contests.Participant) contests.Participant {
	if p.ID == uuid.Nil {
		p.ID = uuid.New()
	}
	if p.Login == "" && r.Accounts != nil {
		p.Login, p.FullName = r.Accounts(context.Background(), p.UserID)
	}
	r.byID[p.ID] = p
	return p
}

// PutWork marks a registration as having queries, answers, notes or signals
// behind it — what HasWork reports and what removing one would destroy.
func (r *Registrations) PutWork(registrationID uuid.UUID) {
	r.work[registrationID] = true
}

func (r *Registrations) HasWork(ctx context.Context, registrationID uuid.UUID) (bool, error) {
	r.HasWorkInTx = inTx(ctx)
	return r.work[registrationID], nil
}

// RegisteredWithPermission names this contest's participants whose account
// holds the permission, ordered by login and bounded the same way the real
// repository bounds it.
func (r *Registrations) RegisteredWithPermission(ctx context.Context, contestID uuid.UUID, permission string) ([]string, error) {
	if r.Permissions == nil {
		return nil, nil
	}

	var logins []string
	for _, p := range r.byID {
		if p.ContestID != contestID {
			continue
		}
		if slices.Contains(r.Permissions(ctx, p.UserID), permission) {
			logins = append(logins, p.Login)
		}
	}
	slices.Sort(logins)
	return logins[:min(len(logins), contests.MaxReportedStaff)], nil
}

func (r *Registrations) List(_ context.Context, contestID uuid.UUID, f contests.ParticipantFilter) ([]contests.Participant, int, error) {
	var matched []contests.Participant
	for _, p := range r.byID {
		if p.ContestID != contestID {
			continue
		}
		if f.Status != "" && p.Status != f.Status {
			continue
		}
		if f.Query != "" && !matchesQuery(p, f.Query) {
			continue
		}
		matched = append(matched, p)
	}
	slices.SortFunc(matched, func(a, b contests.Participant) int { return strings.Compare(a.Login, b.Login) })

	total := len(matched)
	if f.Offset >= total {
		return nil, total, nil
	}
	end := min(f.Offset+f.Limit, total)
	return matched[f.Offset:end], total, nil
}

// matchesQuery reports whether the search names the participant's login or
// full name, ignoring case, as the real repository's ILIKE does. The text is
// a substring and nothing more: a percent sign or an underscore in it means
// itself.
func matchesQuery(p contests.Participant, query string) bool {
	query = strings.ToLower(query)
	return strings.Contains(strings.ToLower(p.Login), query) ||
		strings.Contains(strings.ToLower(p.FullName), query)
}

func (r *Registrations) ByUser(_ context.Context, contestID, userID uuid.UUID) (contests.Participant, error) {
	if r.MissLookups {
		return contests.Participant{}, contests.ErrParticipantNotFound
	}
	for _, p := range r.byID {
		if p.ContestID == contestID && p.UserID == userID {
			return p, nil
		}
	}
	return contests.Participant{}, contests.ErrParticipantNotFound
}

func (r *Registrations) Add(ctx context.Context, contestID, userID uuid.UUID) (contests.Participant, error) {
	if r.ContestExists != nil && !r.ContestExists(contestID) {
		return contests.Participant{}, contests.ErrNotFound
	}
	if r.UserExists != nil && !r.UserExists(userID) {
		return contests.Participant{}, users.ErrNotFound
	}
	// Checked against the stored rows rather than through ByUser: the real
	// guarantee is a unique index, and it does not stop holding because a
	// lookup missed.
	for _, existing := range r.byID {
		if existing.ContestID == contestID && existing.UserID == userID {
			return contests.Participant{}, contests.ErrAlreadyEnrolled
		}
	}
	p := contests.Participant{
		ContestID: contestID,
		UserID:    userID,
		Status:    contests.RegistrationRegistered,
	}
	if r.Clock != nil {
		p.CreatedAt = r.Clock()
	}
	if r.Accounts != nil {
		p.Login, p.FullName = r.Accounts(ctx, userID)
	}
	return r.Put(p), nil
}

func (r *Registrations) Remove(ctx context.Context, contestID, userID uuid.UUID) error {
	p, err := r.ByUser(ctx, contestID, userID)
	if err != nil {
		return err
	}
	delete(r.byID, p.ID)
	return nil
}

// EnrolledIn reports which of the named contests the user is registered for.
//
// Only the ones asked about: the map is a lookup for a page of rows, not a
// dump of everything the person is on, and a caller that read it as one would
// be reading somebody else's list into their own screen.
func (r *Registrations) EnrolledIn(_ context.Context, userID uuid.UUID, contestIDs []uuid.UUID) (map[uuid.UUID]bool, error) {
	wanted := make(map[uuid.UUID]struct{}, len(contestIDs))
	for _, id := range contestIDs {
		wanted[id] = struct{}{}
	}

	on := map[uuid.UUID]bool{}
	for _, p := range r.byID {
		if p.UserID != userID {
			continue
		}
		if _, asked := wanted[p.ContestID]; asked {
			on[p.ContestID] = true
		}
	}
	return on, nil
}

func (r *Registrations) SetStatus(_ context.Context, registrationID uuid.UUID, status string) error {
	p, ok := r.byID[registrationID]
	if !ok {
		return contests.ErrParticipantNotFound
	}
	p.Status = status
	r.byID[registrationID] = p
	return nil
}

// Start mirrors postgres.Registrations.Start: it sets StartedAt and moves the
// status to active together, and only the first call for a registration has
// any effect — a second one reads back what the first wrote instead of
// moving the clock. A registration whose status is anything but registered
// (disqualified, say) is read back as it is: starting it would undo that.
func (r *Registrations) Start(_ context.Context, registrationID uuid.UUID, now time.Time) (contests.Participant, error) {
	p, ok := r.byID[registrationID]
	if !ok {
		return contests.Participant{}, contests.ErrParticipantNotFound
	}
	if p.StartedAt == nil && p.Status == contests.RegistrationRegistered {
		started := now
		p.StartedAt = &started
		p.Status = contests.RegistrationActive
		r.byID[registrationID] = p
	}
	return p, nil
}

// AddScore mirrors the real repository's atomic increment.
func (r *Registrations) AddScore(_ context.Context, registrationID uuid.UUID, delta int) error {
	p, ok := r.byID[registrationID]
	if !ok {
		return contests.ErrParticipantNotFound
	}
	p.TotalScore += delta
	r.byID[registrationID] = p
	return nil
}

// Policies is an in-memory contests.PolicyStore, held to the same answers as
// postgres.SQLPolicies by PolicyStoreContract, which both run.
type Policies struct {
	byContest map[uuid.UUID]contests.SQLPolicy
	// Clock is what Save stamps UpdatedAt with, the way the statement stamps it
	// with the database's own now(). Nil leaves it as it is.
	Clock func() time.Time
	// ContestExists is what Save asks about the contest a policy belongs to,
	// standing in for the real table's foreign key. Nil takes every contest
	// as there: a policy store standing alone in a test has nothing to ask.
	ContestExists func(id uuid.UUID) bool
}

var _ contests.PolicyStore = (*Policies)(nil)

// NewPolicies returns an empty policy store.
func NewPolicies() *Policies {
	return &Policies{byContest: map[uuid.UUID]contests.SQLPolicy{}}
}

func (r *Policies) ByContest(_ context.Context, contestID uuid.UUID) (contests.SQLPolicy, error) {
	p, ok := r.byContest[contestID]
	if !ok {
		// A contest nobody configured is read-only, exactly as in the real
		// store.
		return contests.DefaultSQLPolicy(contestID), nil
	}
	return clonePolicy(p), nil
}

func (r *Policies) Save(_ context.Context, p contests.SQLPolicy) error {
	if r.ContestExists != nil && !r.ContestExists(p.ContestID) {
		return contests.ErrNotFound
	}
	p = clonePolicy(p)
	if r.Clock != nil {
		p.UpdatedAt = r.Clock()
	}
	r.byContest[p.ContestID] = p
	return nil
}

// clonePolicy copies what a policy holds by reference, so neither the caller
// of Save nor the caller of ByContest shares it with the store, and gives an
// unset list the empty one a stored array reads back as.
func clonePolicy(p contests.SQLPolicy) contests.SQLPolicy {
	p.WritableTables = append([]string{}, p.WritableTables...)
	if p.UpdatedBy != nil {
		by := *p.UpdatedBy
		p.UpdatedBy = &by
	}
	return p
}

// Attempts is an in-memory contests.AttemptStore, derived from the submission
// store the way the real one reads the submissions table, so an answer a test
// records through Insert is what it reads back here, as in production — held
// to the same answers as postgres.Attempts by AttemptStoreContract, which both
// run. It has no store of its own for a test to stage stats in: every state a
// participant can be in is one Insert can produce, and a staged one could say
// what no sequence of submissions ever would.
type Attempts struct {
	submissions *Submissions
}

var _ contests.AttemptStore = (*Attempts)(nil)

// NewAttempts derives attempt stats from the given submission store.
func NewAttempts(submissions *Submissions) *Attempts {
	return &Attempts{submissions: submissions}
}

// ForRegistration mirrors postgres.Attempts.ForRegistration: per question the
// registration answered, how many submissions it made, whether any was
// correct, and the sum of what they were awarded.
func (r *Attempts) ForRegistration(_ context.Context, registrationID uuid.UUID) (map[uuid.UUID]contests.AttemptStats, error) {
	out := map[uuid.UUID]contests.AttemptStats{}
	for key, recorded := range r.submissions.byKey {
		if key.registrationID != registrationID {
			continue
		}
		stats := contests.AttemptStats{Attempts: len(recorded)}
		for _, s := range recorded {
			stats.Correct = stats.Correct || s.IsCorrect
			stats.PointsAwarded += s.PointsAwarded
		}
		out[key.questionID] = stats
	}
	return out, nil
}

// Submissions is an in-memory contests.SubmissionRepository.
//
// It mirrors what the real repository's one INSERT statement guarantees
// (postgres.Submissions.Insert): a submission is refused with
// ErrDeadlinePassed once the clock has reached req.Deadline, or with
// ErrQuestionClosed once the question is already answered correctly or every
// attempt is spent, and otherwise takes the next attempt number — held to the
// same answers as the real one by SubmissionRepositoryContract, which both
// run. It does not reproduce the real repository's concurrency guarantee — a
// Go map has no analogue of the table's own UNIQUE constraint racing two
// transactions — so the genuine race (finding 3) is proven where it can
// actually happen, against PostgreSQL (internal/postgres/submissions_test.go),
// not here. ConflictsRemaining exists so a Service-level test can still
// exercise Submit's own retry loop deterministically, without a second
// goroutine.
type Submissions struct {
	byKey map[submissionKey][]contests.Submission
	// Clock answers what Insert checks req.Deadline against; nil defaults to
	// the real wall clock so a test that never sets it still gets a moving
	// clock rather than the zero value.
	Clock func() time.Time
	// ConflictsRemaining makes the next this-many Insert calls return
	// ErrAttemptConflict instead of writing anything, simulating a
	// submission that lost the attempt-number race and must be retried.
	ConflictsRemaining int
	// Requests is every request Insert was handed, in order, whatever it
	// answered: what a test reads to prove the deadline Submit wrote with, or
	// that a refused answer never reached the write at all.
	Requests []contests.SubmissionRequest
}

type submissionKey struct {
	registrationID uuid.UUID
	questionID     uuid.UUID
}

var _ contests.SubmissionRepository = (*Submissions)(nil)

// NewSubmissions returns an empty submission store.
func NewSubmissions() *Submissions {
	return &Submissions{byKey: map[submissionKey][]contests.Submission{}}
}

func (r *Submissions) now() time.Time {
	if r.Clock != nil {
		return r.Clock()
	}
	return time.Now().UTC()
}

func (r *Submissions) Insert(_ context.Context, req contests.SubmissionRequest) (contests.Submission, error) {
	r.Requests = append(r.Requests, req)
	if r.ConflictsRemaining > 0 {
		r.ConflictsRemaining--
		return contests.Submission{}, contests.ErrAttemptConflict
	}

	now := r.now()
	// The deadline, checked here against this call's own clock reading,
	// takes priority over "closed" — the same order the real statement's
	// HAVING clause and its own fallback query resolve it in
	// (postgres.Submissions.Insert).
	if !now.Before(req.Deadline) {
		return contests.Submission{}, contests.ErrDeadlinePassed
	}

	key := submissionKey{req.RegistrationID, req.QuestionID}
	existing := r.byKey[key]

	for _, s := range existing {
		if s.IsCorrect {
			return contests.Submission{}, contests.ErrQuestionClosed
		}
	}
	if req.MaxAttempts != nil && len(existing) >= *req.MaxAttempts {
		return contests.Submission{}, contests.ErrQuestionClosed
	}

	s := contests.Submission{
		ID:             uuid.New(),
		RegistrationID: req.RegistrationID,
		QuestionID:     req.QuestionID,
		AttemptNo:      len(existing) + 1,
		Value:          req.Value,
		IsCorrect:      req.IsCorrect,
		// Mirrors the real statement's own computation (§6.1.1,
		// postgres.Submissions.Insert): the question's face value minus the
		// per-attempt penalty times however many attempts are already
		// committed (len(existing), the same count used for AttemptNo above),
		// floored at zero, and only when this attempt is itself correct.
		PointsAwarded: pointsAwarded(req, len(existing)),
		SubmittedAt:   now,
	}
	r.byKey[key] = append(existing, s)
	return s, nil
}

// pointsAwarded computes what one attempt earns, the same way the real
// statement does (§6.1.1): nothing for a wrong answer, and for a correct one
// the face value minus the penalty already run up by priorAttempts, never
// below zero.
func pointsAwarded(req contests.SubmissionRequest, priorAttempts int) int {
	if !req.IsCorrect {
		return 0
	}
	awarded := req.Points - priorAttempts*req.PenaltyPerAttempt
	if awarded < 0 {
		awarded = 0
	}
	return awarded
}

// SequentialProgress is an in-memory contests.SequentialGate, derived from
// the same two stores Submit itself consults in production — which questions
// exist and in what order, and what a registration has already submitted to
// each — rather than a store of its own a test could forget to keep in sync
// with what Submit actually wrote. It is held to the same answers as
// postgres.Sequence by SequentialGateContract, which both run.
type SequentialProgress struct {
	questions   *Questions
	submissions *Submissions
}

var _ contests.SequentialGate = (*SequentialProgress)(nil)

// NewSequentialProgress derives sequential-progression state from the given
// question and submission stores.
func NewSequentialProgress(questions *Questions, submissions *Submissions) *SequentialProgress {
	return &SequentialProgress{questions: questions, submissions: submissions}
}

// Open mirrors postgres.Sequence.Open: every question of contestID ordered
// strictly before ord must be closed — answered correctly, or every attempt
// spent — for registrationID.
func (g *SequentialProgress) Open(ctx context.Context, contestID, registrationID uuid.UUID, ord int) (bool, error) {
	all, err := g.questions.List(ctx, contestID)
	if err != nil {
		return false, err
	}
	for _, q := range all {
		if q.Ord >= ord {
			continue
		}
		submissions := g.submissions.All(registrationID, q.ID)
		closed := false
		for _, s := range submissions {
			if s.IsCorrect {
				closed = true
				break
			}
		}
		if !closed && q.MaxAttempts != nil && len(submissions) >= *q.MaxAttempts {
			closed = true
		}
		if !closed {
			return false, nil
		}
	}
	return true, nil
}

// Frontier mirrors postgres.Sequence.Frontier: the question of contestID
// lowest in display order that is not yet closed for registrationID, or
// uuid.Nil once every question is closed.
func (g *SequentialProgress) Frontier(ctx context.Context, contestID, registrationID uuid.UUID) (uuid.UUID, error) {
	all, err := g.questions.List(ctx, contestID)
	if err != nil {
		return uuid.Nil, err
	}

	var frontier uuid.UUID
	frontierOrd := 0
	haveFrontier := false
	for _, q := range all {
		submissions := g.submissions.All(registrationID, q.ID)
		closed := false
		for _, s := range submissions {
			if s.IsCorrect {
				closed = true
				break
			}
		}
		if !closed && q.MaxAttempts != nil && len(submissions) >= *q.MaxAttempts {
			closed = true
		}
		if closed {
			continue
		}
		if !haveFrontier || q.Ord < frontierOrd {
			frontier = q.ID
			frontierOrd = q.Ord
			haveFrontier = true
		}
	}
	return frontier, nil
}

// All lists every submission stored for this registration and question, in
// the order they were inserted, so a test can inspect exactly what was
// written.
func (r *Submissions) All(registrationID, questionID uuid.UUID) []contests.Submission {
	return append([]contests.Submission(nil), r.byKey[submissionKey{registrationID, questionID}]...)
}

// Languages is an in-memory contests.LanguageCatalog, held to the same answers
// as postgres.Languages by LanguageCatalogContract, which both run.
type Languages struct {
	Available []contests.Language
}

var _ contests.LanguageCatalog = (*Languages)(nil)

// NewLanguages returns the three languages the platform launches with.
func NewLanguages() *Languages {
	return &Languages{Available: []contests.Language{
		{Code: "en", Name: "English", NativeName: "English", IsActive: true, SortOrder: 10},
		{Code: "ro", Name: "Romanian", NativeName: "Română", IsActive: true, SortOrder: 20},
		{Code: "ru", Name: "Russian", NativeName: "Русский", IsActive: true, SortOrder: 30},
	}}
}

// Active lists the active languages in display order: by sort order, ties
// broken by code, as the table is read.
func (r *Languages) Active(context.Context) ([]contests.Language, error) {
	var active []contests.Language
	for _, l := range r.Available {
		if l.IsActive {
			active = append(active, l)
		}
	}
	slices.SortFunc(active, func(a, b contests.Language) int {
		return cmp.Or(cmp.Compare(a.SortOrder, b.SortOrder), strings.Compare(a.Code, b.Code))
	})
	return active, nil
}

// Sink collects audit entries so a test can assert that a privileged action
// left a trail.
type Sink struct {
	Entries []audit.Entry
	// Loose holds the entries appended with no transaction open.
	//
	// Almost every action must record inside one: an entry written after the
	// change commits can be lost while the change survives, and one written
	// before it can outlive a rollback. Either way the trail stops being
	// evidence. Refusals are the exception — there is nothing to be atomic
	// with — so this is a list rather than a flag, and a test names which
	// entries it expects to find in it.
	Loose []audit.Entry
	// AppendManyErr, when set, is what AppendMany returns instead of
	// recording anything — a test's way of standing for the trail itself
	// failing (a full disk, a database that is away) so a caller relying on
	// RecordMany can prove it does not swallow that failure.
	AppendManyErr error
}

var _ audit.Sink = (*Sink)(nil)

// NewSink returns an empty audit sink.
func NewSink() *Sink { return &Sink{} }

func (s *Sink) Append(ctx context.Context, e audit.Entry) error {
	s.Entries = append(s.Entries, e)
	if !inTx(ctx) {
		s.Loose = append(s.Loose, e)
	}
	return nil
}

// LatestStartBlocked implements the narrow read contests.Scheduler's own
// dedup check needs (finding 1) directly off Entries: the same in-memory log
// Append and AppendMany already build stands in for the one real audit_log
// table postgres.AuditSink writes and postgres.AuditTrail reads, so "the
// newest entry on file for this contest" is simply the last matching one in
// append order — no separate fake to keep in sync with this one.
func (s *Sink) LatestStartBlocked(_ context.Context, contestID uuid.UUID) ([]string, bool, error) {
	for i := len(s.Entries) - 1; i >= 0; i-- {
		e := s.Entries[i]
		if e.Entity != "contest" || e.EntityID != contestID.String() {
			continue
		}
		if e.Action != audit.ActionContestStartBlocked {
			return nil, false, nil
		}
		codes, _ := e.Payload["problems"].([]string)
		return codes, true, nil
	}
	return nil, false, nil
}

// AppendMany appends every entry the same way Append does, one at a time:
// what a test asserts on is the resulting state, not the round trips it took.
func (s *Sink) AppendMany(ctx context.Context, entries []audit.Entry) error {
	if s.AppendManyErr != nil {
		return s.AppendManyErr
	}
	for _, e := range entries {
		if err := s.Append(ctx, e); err != nil {
			return err
		}
	}
	return nil
}

// Recorded reports whether an entry with that action was written.
func (s *Sink) Recorded(action string) bool {
	return slices.Contains(s.Actions(), action)
}

// Actions lists the recorded actions, for a readable failure message.
func (s *Sink) Actions() []string {
	actions := make([]string, 0, len(s.Entries))
	for _, e := range s.Entries {
		actions = append(actions, e.Action)
	}
	return actions
}

// Games is an in-memory contests.GameSource: the SQL an organizer wrote as
// one contest's game, without any of the template lifecycle that lives in
// internal/provisioning.
type Games struct {
	byContest map[uuid.UUID]string
	// files holds the contests whose game came from an uploaded dump — a
	// game that exists and whose SQL a package cannot carry
	// (provisioning.SourceFile). Kept apart from byContest rather than as an
	// empty string in it, because an empty string in byContest is precisely
	// the confusion this fake has to be able to reproduce.
	files map[uuid.UUID]bool
	// Err, when set, is what Script returns instead of a script — a test's
	// way of standing for the game's own storage being away.
	Err error
}

var _ contests.GameSource = (*Games)(nil)

// NewGames returns an empty game store.
func NewGames() *Games {
	return &Games{byContest: map[uuid.UUID]string{}, files: map[uuid.UUID]bool{}}
}

// Put stores the script one contest's game is built from.
func (r *Games) Put(contestID uuid.UUID, script string) {
	r.byContest[contestID] = script
}

// PutFile gives the contest a game built from an uploaded dump: one that
// exists and whose SQL no package can carry.
func (r *Games) PutFile(contestID uuid.UUID) {
	r.files[contestID] = true
}

func (r *Games) Script(_ context.Context, contestID uuid.UUID) (string, bool, bool, error) {
	if r.Err != nil {
		return "", false, false, r.Err
	}
	if r.files[contestID] {
		return "", true, true, nil
	}
	script, ok := r.byContest[contestID]
	return script, ok, false, nil
}

// maps copies a map so a stored value cannot be mutated through the caller's
// reference.
func maps(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// Covers is the uploaded covers of contests, in memory: the one question the
// publish gate asks about a contest's picture.
//
// Deliberately not a covers.Repository — the gate's own narrow interface is
// one method, and this fake stands for that method rather than for a store
// the contests package has never heard of.
type Covers struct {
	byContest map[uuid.UUID]string
	// Err, when set, is what Attribution returns instead of an answer: a
	// test's way of standing for the covers table being away.
	Err error
}

// NewCovers returns an empty cover store.
func NewCovers() *Covers { return &Covers{byContest: map[uuid.UUID]string{}} }

// Put gives the contest an uploaded cover with that credit line. An empty one
// is exactly the state the publish gate exists to refuse.
func (r *Covers) Put(contestID uuid.UUID, attribution string) {
	r.byContest[contestID] = attribution
}

// Attribution answers whether the contest has an uploaded cover, and whose it
// is.
func (r *Covers) Attribution(_ context.Context, contestID uuid.UUID) (string, bool, error) {
	if r.Err != nil {
		return "", false, r.Err
	}
	attribution, uploaded := r.byContest[contestID]
	return attribution, uploaded, nil
}
