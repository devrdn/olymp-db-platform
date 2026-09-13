package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/devrdn/db-contest/backend/internal/auth"
	"github.com/devrdn/db-contest/backend/internal/contests"
	"github.com/devrdn/db-contest/backend/internal/platform/httpx"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// The contest package: one olympiad as a file an organizer can edit and,
// once the other half of docs/ARCHITECTURE.md §15 item 12 is built, load back
// in. Export exists first and on its own because the useful direction is
// "take last year's and change it", and because an importer with nothing to
// import is a form nobody can fill.
//
// What makes this file the sensitive one in the whole API: it carries the
// reference answers. Everything about how it is served follows from that —
// the permission (contests.edit, not contest.view), the audit entry, and the
// deliberate absence of any participant-reachable route to it.
const (
	// packageFormat names the shape, so a file found on a disk in a year can
	// be identified without guessing, and so an importer can refuse a
	// spreadsheet somebody renamed.
	packageFormat = "dbcontest.contest-package"
	// packageVersion is this shape's revision. An importer reads it before
	// anything else; a package from a future version is refused rather than
	// half-understood.
	packageVersion = 1
)

// ContestPackage is one contest as a file.
//
// A wire type of its own rather than the domain object serialised, for the
// reason every other response here has one: direct serialisation publishes
// whatever field is added to the domain next, and on this endpoint "whatever
// is added next" would be published together with the answer key.
//
// What it deliberately does not carry, and why:
//   - identifiers — an importing installation mints its own, and a stale uuid
//     in a file is an invitation to write one that does not exist;
//   - the status, the window, the enrollment deadline — facts about the run
//     this came from, not about the contest;
//   - the roster, the registrations, the submissions and the query log — this
//     is not a backup, and pretending otherwise would be the more dangerous
//     of the two mistakes.
type ContestPackage struct {
	Format  string `json:"format"`
	Version int    `json:"version"`
	// ExportedAt is when this file was made, which is the one fact about the
	// export itself worth keeping in it.
	ExportedAt string             `json:"exported_at"`
	Contest    PackagedContest    `json:"contest"`
	Story      map[string]string  `json:"story"`
	Questions  []PackagedQuestion `json:"questions"`
	SQLPolicy  PackagedSQLPolicy  `json:"sql_policy"`
	Game       *PackagedGame      `json:"game,omitempty"`
}

// PackagedContest is the contest's own configuration and authored titles.
type PackagedContest struct {
	Enrollment   string `json:"enrollment"`
	QuestionMode string `json:"question_mode"`
	Progression  string `json:"progression"`
	Scoring      string `json:"scoring"`
	Timing       string `json:"timing"`
	// DurationMin is the per-participant session length, present only for
	// individual timing. Unlike the window, it is a setting rather than a
	// moment: "ninety minutes each" is as true next year as it was last.
	DurationMin *int `json:"duration_min,omitempty"`
	// AllowedCIDRs is the network restriction — the university's own ranges,
	// which is configuration worth carrying over, not a fact about one run.
	AllowedCIDRs []string         `json:"allowed_cidrs"`
	Settings     PackagedSettings `json:"settings"`
	// Leaderboard is how the table is frozen and labelled — configuration.
	// When results were revealed is a fact about one run and stays behind.
	Leaderboard PackagedLeaderboard `json:"leaderboard"`
	// DefaultLanguage is the language served when the requested one is
	// missing. Named separately as well as flagged in Languages because it is
	// the one thing about the set that must survive a round trip intact.
	DefaultLanguage string `json:"default_language"`
	// Languages are the declared codes, in declaration order.
	Languages    []string                       `json:"languages"`
	Translations map[string]TranslationResponse `json:"translations"`
}

// PackagedLeaderboard is the table's configuration.
type PackagedLeaderboard struct {
	FreezeMin *int   `json:"freeze_min"`
	Names     string `json:"names"`
}

// PackagedSettings is the part of contests.Settings that is configuration
// rather than a date. EnrollmentDeadline is absent for the same reason
// starts_at is: it is a moment in last year's calendar.
type PackagedSettings struct {
	QueryRateLimitPerMin int `json:"query_rate_limit_per_min"`
	GracePeriodMin       int `json:"grace_period_min"`
}

// PackagedQuestion is one question with its wording, its options and its
// reference answers.
type PackagedQuestion struct {
	// Ord is the display position. Carried explicitly as well as implied by
	// the array's order, so a file somebody reordered by hand still says what
	// was meant.
	Ord         int                             `json:"ord"`
	Kind        string                          `json:"kind"`
	Points      int                             `json:"points"`
	MaxAttempts *int                            `json:"max_attempts,omitempty"`
	PenaltyPct  int                             `json:"penalty_pct"`
	IsVisible   bool                            `json:"is_visible"`
	ChoiceIDs   []string                        `json:"choice_ids"`
	Texts       map[string]PackagedQuestionText `json:"texts"`
	Answers     []PackagedAnswer                `json:"answers"`
}

// PackagedQuestionText is a question as authored in one language.
type PackagedQuestionText struct {
	BodyMD  string            `json:"body_md"`
	Choices map[string]string `json:"choices,omitempty"`
}

// PackagedAnswer is one accepted response. This is the answer key.
type PackagedAnswer struct {
	MatchKind string `json:"match_kind"`
	Value     string `json:"value"`
}

// PackagedSQLPolicy is how much SQL power the contest hands its participants.
type PackagedSQLPolicy struct {
	Mode            string   `json:"mode"`
	WritableTables  []string `json:"writable_tables"`
	AllowCreateView bool     `json:"allow_create_view"`
	AllowOwnTables  bool     `json:"allow_own_tables"`
	AllowTempTables bool     `json:"allow_temp_tables"`
	AllowCatalog    bool     `json:"allow_catalog"`
	DiskQuotaRatio  int      `json:"disk_quota_ratio"`
}

// PackagedGame is the SQL the contest's game database is built from. Absent
// for a contest that has none — a draft whose game has not been written, or
// an installation with no game cluster at all.
type PackagedGame struct {
	// Script is the SQL, and is empty exactly when Omitted is true.
	Script string `json:"script"`
	// Omitted says this contest's game was built from an uploaded dump — up
	// to gigabytes of SQL living on the API host's own volume — which a
	// package assembled whole in memory cannot carry.
	//
	// Present, with an empty script, rather than the whole game object being
	// absent: "this contest has a game you have to move separately" and "this
	// contest has no game" are different facts, and an importer that read the
	// first as the second would rebuild a contest whose every question
	// answers "no such table" with nothing in the file to say so.
	Omitted bool `json:"omitted,omitempty"`
}

// exportPackage serves GET /contests/{id}/export.
//
// Mounted inside ContestsHandler's contest.edit group. That is the whole
// access decision and it is deliberate: the package is a full answer key, so
// it belongs to the permission that already means "may write those answers",
// not to contest.view, which means "may look at this contest". A participant
// holds neither — taking part is a registration, not a contest role — so the
// middleware refuses before this function is reached.
//
// Written whole rather than streamed. A package is only useful complete: a
// stream that fails halfway leaves a file that already claimed 200 and is
// missing questions its answer key still names, with nothing in it to say so.
// The size that makes this safe is asserted rather than assumed —
// contests.MaxPackageQuestions bounds the one list authoring itself leaves
// unbounded, and every other part is bounded where it was written (the game
// script by provisioning.MaxScriptBytes, the options per question, the
// reference answers, the writable tables).
func (h *ContestsHandler) exportPackage(w http.ResponseWriter, r *http.Request) {
	contestID, err := uuid.Parse(chi.URLParam(r, contestIDParam))
	if err != nil {
		httpx.Error(w, r, http.StatusBadRequest, auth.CodeInvalidContestID, "Contest identifier is not valid")
		return
	}
	identity, _ := auth.IdentityFrom(r.Context())

	pkg, err := h.service.ExportPackage(r.Context(), identity.UserID, contestID)
	if err != nil {
		h.fail(w, r, err)
		return
	}

	// A download rather than something a browser renders. `nosniff` is
	// already on every response (httpx.SecureHeaders); the disposition is
	// what makes the browser save it, and the name is built from the
	// identifier rather than the title because a title is authored text in
	// any script and a header is ASCII.
	w.Header().Set("Content-Disposition",
		fmt.Sprintf("attachment; filename=%q", "contest-package-"+contestID.String()+".json"))
	// Indented, because the file is read and edited by people. It costs
	// whitespace on a document already bounded above.
	body := toContestPackage(pkg)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(body); err != nil {
		// The status line is already out, so there is nothing to tell the
		// client that it has not already been told. Logged rather than
		// swallowed: a package that failed to finish is a truncated answer
		// key somebody may be about to import.
		h.log.ErrorContext(r.Context(), "could not write the contest package", "error", err)
	}
}

// toContestPackage maps the domain's package onto the wire.
func toContestPackage(pkg contests.Package) ContestPackage {
	c := pkg.Contest
	out := ContestPackage{
		Format:     packageFormat,
		Version:    packageVersion,
		ExportedAt: time.Now().UTC().Format(timeLayout),
		Contest: PackagedContest{
			Enrollment:   c.Enrollment,
			QuestionMode: c.QuestionMode,
			Progression:  c.Progression,
			Scoring:      c.Scoring,
			Timing:       c.Timing,
			DurationMin:  c.DurationMin,
			AllowedCIDRs: make([]string, 0, len(c.AllowedCIDRs)),
			Settings: PackagedSettings{
				QueryRateLimitPerMin: c.Settings.QueryRateLimitPerMin,
				GracePeriodMin:       c.Settings.GracePeriodMin,
			},
			Leaderboard: PackagedLeaderboard{
				FreezeMin: c.LeaderboardFreezeMin,
				Names:     c.LeaderboardNames,
			},
			DefaultLanguage: c.DefaultLanguage(),
			Languages:       c.LanguageCodes(),
			Translations:    make(map[string]TranslationResponse, len(c.Translations)),
		},
		// Never nil on the wire: a reader that has to tell `null` from `{}`
		// before it can decide whether a contest has a story is a reader with
		// a bug waiting, the same rule the query result and the schema tree
		// already follow.
		Story:     make(map[string]string, len(pkg.Story.Bodies)),
		Questions: make([]PackagedQuestion, 0, len(pkg.Questions)),
		SQLPolicy: PackagedSQLPolicy{
			Mode:            pkg.Policy.Mode,
			WritableTables:  emptyIfNil(pkg.Policy.WritableTables),
			AllowCreateView: pkg.Policy.AllowCreateView,
			AllowOwnTables:  pkg.Policy.AllowOwnTables,
			AllowTempTables: pkg.Policy.AllowTempTables,
			AllowCatalog:    pkg.Policy.AllowCatalog,
			DiskQuotaRatio:  pkg.Policy.DiskQuotaRatio,
		},
	}

	for _, prefix := range c.AllowedCIDRs {
		out.Contest.AllowedCIDRs = append(out.Contest.AllowedCIDRs, prefix.String())
	}
	for lang, t := range c.Translations {
		out.Contest.Translations[lang] = TranslationResponse{Title: t.Title, Description: t.Description}
	}
	for lang, body := range pkg.Story.Bodies {
		out.Story[lang] = body
	}

	for _, q := range pkg.Questions {
		packaged := PackagedQuestion{
			Ord:         q.Ord,
			Kind:        q.Kind,
			Points:      q.Points,
			MaxAttempts: q.MaxAttempts,
			PenaltyPct:  q.PenaltyPct,
			IsVisible:   q.IsVisible,
			ChoiceIDs:   emptyIfNil(q.ChoiceIDs),
			Texts:       make(map[string]PackagedQuestionText, len(q.Texts)),
			Answers:     make([]PackagedAnswer, 0, len(q.Answers)),
		}
		for lang, text := range q.Texts {
			packaged.Texts[lang] = PackagedQuestionText{BodyMD: text.BodyMD, Choices: text.Choices}
		}
		for _, a := range q.Answers {
			packaged.Answers = append(packaged.Answers, PackagedAnswer{MatchKind: a.MatchKind, Value: a.Value})
		}
		out.Questions = append(out.Questions, packaged)
	}

	if pkg.HasGame {
		out.Game = &PackagedGame{Script: pkg.Game, Omitted: pkg.GameOmitted}
	}
	return out
}

// emptyIfNil keeps a nil slice out of the document, for the same reason the
// maps above are always allocated.
func emptyIfNil(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}
