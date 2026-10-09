package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/devrdn/db-contest/backend/internal/auth"
	"github.com/devrdn/db-contest/backend/internal/contests"
)

// The contest package: one olympiad as a file an organiser can edit and, once
// import is built, load back (docs/ARCHITECTURE.md §15 item 12).
//
// It carries the reference answers. That decides how it is served: contest.edit
// rather than contest.view, an audit entry, and no route a participant can
// reach.
const (
	// packageFormat identifies the file, so an importer can refuse anything
	// else.
	packageFormat = "dbcontest.contest-package"
	// packageVersion is this shape's revision; an importer refuses a future
	// version rather than half-reading it.
	packageVersion = 1
)

// ContestPackage is one contest as a file. A wire type of its own, so a field
// added to the domain is not published automatically alongside the answer key.
//
// It does not carry:
//   - identifiers: an importing installation mints its own;
//   - the status, window or enrollment deadline: facts about one run, not the
//     contest;
//   - the roster, registrations, submissions or query log: this is not a
//     backup.
type ContestPackage struct {
	Format     string             `json:"format"`
	Version    int                `json:"version"`
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
	// individual timing: a setting, unlike the window.
	DurationMin *int `json:"duration_min,omitempty"`
	// AllowedCIDRs is the network restriction, configuration worth carrying
	// over.
	AllowedCIDRs []string         `json:"allowed_cidrs"`
	Settings     PackagedSettings `json:"settings"`
	// ICPCPenaltyMin is carried whether or not the contest uses ICPC scoring.
	ICPCPenaltyMin int `json:"icpc_penalty_min"`
	// Leaderboard is the freeze and labelling configuration; when results were
	// revealed is about one run and stays behind.
	Leaderboard PackagedLeaderboard `json:"leaderboard"`
	// DefaultLanguage is named separately, as well as flagged in Languages,
	// because it must survive a round trip intact.
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

// PackagedSettings is the part of contests.Settings that is configuration.
// EnrollmentDeadline is left out: it is a date of one run, not configuration.
type PackagedSettings struct {
	QueryRateLimitPerMin int `json:"query_rate_limit_per_min"`
	GracePeriodMin       int `json:"grace_period_min"`
}

// PackagedQuestion is one question with its wording, its options and its
// reference answers.
type PackagedQuestion struct {
	// Ord is carried as well as implied by order, so a file reordered by hand
	// still says what was meant.
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

// PackagedGame is the SQL the game database is built from; absent for a contest
// with no game.
type PackagedGame struct {
	// Script is empty exactly when Omitted is true.
	Script string `json:"script"`
	// Omitted says the game was built from an uploaded dump, possibly gigabytes
	// on the API host's volume, which a package built in memory cannot carry.
	// Sent as a present game with an empty script, so an importer can tell
	// "move the game separately" from "no game".
	Omitted bool `json:"omitted,omitempty"`
}

// exportPackage serves GET /contests/{id}/export. Mounted in the contest.edit
// group because the package is a full answer key; a participant holds no
// contest role and never reaches it.
//
// Written whole, not streamed: a stream failing halfway would leave a file that
// claimed 200 and silently lacks questions. That is safe because every part is
// bounded where it is written (contests.MaxPackageQuestions,
// provisioning.MaxScriptBytes, and the per-question limits).
func (h *ContestsHandler) exportPackage(w http.ResponseWriter, r *http.Request) {
	contestID, ok := contestIDFrom(w, r)
	if !ok {
		return
	}
	identity, _ := auth.IdentityFrom(r.Context())

	pkg, err := h.service.ExportPackage(r.Context(), identity.UserID, contestID)
	if err != nil {
		h.fail(w, r, err)
		return
	}

	// A download, not something to render (nosniff is on every response). The
	// name uses the identifier because a title can be any script and a header
	// is ASCII.
	w.Header().Set("Content-Disposition",
		fmt.Sprintf("attachment; filename=%q", "contest-package-"+contestID.String()+".json"))
	// Indented, because people read and edit the file.
	body := toContestPackage(pkg)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(body); err != nil {
		// The 200 is already out. Logged, because a truncated answer key may be
		// about to be imported.
		h.log.ErrorContext(r.Context(), "could not write the contest package", "error", err)
	}
}

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
			ICPCPenaltyMin: c.ICPCPenaltyMin,
			Leaderboard: PackagedLeaderboard{
				FreezeMin: c.LeaderboardFreezeMin,
				Names:     c.LeaderboardNames,
			},
			DefaultLanguage: c.DefaultLanguage(),
			Languages:       c.LanguageCodes(),
			Translations:    make(map[string]TranslationResponse, len(c.Translations)),
		},
		// Never nil, so a reader never has to tell null from {}.
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

// emptyIfNil keeps a nil slice out of a response, so it encodes as [] and not
// null.
func emptyIfNil[T any](in []T) []T {
	if in == nil {
		return []T{}
	}
	return in
}
