// Package conteststest provides in-memory implementations of the contests
// package's storage, a fixture that assembles the real service from them, and
// the contracts (*_contract.go) every implementation must pass. internal/postgres
// runs the same contracts, which keeps these fakes honest about production.
//
// None of these types is safe for concurrent use: a fixture belongs to one test.
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

// Contests is an in-memory contests.Repository.
type Contests struct {
	byID map[uuid.UUID]contests.Contest
	// order is insertion order, so listings are deterministic.
	order   []uuid.UUID
	racesTo string
	// Clock stands in for the database's now() in Create, Update and
	// SetStatus. Nil leaves the timestamps as they are.
	Clock func() time.Time
	// managers and registrations back Filter.ManagedBy and Filter.VisibleTo;
	// see Rosters.
	managers      *Managers
	registrations *Registrations
}

var _ contests.Repository = (*Contests)(nil)

func NewContests() *Contests {
	return &Contests{byID: map[uuid.UUID]contests.Contest{}}
}

// Rosters wires the stores List joins for Filter.ManagedBy and
// Filter.VisibleTo. Until it is called, a ManagedBy listing is empty and a
// VisibleTo listing holds only the open contests.
func (r *Contests) Rosters(managers *Managers, registrations *Registrations) {
	r.managers, r.registrations = managers, registrations
}

func (r *Contests) Count() int { return len(r.byID) }

// Exists stands in for the foreign key the rows that hang off a contest carry.
func (r *Contests) Exists(id uuid.UUID) bool {
	_, ok := r.byID[id]
	return ok
}

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
	// The column defaults, so a seed that omits them reads back like a
	// stored row.
	if c.LeaderboardNames == "" {
		c.LeaderboardNames = contests.LeaderboardNamesLogin
	}
	if c.ICPCPenaltyMin == 0 {
		c.ICPCPenaltyMin = contests.DefaultICPCPenaltyMin
	}
	r.store(c)
	return c
}

// cloneContest copies everything held by reference. The real repository
// shares nothing with its callers, so editing a held contest must not change
// the stored one until it is saved.
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

// served is what a read hands out: a copy with its languages in the real
// projection's order, the default first and the rest by code.
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

// Create writes only the contest's own columns, as the real insert does: the
// identity and timestamps are assigned here, languages and titles are dropped,
// and Put's seed defaults are not applied.
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

// List orders like the real query: newest start first, no start last, and
// the most recently created first among those that start together.
func (r *Contests) List(ctx context.Context, f contests.Filter) ([]contests.Contest, int, error) {
	var matched []contests.Contest
	// Walked newest first, so the stable sort leaves the later insertion
	// first among ties.
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

func (r *Contests) staffs(user, contest uuid.UUID) bool {
	if r.managers == nil {
		return false
	}
	return slices.ContainsFunc(r.managers.byContest[contest], func(m contests.Manager) bool { return m.UserID == user })
}

// registered ignores the status of both the contest and the registration.
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

// visibleTo is what a participant may see: non-draft contests they are on,
// plus open ones still taking signups.
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

// Update saves the contest's own columns only: status moves through
// SetStatus, languages and titles have their own operations, and the author,
// creation time and reveal time are not the caller's to rewrite.
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
	// A staged race: a concurrent request committed between the caller's
	// read and this write.
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
// status, as a concurrent writer would have left it. It fires once.
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

// LockContest only refuses to run outside a transaction, as the real one
// does, and checks the contest exists. The fixture runs every call in one
// goroutine, so there is nothing to lock against; the real locking is tested
// in internal/postgres/contests_test.go.
func (r *Contests) LockContest(ctx context.Context, id uuid.UUID) error {
	if !inTx(ctx) {
		return errors.New("locking a contest must run inside a transaction")
	}
	if _, ok := r.byID[id]; !ok {
		return contests.ErrNotFound
	}
	return nil
}

// Stories is an in-memory contests.StoryRepository.
type Stories struct {
	byContest map[uuid.UUID]contests.Story
	// Clock stands in for the database's now() in Save. Nil leaves UpdatedAt
	// as it is.
	Clock func() time.Time
	// Err, when set, is what ByContest returns: a database away, which a
	// caller must not mistake for a missing story. Save reads back through
	// ByContest, so it returns Err too, after storing.
	Err error
	// ContestExists stands in for the foreign key on the contest. Nil accepts
	// every contest.
	ContestExists func(id uuid.UUID) bool
}

var (
	_ contests.StoryRepository = (*Stories)(nil)
	_ contests.StoryText       = (*Stories)(nil)
)

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
	// A copy, as a freshly decoded row is.
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
	// ContestExists stands in for the real insert finding the contest row it
	// locks. Nil accepts every contest.
	ContestExists func(id uuid.UUID) bool
}

var (
	_ contests.QuestionRepository        = (*Questions)(nil)
	_ contests.VisibleQuestionRepository = (*Questions)(nil)
)

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

// cloneQuestion copies everything held by reference, for the same reason as
// cloneContest.
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

// ForContest mirrors the real query: visible questions in display order,
// and only those with a body in lang. A missing translation drops the
// question rather than serving it empty.
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

// Create writes only the question's own fields, as the real insert does: the
// identity and position are assigned here, and texts and answers are dropped.
func (r *Questions) Create(ctx context.Context, q contests.Question) (contests.Question, error) {
	// The real insert locks the contest row, which only holds inside a
	// transaction.
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
	// Position, contest, texts and answers are not Update's to change.
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

	// Close the gap: the real repository keeps positions 1..n.
	remaining, _ := r.List(ctx, q.ContestID)
	for i, other := range remaining {
		other.Ord = i + 1
		r.byID[other.ID] = other
	}
	return nil
}

func (r *Questions) Reorder(ctx context.Context, contestID uuid.UUID, ordered []uuid.UUID) error {
	// The real reorder defers its uniqueness check to commit, which needs a
	// transaction.
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
	// Ordered by value. The real query uses the database collation; byte
	// order agrees with it only for the lower-case ASCII values the contract
	// uses.
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

// Managers is an in-memory contests.ManagerRepository.
type Managers struct {
	byContest map[uuid.UUID][]contests.Manager
	// Accounts stands in for the join that names each entry at read time.
	// Nil leaves what the entry was stored with.
	Accounts AccountLookup
	// Clock stands in for the column default now() and overrides the
	// caller's GrantedAt. Nil keeps the caller's time.
	Clock func() time.Time
	// Lookups counts Get and List calls, so a test can bound the lookups a
	// bulk operation makes.
	Lookups int
}

var _ contests.ManagerRepository = (*Managers)(nil)

func NewManagers() *Managers {
	return &Managers{byContest: map[uuid.UUID][]contests.Manager{}}
}

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

// AccountLookup stands in for the join that names an account. Without it an
// entry comes back with an empty login, which the endpoint never returns.
type AccountLookup func(ctx context.Context, id uuid.UUID) (login, fullName string)

// PermissionLookup stands in for the join through the role tables. Without it
// every account holds nothing, which would let a test pass a publish gate the
// real one refuses.
type PermissionLookup func(ctx context.Context, id uuid.UUID) []string

// Registrations is an in-memory contests.RegistrationRepository.
type Registrations struct {
	byID        map[uuid.UUID]contests.Participant
	Accounts    AccountLookup
	Permissions PermissionLookup
	// Clock stands in for the column default now() in Add. Nil leaves
	// CreatedAt zero.
	Clock func() time.Time
	// ContestExists and UserExists stand in for the foreign keys Add meets.
	// Nil accepts everything; the fixture sets only ContestExists.
	ContestExists func(id uuid.UUID) bool
	UserExists    func(id uuid.UUID) bool
	work          map[uuid.UUID]bool
	// HasWorkInTx records whether the last HasWork ran inside a transaction.
	// A deletion that checks outside one decides on a state it no longer
	// writes against.
	HasWorkInTx bool
	// MissLookups makes ByUser miss an existing row, as when a concurrent
	// writer registers the same person between lookup and write, without
	// needing two goroutines.
	MissLookups bool
}

var _ contests.RegistrationRepository = (*Registrations)(nil)

func NewRegistrations() *Registrations {
	return &Registrations{byID: map[uuid.UUID]contests.Participant{}, work: map[uuid.UUID]bool{}}
}

// Put stores a participation as given, naming the account as Add does.
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
// behind it, which HasWork then reports.
func (r *Registrations) PutWork(registrationID uuid.UUID) {
	r.work[registrationID] = true
}

func (r *Registrations) HasWork(ctx context.Context, registrationID uuid.UUID) (bool, error) {
	r.HasWorkInTx = inTx(ctx)
	return r.work[registrationID], nil
}

// RegisteredWithPermission is ordered by login and capped at
// contests.MaxReportedStaff, as the real one is.
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

// matchesQuery mirrors the real escaped ILIKE on login or full name: a plain
// case-insensitive substring, where % and _ match themselves.
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
	// Checked against the stored rows, not through ByUser: the real guarantee
	// is a unique index, which holds even when a lookup missed.
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

// EnrolledIn reports which of the named contests, and only those, the user is
// registered for.
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

// Start mirrors postgres.Registrations.Start: it sets StartedAt and the
// active status together, once; a repeat call reads back the first. A
// registration in any other status (disqualified, say) is returned unchanged.
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

// Policies is an in-memory contests.PolicyStore.
type Policies struct {
	byContest map[uuid.UUID]contests.SQLPolicy
	// Clock stands in for the database's now() in Save. Nil leaves UpdatedAt
	// as it is.
	Clock func() time.Time
	// ContestExists stands in for the foreign key on the contest. Nil accepts
	// every contest.
	ContestExists func(id uuid.UUID) bool
}

var _ contests.PolicyStore = (*Policies)(nil)

func NewPolicies() *Policies {
	return &Policies{byContest: map[uuid.UUID]contests.SQLPolicy{}}
}

func (r *Policies) ByContest(_ context.Context, contestID uuid.UUID) (contests.SQLPolicy, error) {
	p, ok := r.byContest[contestID]
	if !ok {
		// An unconfigured contest is read-only, as in the real store.
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

// clonePolicy shares nothing with the store, and turns a nil list into the
// empty one a stored array reads back as.
func clonePolicy(p contests.SQLPolicy) contests.SQLPolicy {
	p.WritableTables = append([]string{}, p.WritableTables...)
	if p.UpdatedBy != nil {
		by := *p.UpdatedBy
		p.UpdatedBy = &by
	}
	return p
}

// Attempts is an in-memory contests.AttemptStore derived from the submission
// store, as the real one reads the submissions table. It has no state of its
// own, so a test cannot stage stats no sequence of submissions could produce.
type Attempts struct {
	submissions *Submissions
}

var _ contests.AttemptStore = (*Attempts)(nil)

func NewAttempts(submissions *Submissions) *Attempts {
	return &Attempts{submissions: submissions}
}

// ForRegistration mirrors postgres.Attempts.ForRegistration.
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

// Submissions is an in-memory contests.SubmissionRepository mirroring the
// real single INSERT: it refuses with ErrDeadlinePassed once the clock
// reaches req.Deadline, with ErrQuestionClosed once the question is solved or
// out of attempts, and otherwise takes the next attempt number.
//
// It cannot reproduce the unique-constraint race between two transactions;
// that is tested in internal/postgres/submissions_test.go. ConflictsRemaining
// drives Submit's retry loop without a second goroutine.
type Submissions struct {
	byKey map[submissionKey][]contests.Submission
	// Clock is what Insert checks req.Deadline against; nil is the wall clock.
	Clock func() time.Time
	// ConflictsRemaining makes the next n Insert calls return
	// ErrAttemptConflict without writing, as if they lost the attempt-number
	// race.
	ConflictsRemaining int
	// Requests is every request Insert received, in order, refused or not.
	Requests []contests.SubmissionRequest
}

type submissionKey struct {
	registrationID uuid.UUID
	questionID     uuid.UUID
}

var _ contests.SubmissionRepository = (*Submissions)(nil)

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
	// The deadline takes priority over "closed", as in the real statement.
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
		PointsAwarded:  pointsAwarded(req, len(existing)),
		SubmittedAt:    now,
	}
	r.byKey[key] = append(existing, s)
	return s, nil
}

// pointsAwarded mirrors the real statement: nothing for a wrong answer, else
// the face value minus the penalty for priorAttempts, floored at zero.
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

// SequentialProgress is an in-memory contests.SequentialGate derived from the
// question and submission stores, so it cannot drift from what Submit wrote.
type SequentialProgress struct {
	questions   *Questions
	submissions *Submissions
}

var _ contests.SequentialGate = (*SequentialProgress)(nil)

func NewSequentialProgress(questions *Questions, submissions *Submissions) *SequentialProgress {
	return &SequentialProgress{questions: questions, submissions: submissions}
}

// Open mirrors postgres.Sequence.Open: every question ordered before ord must
// be closed (solved, or out of attempts).
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

// Frontier mirrors postgres.Sequence.Frontier: the lowest-ordered question
// not yet closed, or uuid.Nil once all are.
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

// All lists the stored submissions for a registration and question, in
// insertion order.
func (r *Submissions) All(registrationID, questionID uuid.UUID) []contests.Submission {
	return append([]contests.Submission(nil), r.byKey[submissionKey{registrationID, questionID}]...)
}

// Languages is an in-memory contests.LanguageCatalog.
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

// Active orders by sort order, then code, as the table is read.
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
	// Loose holds the entries appended with no transaction open. An entry
	// must be written inside the change's transaction, or it can be lost or
	// outlive a rollback. Refusals have nothing to be atomic with, so a test
	// names which entries it expects here.
	Loose []audit.Entry
	// AppendManyErr, when set, is what AppendMany returns without recording:
	// the trail itself failing, so a test can prove the caller does not
	// swallow it.
	AppendManyErr error
}

var _ audit.Sink = (*Sink)(nil)

func NewSink() *Sink { return &Sink{} }

func (s *Sink) Append(ctx context.Context, e audit.Entry) error {
	s.Entries = append(s.Entries, e)
	if !inTx(ctx) {
		s.Loose = append(s.Loose, e)
	}
	return nil
}

// LatestStartBlocked reads Entries as postgres.AuditTrail reads audit_log:
// the newest entry for the contest decides.
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

// Games is an in-memory contests.GameSource, without the template lifecycle
// of internal/provisioning.
type Games struct {
	byContest map[uuid.UUID]string
	// files marks games built from an uploaded dump (PutFile). They are kept
	// apart from byContest because an empty script there is a distinct case
	// this fake must be able to reproduce.
	files map[uuid.UUID]bool
	// Err, when set, is what Script returns: the game storage being away.
	Err error
}

var _ contests.GameSource = (*Games)(nil)

func NewGames() *Games {
	return &Games{byContest: map[uuid.UUID]string{}, files: map[uuid.UUID]bool{}}
}

func (r *Games) Put(contestID uuid.UUID, script string) {
	r.byContest[contestID] = script
}

// PutFile gives the contest a game built from an uploaded dump, whose SQL no
// package can carry (provisioning.SourceFile).
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

func maps(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// Covers answers the publish gate's one question about a contest's cover. It
// fakes that narrow interface, not a covers.Repository.
type Covers struct {
	byContest map[uuid.UUID]string
	// Err, when set, is what Attribution returns: the covers table being away.
	Err error
}

func NewCovers() *Covers { return &Covers{byContest: map[uuid.UUID]string{}} }

// Put gives the contest an uploaded cover with that credit line. An empty one
// is the state the publish gate refuses.
func (r *Covers) Put(contestID uuid.UUID, attribution string) {
	r.byContest[contestID] = attribution
}

func (r *Covers) Attribution(_ context.Context, contestID uuid.UUID) (string, bool, error) {
	if r.Err != nil {
		return "", false, r.Err
	}
	attribution, uploaded := r.byContest[contestID]
	return attribution, uploaded, nil
}
