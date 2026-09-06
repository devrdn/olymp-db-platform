// Package conteststest provides in-memory implementations of the storage the
// contests package declares, plus a fixture that assembles a service from
// them.
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
	"context"
	"slices"
	"strings"
	"time"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/rbac"
	"github.com/google/uuid"
)

// Contests is an in-memory contests.Repository.
type Contests struct {
	byID map[uuid.UUID]contests.Contest
	// order preserves insertion order, so listings are deterministic.
	order []uuid.UUID
	// racesTo stages one concurrent status change; see SetStatusRaces.
	racesTo string
}

var _ contests.Repository = (*Contests)(nil)

// NewContests returns an empty contest store.
func NewContests() *Contests {
	return &Contests{byID: map[uuid.UUID]contests.Contest{}}
}

// Count reports how many contests are stored.
func (r *Contests) Count() int { return len(r.byID) }

// Put stores a contest as given, for tests that need a particular state.
func (r *Contests) Put(c contests.Contest) contests.Contest {
	if c.ID == uuid.Nil {
		c.ID = uuid.New()
	}
	if _, exists := r.byID[c.ID]; !exists {
		r.order = append(r.order, c.ID)
	}
	r.byID[c.ID] = c
	return c
}

func (r *Contests) Create(_ context.Context, c contests.Contest) (contests.Contest, error) {
	c.ID = uuid.New()
	return r.Put(c), nil
}

func (r *Contests) ByID(_ context.Context, id uuid.UUID) (contests.Contest, error) {
	c, ok := r.byID[id]
	if !ok {
		return contests.Contest{}, contests.ErrNotFound
	}
	return c, nil
}

func (r *Contests) List(_ context.Context, f contests.Filter) ([]contests.Contest, int, error) {
	var matched []contests.Contest
	for _, id := range r.order {
		c := r.byID[id]
		if f.Status != "" && c.Status != f.Status {
			continue
		}
		if f.Query != "" && !matchesTitle(c, f.Query) {
			continue
		}
		matched = append(matched, c)
	}

	total := len(matched)
	if f.Offset >= total {
		return nil, total, nil
	}
	end := min(f.Offset+f.Limit, total)
	return matched[f.Offset:end], total, nil
}

func matchesTitle(c contests.Contest, query string) bool {
	for _, t := range c.Translations {
		if strings.Contains(strings.ToLower(t.Title), strings.ToLower(query)) {
			return true
		}
	}
	return false
}

func (r *Contests) Update(_ context.Context, c contests.Contest) error {
	stored, ok := r.byID[c.ID]
	if !ok {
		return contests.ErrNotFound
	}
	// Status is not among the updatable fields, matching the real repository.
	c.Status = stored.Status
	r.byID[c.ID] = c
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

// Stories is an in-memory contests.StoryRepository.
type Stories struct {
	byContest map[uuid.UUID]contests.Story
}

var _ contests.StoryRepository = (*Stories)(nil)

// NewStories returns an empty story store.
func NewStories() *Stories {
	return &Stories{byContest: map[uuid.UUID]contests.Story{}}
}

func (r *Stories) ByContest(_ context.Context, contestID uuid.UUID) (contests.Story, error) {
	story, ok := r.byContest[contestID]
	if !ok {
		return contests.Story{}, contests.ErrStoryNotFound
	}
	return story, nil
}

func (r *Stories) Save(_ context.Context, contestID uuid.UUID, bodies map[string]string) (contests.Story, error) {
	story, ok := r.byContest[contestID]
	if !ok {
		story = contests.Story{ID: uuid.New(), ContestID: contestID}
	}
	story.Bodies = maps(bodies)
	r.byContest[contestID] = story
	return story, nil
}

func (r *Stories) Delete(_ context.Context, contestID uuid.UUID) error {
	delete(r.byContest, contestID)
	return nil
}

// Questions is an in-memory contests.QuestionRepository.
type Questions struct {
	byID map[uuid.UUID]contests.Question
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
	r.byID[q.ID] = q
	return q
}

func (r *Questions) List(_ context.Context, contestID uuid.UUID) ([]contests.Question, error) {
	var found []contests.Question
	for _, q := range r.byID {
		if q.ContestID == contestID {
			found = append(found, q)
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
	return q, nil
}

func (r *Questions) Create(ctx context.Context, q contests.Question) (contests.Question, error) {
	existing, _ := r.List(ctx, q.ContestID)
	q.ID = uuid.New()
	q.Ord = len(existing) + 1
	return r.Put(q), nil
}

func (r *Questions) Update(_ context.Context, q contests.Question) error {
	stored, ok := r.byID[q.ID]
	if !ok {
		return contests.ErrQuestionNotFound
	}
	// Position, text and answers have their own operations, exactly as in the
	// real repository.
	q.Ord, q.Texts, q.Answers = stored.Ord, stored.Texts, stored.Answers
	r.byID[q.ID] = q
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

func (r *Questions) Reorder(_ context.Context, contestID uuid.UUID, ordered []uuid.UUID) error {
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
	q.Texts = texts
	r.byID[questionID] = q
	return nil
}

func (r *Questions) ReplaceAnswers(_ context.Context, questionID uuid.UUID, answers []contests.Answer) error {
	q, ok := r.byID[questionID]
	if !ok {
		return contests.ErrQuestionNotFound
	}
	q.Answers = slices.Clone(answers)
	r.byID[questionID] = q
	return nil
}

// Managers is an in-memory contests.ManagerRepository.
type Managers struct {
	byContest map[uuid.UUID][]contests.Manager
}

var _ contests.ManagerRepository = (*Managers)(nil)

// NewManagers returns an empty staff store.
func NewManagers() *Managers {
	return &Managers{byContest: map[uuid.UUID][]contests.Manager{}}
}

func (r *Managers) List(_ context.Context, contestID uuid.UUID) ([]contests.Manager, error) {
	staff := slices.Clone(r.byContest[contestID])
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

func (r *Managers) Get(_ context.Context, contestID, userID uuid.UUID) (contests.Manager, error) {
	for _, m := range r.byContest[contestID] {
		if m.UserID == userID {
			return m, nil
		}
	}
	return contests.Manager{}, contests.ErrManagerNotFound
}

func (r *Managers) Grant(_ context.Context, m contests.Manager) error {
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
// repository does. Without it a participant would come back with an empty
// login, which is not what the endpoint returns and not what a test should be
// allowed to pass against.
type AccountLookup func(ctx context.Context, id uuid.UUID) (login, fullName string)

// Registrations is an in-memory contests.RegistrationRepository.
type Registrations struct {
	byID map[uuid.UUID]contests.Participant
	// Accounts resolves the login and name carried on every participant.
	Accounts AccountLookup
	// MissLookups makes ByUser report "not found" even when the row is there,
	// which is what a caller sees when a concurrent writer registered the same
	// person between the lookup and the write. It exists so that path can be
	// exercised without two goroutines.
	MissLookups bool
}

var _ contests.RegistrationRepository = (*Registrations)(nil)

// NewRegistrations returns an empty registration store.
func NewRegistrations() *Registrations {
	return &Registrations{byID: map[uuid.UUID]contests.Participant{}}
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

func (r *Registrations) List(_ context.Context, contestID uuid.UUID, f contests.ParticipantFilter) ([]contests.Participant, int, error) {
	var matched []contests.Participant
	for _, p := range r.byID {
		if p.ContestID != contestID {
			continue
		}
		if f.Status != "" && p.Status != f.Status {
			continue
		}
		if f.Query != "" && !strings.Contains(strings.ToLower(p.Login), strings.ToLower(f.Query)) {
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
// moving the clock.
func (r *Registrations) Start(_ context.Context, registrationID uuid.UUID, now time.Time) (contests.Participant, error) {
	p, ok := r.byID[registrationID]
	if !ok {
		return contests.Participant{}, contests.ErrParticipantNotFound
	}
	if p.StartedAt == nil {
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
	return p, nil
}

func (r *Policies) Save(_ context.Context, p contests.SQLPolicy) error {
	r.byContest[p.ContestID] = p
	return nil
}

// Attempts is an in-memory contests.AttemptStore.
type Attempts struct {
	byRegistration map[uuid.UUID]map[uuid.UUID]contests.AttemptStats
}

var _ contests.AttemptStore = (*Attempts)(nil)

// NewAttempts returns an empty attempt store.
func NewAttempts() *Attempts {
	return &Attempts{byRegistration: map[uuid.UUID]map[uuid.UUID]contests.AttemptStats{}}
}

// Put stages one registration's attempt count and correctness on a question,
// as the real repository would derive it from the submissions it has
// recorded.
func (r *Attempts) Put(registrationID, questionID uuid.UUID, stats contests.AttemptStats) {
	if r.byRegistration[registrationID] == nil {
		r.byRegistration[registrationID] = map[uuid.UUID]contests.AttemptStats{}
	}
	r.byRegistration[registrationID][questionID] = stats
}

func (r *Attempts) ForRegistration(_ context.Context, registrationID uuid.UUID) (map[uuid.UUID]contests.AttemptStats, error) {
	out := make(map[uuid.UUID]contests.AttemptStats, len(r.byRegistration[registrationID]))
	for id, stats := range r.byRegistration[registrationID] {
		out[id] = stats
	}
	return out, nil
}

// Submissions is an in-memory contests.SubmissionRepository.
//
// It mirrors what the real repository's one INSERT statement guarantees
// (postgres.Submissions.Insert): a submission is refused with
// ErrQuestionClosed once the question is already answered correctly or every
// attempt is spent, and otherwise takes the next attempt number. It does not
// reproduce the real repository's concurrency guarantee — a Go map has no
// analogue of the table's own UNIQUE constraint racing two transactions — so
// the genuine race (finding 3) is proven where it can actually happen,
// against PostgreSQL (internal/postgres/submissions_test.go), not here.
// ConflictsRemaining exists so a Service-level test can still exercise
// Submit's own retry loop deterministically, without a second goroutine.
type Submissions struct {
	byKey map[submissionKey][]contests.Submission
	// Clock answers Now(); nil defaults to the real wall clock so a test that
	// never sets it still gets a moving clock rather than the zero value.
	Clock func() time.Time
	// ConflictsRemaining makes the next this-many Insert calls return
	// ErrAttemptConflict instead of writing anything, simulating a
	// submission that lost the attempt-number race and must be retried.
	ConflictsRemaining int
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

func (r *Submissions) Now(context.Context) (time.Time, error) {
	if r.Clock != nil {
		return r.Clock(), nil
	}
	return time.Now().UTC(), nil
}

func (r *Submissions) Insert(_ context.Context, req contests.SubmissionRequest) (contests.Submission, error) {
	if r.ConflictsRemaining > 0 {
		r.ConflictsRemaining--
		return contests.Submission{}, contests.ErrAttemptConflict
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
		PointsAwarded:  req.PointsAwarded,
		SubmittedAt:    req.SubmittedAt,
	}
	r.byKey[key] = append(existing, s)
	return s, nil
}

// All lists every submission stored for this registration and question, in
// the order they were inserted, so a test can inspect exactly what was
// written.
func (r *Submissions) All(registrationID, questionID uuid.UUID) []contests.Submission {
	return append([]contests.Submission(nil), r.byKey[submissionKey{registrationID, questionID}]...)
}

// Languages is a fixed language catalog.
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

func (r *Languages) Active(context.Context) ([]contests.Language, error) {
	var active []contests.Language
	for _, l := range r.Available {
		if l.IsActive {
			active = append(active, l)
		}
	}
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

// AppendMany appends every entry the same way Append does, one at a time:
// what a test asserts on is the resulting state, not the round trips it took.
func (s *Sink) AppendMany(ctx context.Context, entries []audit.Entry) error {
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

// maps copies a map so a stored value cannot be mutated through the caller's
// reference.
func maps(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
