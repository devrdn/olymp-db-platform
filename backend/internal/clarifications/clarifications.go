// Package clarifications holds what the participants of a running contest ask
// its organisers, the organisers' answers, and their announcements: who may see
// which, the bounds on every text, the cap on questions waiting for an answer
// and the rate of asking.
//
// A participant sees their own questions, every answer the organisers made
// public (with the question it answers, never who asked it), and the
// announcements. Nothing here returns another participant's private question,
// and nothing returned to a participant names an asker.
//
// It does not decide whether a participant may act in the contest now (the
// participation gate does, before a call reaches here) or who is staff
// (internal/rbac), it holds no SQL, and it delivers no notification itself: it
// tells a Notifier which audience should look again.
package clarifications

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/google/uuid"
)

// The bounds of what reaches storage (CLAUDE.md rule 2). Lengths count
// characters, not bytes, so Cyrillic gets the same room as Latin.
const (
	// MaxQuestionRunes fits a question about one task with the query it is
	// about; a longer one is a second question.
	MaxQuestionRunes = 2000
	// MaxAnswerRunes leaves room to restate a corrected sentence of the
	// statement or quote a few rows; an announcement is the same kind of text.
	MaxAnswerRunes       = 4000
	MaxAnnouncementRunes = 4000
	// MaxOpenPerRegistration is how many of one participant's questions may
	// wait for an answer at once. It bounds the staff queue a participant can
	// fill, whatever the rate.
	MaxOpenPerRegistration = 20
	// AsksPerMinute is one registration's rate of asking, refused attempts
	// included (CLAUDE.md rule 13).
	AsksPerMinute = 3
	// AsksPerMinutePerAccount is charged before the participant is looked up,
	// so an account that is on no roster still pays for every attempt. Above
	// AsksPerMinute because one account may take part in two running contests.
	AsksPerMinutePerAccount = 6
	// DefaultPageSize and MaxPageSize bound every list.
	DefaultPageSize = 50
	MaxPageSize     = 100
)

const askWindow = time.Minute

// Why a call was refused (CLAUDE.md rule 1).
var (
	ErrQuestionTooLong     = fmt.Errorf("a question is at most %d characters", MaxQuestionRunes)
	ErrAnswerTooLong       = fmt.Errorf("an answer is at most %d characters", MaxAnswerRunes)
	ErrAnnouncementTooLong = fmt.Errorf("an announcement is at most %d characters", MaxAnnouncementRunes)
	// ErrEmpty is a question, an answer or an announcement with nothing but
	// spaces in it. A decline may leave its reply empty.
	ErrEmpty       = errors.New("the text is empty")
	ErrTextInvalid = errors.New("the text contains a NUL character or is not valid UTF-8")
	ErrTooManyOpen = fmt.Errorf("a participant may have at most %d questions waiting for an answer", MaxOpenPerRegistration)
	ErrTooOften    = errors.New("too many questions asked this minute")
	// ErrNotFound is also the answer for a clarification of another contest.
	ErrNotFound = errors.New("no such clarification in this contest")
	// ErrQuestionNotFound is a question named by an ask that is not one of the
	// contest's.
	ErrQuestionNotFound  = errors.New("no such question in this contest")
	ErrAlreadyAnswered   = errors.New("the question is already answered; edit the answer instead")
	ErrNotAnswered       = errors.New("the question has no answer to edit yet")
	ErrInvalidVisibility = errors.New("an answer's visibility is private or public")
	// ErrClosed refuses staff writes outside a running contest: no
	// participant could read them.
	ErrClosed          = errors.New("the contest is not running, so nothing can be answered or announced")
	ErrContestNotFound = errors.New("no such contest")
	ErrInvalidCursor   = errors.New("the cursor is not valid")
)

// Visibility is who reads an answer: its asker alone, or every participant.
type Visibility string

const (
	Private Visibility = "private"
	// Public shows the answer and the question it answers to every
	// participant, without the asker.
	Public Visibility = "public"
)

func (v Visibility) valid() bool { return v == Private || v == Public }

// Clarification is one participant's question and, once given, its answer.
type Clarification struct {
	ID             uuid.UUID
	ContestID      uuid.UUID
	RegistrationID uuid.UUID
	// QuestionID is the contest question it is about; nil for a general one.
	QuestionID *uuid.UUID
	Body       string
	CreatedAt  time.Time
	// Answer is nil while the question waits.
	Answer *Answer
}

// PostedAt is when the clarification last appeared in a feed: when it was
// asked, then when it was answered. An edit does not move it.
func (c Clarification) PostedAt() time.Time {
	if c.Answer != nil {
		return c.Answer.At
	}
	return c.CreatedAt
}

// Visibility is Private until an answer says otherwise.
func (c Clarification) Visibility() Visibility {
	if c.Answer == nil {
		return Private
	}
	return c.Answer.Visibility
}

// Answer is the organisers' reply. A declined question carries a reply that
// may be empty.
type Answer struct {
	Body       string
	Declined   bool
	Visibility Visibility
	// By is nil once the account that answered is gone.
	By       *uuid.UUID
	At       time.Time
	EditedAt *time.Time
}

// Announcement is a message from the organisers to every participant.
type Announcement struct {
	ID        uuid.UUID
	ContestID uuid.UUID
	Body      string
	// Author is nil once the account that wrote it is gone.
	Author    *uuid.UUID
	CreatedAt time.Time
}

// Entry is one row of a feed as storage returns it: exactly one of the two is
// set.
type Entry struct {
	Clarification *Clarification
	Announcement  *Announcement
}

func (e Entry) cursor() Cursor {
	if e.Announcement != nil {
		return Cursor{At: e.Announcement.CreatedAt, ID: e.Announcement.ID}
	}
	return Cursor{At: e.Clarification.PostedAt(), ID: e.Clarification.ID}
}

// Asked is a clarification as staff see it, with who asked.
type Asked struct {
	Clarification
	AskerLogin    string
	AskerFullName string
	// AnsweredByLogin is empty while unanswered or once that account is gone.
	AnsweredByLogin string
}

// State selects one half of the staff queue.
type State string

const (
	// StateOpen is the questions waiting, oldest first.
	StateOpen State = "open"
	// StateAnswered is the answered ones, most recently answered first.
	StateAnswered State = "answered"
)

// Counts is how many of a contest's questions wait and how many are answered.
type Counts struct {
	Open, Answered int
}

// Cursor is a position in a feed: entries strictly older than it come next.
type Cursor struct {
	At time.Time
	ID uuid.UUID
}

// latestCursor stands for "from the newest"; past any time a row can hold.
var latestCursor = Cursor{At: time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC), ID: uuid.Max}

// The range of times a parsed cursor may name, so a client-sent time
// PostgreSQL cannot hold is refused rather than a 500.
var (
	earliestCursorTime = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	latestCursorTime   = time.Date(2200, 1, 1, 0, 0, 0, 0, time.UTC)
)

// maxCursorLength bounds what ParseCursor decodes: microseconds, a dot and a
// uuid, base64-encoded, are well under it.
const maxCursorLength = 128

// String encodes the cursor for a client to send back as it is.
func (c Cursor) String() string {
	raw := strconv.FormatInt(c.At.UnixMicro(), 10) + "." + c.ID.String()
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// ParseCursor reads what String wrote, or refuses with ErrInvalidCursor.
func ParseCursor(text string) (Cursor, error) {
	if text == "" || len(text) > maxCursorLength {
		return Cursor{}, ErrInvalidCursor
	}
	raw, err := base64.RawURLEncoding.DecodeString(text)
	if err != nil {
		return Cursor{}, ErrInvalidCursor
	}
	micros, id, ok := strings.Cut(string(raw), ".")
	if !ok {
		return Cursor{}, ErrInvalidCursor
	}
	at, err := strconv.ParseInt(micros, 10, 64)
	if err != nil || at < earliestCursorTime.UnixMicro() || at > latestCursorTime.UnixMicro() {
		return Cursor{}, ErrInvalidCursor
	}
	parsed, err := uuid.Parse(id)
	if err != nil || parsed.String() != id {
		return Cursor{}, ErrInvalidCursor
	}
	return Cursor{At: time.UnixMicro(at).UTC(), ID: parsed}, nil
}

// Repository is the storage this service needs. Every method that names a
// contest and a clarification treats one of another contest as absent.
type Repository interface {
	// Ask stores c, refusing with ErrTooManyOpen when its registration
	// already has openLimit unanswered questions, and with
	// ErrQuestionNotFound when c.QuestionID is not a question of c.ContestID.
	// Two concurrent asks never both pass the count.
	Ask(ctx context.Context, c Clarification, openLimit int) (Clarification, error)
	// Feed returns at most limit entries posted strictly before `before`,
	// newest first: the registration's own clarifications, every public one
	// of the contest, and the contest's announcements.
	Feed(ctx context.Context, contestID, registrationID uuid.UUID, before Cursor, limit int) ([]Entry, error)
	// OpenCount is how many of the registration's questions wait.
	OpenCount(ctx context.Context, registrationID uuid.UUID) (int, error)
	// Queue pages one state of the contest's questions (see State).
	Queue(ctx context.Context, contestID uuid.UUID, state State, limit, offset int) ([]Asked, error)
	Counts(ctx context.Context, contestID uuid.UUID) (Counts, error)
	// Answer gives an unanswered clarification its answer: ErrNotFound, or
	// ErrAlreadyAnswered when it has one.
	Answer(ctx context.Context, contestID, id uuid.UUID, answer Answer) (Clarification, error)
	// EditAnswer replaces an answer's text, decline and visibility, stamping
	// EditedAt, and returns the clarification before and after. It keeps the
	// first answer's author and time. ErrNotFound, or ErrNotAnswered.
	EditAnswer(ctx context.Context, contestID, id uuid.UUID, answer Answer) (before, after Clarification, err error)
	Announce(ctx context.Context, a Announcement) (Announcement, error)
	// Announcements pages the contest's announcements, newest first, with
	// their total.
	Announcements(ctx context.Context, contestID uuid.UUID, limit, offset int) ([]Announcement, int, error)
}

// Contests reads the contest a staff write is for.
type Contests interface {
	// ByID returns contests.ErrNotFound for a contest that does not exist.
	ByID(ctx context.Context, id uuid.UUID) (contests.Contest, error)
}

// Limiter is the slice of auth.Limiter the rate of asking needs.
type Limiter interface {
	Allow(ctx context.Context, subject string, limit int, window time.Duration) (bool, error)
}

// Notifier tells the open event channels of one audience that what they show
// has changed. A topic is a contest ID (every participant of it) or a
// registration ID (that participant). It is told after the change commits,
// never fails, and must not block.
type Notifier interface {
	Publish(topic uuid.UUID)
}

// Config is what a Service is built from.
type Config struct {
	Repository Repository
	Contests   Contests
	Limiter    Limiter
	// Notifier is optional; nil tells nobody.
	Notifier   Notifier
	Audit      *audit.Recorder
	UnitOfWork storage.UnitOfWork
	// Now is optional; it stamps every write.
	Now func() time.Time
}

// Service applies the rules over a Repository.
type Service struct {
	repo     Repository
	contests Contests
	limiter  Limiter
	notifier Notifier
	audit    *audit.Recorder
	uow      storage.UnitOfWork
	now      func() time.Time
}

// NewService builds the service. It panics on a missing dependency: an answer
// is never recorded without its audit entry, nor a question asked unthrottled.
func NewService(cfg Config) *Service {
	if cfg.Repository == nil || cfg.Contests == nil || cfg.Limiter == nil || cfg.Audit == nil || cfg.UnitOfWork == nil {
		panic("clarifications: NewService needs a repository, contests, a limiter, the audit recorder and its unit of work")
	}
	now := cfg.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Service{
		repo: cfg.Repository, contests: cfg.Contests, limiter: cfg.Limiter, notifier: cfg.Notifier,
		audit: cfg.Audit, uow: cfg.UnitOfWork, now: now,
	}
}

// RetryAfter is the longest a caller refused with ErrTooOften should wait: the
// whole window, since its start is not known here.
func RetryAfter() time.Duration { return askWindow }

// AdmitAsk spends one ask of the account's budget. The caller asks it before
// any lookup or body read, so every attempt counts; the account is known
// before the lookups and never named by the request (CLAUDE.md rule 5).
func (s *Service) AdmitAsk(ctx context.Context, account uuid.UUID) error {
	return s.spend(ctx, "clarify:user:"+account.String(), AsksPerMinutePerAccount)
}

func (s *Service) spend(ctx context.Context, subject string, limit int) error {
	allowed, err := s.limiter.Allow(ctx, subject, limit, askWindow)
	if err != nil {
		// A counter that cannot be kept refuses, but not with ErrTooOften.
		return fmt.Errorf("check the rate of asking: %w", err)
	}
	if !allowed {
		return ErrTooOften
	}
	return nil
}

// AskCommand is a participant's question, after the participation gate
// admitted them.
type AskCommand struct {
	ContestID      uuid.UUID
	RegistrationID uuid.UUID
	QuestionID     *uuid.UUID
	Body           string
}

// Ask stores a question. It spends the registration's budget before looking at
// the text, so a refused question still counts. A question is not audited:
// it is stored, and only the organisers' acts are.
func (s *Service) Ask(ctx context.Context, cmd AskCommand) (Clarification, error) {
	if err := s.spend(ctx, "clarify:reg:"+cmd.RegistrationID.String(), AsksPerMinute); err != nil {
		return Clarification{}, err
	}
	body, err := cleanText(cmd.Body, MaxQuestionRunes, ErrQuestionTooLong, false)
	if err != nil {
		return Clarification{}, err
	}
	asked, err := s.repo.Ask(ctx, Clarification{
		ContestID: cmd.ContestID, RegistrationID: cmd.RegistrationID,
		QuestionID: cmd.QuestionID, Body: body, CreatedAt: s.now(),
	}, MaxOpenPerRegistration)
	if err != nil {
		if errors.Is(err, ErrTooManyOpen) || errors.Is(err, ErrQuestionNotFound) {
			return Clarification{}, err
		}
		return Clarification{}, fmt.Errorf("store the question: %w", err)
	}
	return asked, nil
}

// FeedQuery asks for one page of what a participant may see.
type FeedQuery struct {
	ContestID      uuid.UUID
	RegistrationID uuid.UUID
	// Before is nil for the newest page.
	Before *Cursor
	// Limit is clamped to 1..MaxPageSize; zero asks for DefaultPageSize.
	Limit int
}

// FeedPage is one page of a participant's feed.
type FeedPage struct {
	Items []FeedItem
	// Next is nil on the last page.
	Next *Cursor
	// Open is how many of this participant's questions wait, whatever the
	// page.
	Open int
}

// FeedItem is one entry as a participant sees it: exactly one of the two is
// set.
type FeedItem struct {
	ID       uuid.UUID
	PostedAt time.Time
	// Clarification has no field for the asker: Mine is all a participant
	// learns about who asked.
	Clarification *Seen
	Announcement  *Announcement
}

// Seen is a clarification as one participant sees it.
type Seen struct {
	Mine       bool
	QuestionID *uuid.UUID
	Body       string
	AskedAt    time.Time
	Answer     *Answer
}

// Feed returns one page of the participant's own questions, the public
// answers and the announcements, newest first.
func (s *Service) Feed(ctx context.Context, q FeedQuery) (FeedPage, error) {
	limit := clampLimit(q.Limit)
	before := latestCursor
	if q.Before != nil {
		before = *q.Before
	}
	entries, err := s.repo.Feed(ctx, q.ContestID, q.RegistrationID, before, limit+1)
	if err != nil {
		return FeedPage{}, fmt.Errorf("read the feed: %w", err)
	}
	open, err := s.repo.OpenCount(ctx, q.RegistrationID)
	if err != nil {
		return FeedPage{}, fmt.Errorf("count the open questions: %w", err)
	}

	page := FeedPage{Items: make([]FeedItem, 0, min(len(entries), limit)), Open: open}
	if len(entries) > limit {
		entries = entries[:limit]
		next := entries[limit-1].cursor()
		page.Next = &next
	}
	for _, e := range entries {
		page.Items = append(page.Items, seenBy(e, q.RegistrationID))
	}
	return page, nil
}

// seenBy is e as the participant holding registration sees it.
func seenBy(e Entry, registration uuid.UUID) FeedItem {
	if e.Announcement != nil {
		a := *e.Announcement
		return FeedItem{ID: a.ID, PostedAt: a.CreatedAt, Announcement: &a}
	}
	c := e.Clarification
	return FeedItem{ID: c.ID, PostedAt: c.PostedAt(), Clarification: &Seen{
		Mine: c.RegistrationID == registration, QuestionID: c.QuestionID,
		Body: c.Body, AskedAt: c.CreatedAt, Answer: c.Answer,
	}}
}

// QueueQuery asks for one page of one state of the staff queue.
type QueueQuery struct {
	ContestID uuid.UUID
	State     State
	Limit     int
	Offset    int
}

// QueuePage is one page of the staff queue, with both states' counts.
type QueuePage struct {
	Items  []Asked
	Counts Counts
}

// Queue pages the contest's questions for its staff. An unknown state reads
// the open ones.
func (s *Service) Queue(ctx context.Context, q QueueQuery) (QueuePage, error) {
	state := q.State
	if state != StateAnswered {
		state = StateOpen
	}
	items, err := s.repo.Queue(ctx, q.ContestID, state, clampLimit(q.Limit), max(q.Offset, 0))
	if err != nil {
		return QueuePage{}, fmt.Errorf("read the queue: %w", err)
	}
	counts, err := s.repo.Counts(ctx, q.ContestID)
	if err != nil {
		return QueuePage{}, fmt.Errorf("count the questions: %w", err)
	}
	return QueuePage{Items: items, Counts: counts}, nil
}

// Announcements pages the contest's announcements for its staff, newest first.
func (s *Service) Announcements(ctx context.Context, contestID uuid.UUID, limit, offset int) ([]Announcement, int, error) {
	items, total, err := s.repo.Announcements(ctx, contestID, clampLimit(limit), max(offset, 0))
	if err != nil {
		return nil, 0, fmt.Errorf("read the announcements: %w", err)
	}
	return items, total, nil
}

// AnswerCommand is a staff member's answer, or its replacement.
type AnswerCommand struct {
	ContestID       uuid.UUID
	ClarificationID uuid.UUID
	Actor           uuid.UUID
	Body            string
	Declined        bool
	Visibility      Visibility
}

func (cmd AnswerCommand) answer(body string, at time.Time) Answer {
	actor := cmd.Actor
	return Answer{Body: body, Declined: cmd.Declined, Visibility: cmd.Visibility, By: &actor, At: at}
}

// Answer answers a waiting question, once. A public answer shows the question
// with it to every participant, and is recorded as a publication.
func (s *Service) Answer(ctx context.Context, cmd AnswerCommand) (Clarification, error) {
	body, err := s.checkAnswer(ctx, cmd)
	if err != nil {
		return Clarification{}, err
	}
	var answered Clarification
	err = s.uow.Do(ctx, func(ctx context.Context) error {
		var err error
		if answered, err = s.repo.Answer(ctx, cmd.ContestID, cmd.ClarificationID, cmd.answer(body, s.now())); err != nil {
			return err
		}
		action := audit.ActionClarificationAnswer
		if answered.Visibility() == Public {
			action = audit.ActionClarificationPublish
		}
		return s.record(ctx, cmd.Actor, action, cmd.ContestID, answerPayload(answered))
	})
	if err != nil {
		return Clarification{}, writeError(err, "answer the question")
	}
	s.tell(answered, answered.Visibility() == Public)
	return answered, nil
}

// EditAnswer replaces an existing answer. Turning a private answer public is
// recorded as a publication; any other change as an edit, with the text it
// replaced.
func (s *Service) EditAnswer(ctx context.Context, cmd AnswerCommand) (Clarification, error) {
	body, err := s.checkAnswer(ctx, cmd)
	if err != nil {
		return Clarification{}, err
	}
	var before, after Clarification
	err = s.uow.Do(ctx, func(ctx context.Context) error {
		var err error
		replacement := cmd.answer(body, s.now())
		if before, after, err = s.repo.EditAnswer(ctx, cmd.ContestID, cmd.ClarificationID, replacement); err != nil {
			return err
		}
		action := audit.ActionClarificationEdit
		if before.Visibility() == Private && after.Visibility() == Public {
			action = audit.ActionClarificationPublish
		}
		payload := answerPayload(after)
		payload["previous_visibility"] = string(before.Visibility())
		payload["previous_declined"] = before.Answer.Declined
		payload["previous_answer"] = before.Answer.Body
		return s.record(ctx, cmd.Actor, action, cmd.ContestID, payload)
	})
	if err != nil {
		return Clarification{}, writeError(err, "edit the answer")
	}
	// An answer taken back from everyone must leave every screen too.
	s.tell(after, before.Visibility() == Public || after.Visibility() == Public)
	return after, nil
}

// checkAnswer validates an answer and that the contest still runs.
func (s *Service) checkAnswer(ctx context.Context, cmd AnswerCommand) (string, error) {
	if !cmd.Visibility.valid() {
		return "", ErrInvalidVisibility
	}
	body, err := cleanText(cmd.Body, MaxAnswerRunes, ErrAnswerTooLong, cmd.Declined)
	if err != nil {
		return "", err
	}
	return body, s.running(ctx, cmd.ContestID)
}

// AnnounceCommand is a message from staff to every participant.
type AnnounceCommand struct {
	ContestID uuid.UUID
	Actor     uuid.UUID
	Body      string
}

// Announce posts a message to every participant of the contest.
func (s *Service) Announce(ctx context.Context, cmd AnnounceCommand) (Announcement, error) {
	body, err := cleanText(cmd.Body, MaxAnnouncementRunes, ErrAnnouncementTooLong, false)
	if err != nil {
		return Announcement{}, err
	}
	if err := s.running(ctx, cmd.ContestID); err != nil {
		return Announcement{}, err
	}
	actor := cmd.Actor
	var posted Announcement
	err = s.uow.Do(ctx, func(ctx context.Context) error {
		var err error
		if posted, err = s.repo.Announce(ctx, Announcement{
			ContestID: cmd.ContestID, Body: body, Author: &actor, CreatedAt: s.now(),
		}); err != nil {
			return err
		}
		return s.record(ctx, cmd.Actor, audit.ActionAnnouncementCreate, cmd.ContestID, map[string]any{
			"announcement_id": posted.ID.String(),
		})
	})
	if err != nil {
		return Announcement{}, fmt.Errorf("post the announcement: %w", err)
	}
	if s.notifier != nil {
		s.notifier.Publish(cmd.ContestID)
	}
	return posted, nil
}

// running refuses a staff write to a contest that is not running. It is
// checked before the write rather than with it: an answer that lands at the
// instant the contest finishes harms nobody.
func (s *Service) running(ctx context.Context, contestID uuid.UUID) error {
	contest, err := s.contests.ByID(ctx, contestID)
	if errors.Is(err, contests.ErrNotFound) {
		return ErrContestNotFound
	}
	if err != nil {
		return fmt.Errorf("read the contest: %w", err)
	}
	if contest.Status != contests.StatusRunning {
		return ErrClosed
	}
	return nil
}

// tell notifies whoever's screen shows c: everybody, or its asker alone.
func (s *Service) tell(c Clarification, everybody bool) {
	if s.notifier == nil {
		return
	}
	if everybody {
		s.notifier.Publish(c.ContestID)
		return
	}
	s.notifier.Publish(c.RegistrationID)
}

func (s *Service) record(ctx context.Context, actor uuid.UUID, action string, contestID uuid.UUID, payload map[string]any) error {
	return s.audit.Record(ctx, audit.Entry{
		ActorID: &actor, Action: action, Entity: "contest", EntityID: contestID.String(), Payload: payload,
	})
}

func answerPayload(c Clarification) map[string]any {
	payload := map[string]any{
		"clarification_id": c.ID.String(),
		"registration_id":  c.RegistrationID.String(),
		"visibility":       string(c.Visibility()),
		"declined":         c.Answer.Declined,
	}
	if c.QuestionID != nil {
		payload["question_id"] = c.QuestionID.String()
	}
	return payload
}

// writeError passes a refusal through as it is and wraps anything else.
func writeError(err error, doing string) error {
	for _, refusal := range []error{ErrNotFound, ErrAlreadyAnswered, ErrNotAnswered} {
		if errors.Is(err, refusal) {
			return err
		}
	}
	return fmt.Errorf("%s: %w", doing, err)
}

// cleanText trims text and checks it against its bound. allowEmpty is for a
// decline, whose reply may be left out.
func cleanText(text string, maxRunes int, tooLong error, allowEmpty bool) (string, error) {
	if strings.ContainsRune(text, 0) || !utf8.ValidString(text) {
		return "", ErrTextInvalid
	}
	clean := strings.TrimSpace(text)
	if clean == "" && !allowEmpty {
		return "", ErrEmpty
	}
	if utf8.RuneCountInString(clean) > maxRunes {
		return "", tooLong
	}
	return clean, nil
}

func clampLimit(limit int) int {
	if limit <= 0 {
		return DefaultPageSize
	}
	return min(limit, MaxPageSize)
}
