package contests

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/google/uuid"
)

// ErrStoryNotFound reports that the contest has no story yet.
var ErrStoryNotFound = errors.New("story not found")

// Story is the crime the participants investigate.
//
// A contest has at most one. The text is per language: the game database is
// English, but the narrative around it is what a participant actually reads.
type Story struct {
	ID        uuid.UUID
	ContestID uuid.UUID
	// Bodies hold the markdown per language code.
	Bodies    map[string]string
	UpdatedAt time.Time
}

// Body returns the story in the given language, and whether it exists.
func (s Story) Body(lang string) (string, bool) {
	body, ok := s.Bodies[lang]
	return body, ok
}

// StoryRepository stores the crime story of a contest.
type StoryRepository interface {
	// ByContest returns the contest's story, or ErrStoryNotFound.
	ByContest(ctx context.Context, contestID uuid.UUID) (Story, error)
	// Save creates or replaces the story, setting its text to exactly these
	// languages.
	Save(ctx context.Context, contestID uuid.UUID, bodies map[string]string) (Story, error)
	// Delete removes the story.
	Delete(ctx context.Context, contestID uuid.UUID) error
}

// Story returns the contest's story, or ErrStoryNotFound.
func (s *Service) Story(ctx context.Context, contestID uuid.UUID) (Story, error) {
	return s.stories.ByContest(ctx, contestID)
}

// SetStory writes the crime story in every language it was authored in.
//
// The whole set is replaced rather than patched one language at a time: it is
// the set the publish gate reasons about, and a half-applied one is exactly
// what it would then have to guess about.
func (s *Service) SetStory(ctx context.Context, actorID, contestID uuid.UUID, bodies map[string]string) (Story, error) {
	c, err := s.contests.ByID(ctx, contestID)
	if err != nil {
		return Story{}, err
	}
	// Participants are reading it; changing it underneath them changes the
	// task they are working on.
	if !c.ContentEditable() {
		return Story{}, fmt.Errorf("%w: it is %s", ErrNotEditable, c.Status)
	}
	if err := s.checkLanguageCodes(ctx, langCodes(bodies)); err != nil {
		return Story{}, err
	}

	var saved Story
	err = s.uow.Do(ctx, func(ctx context.Context) error {
		var err error
		if saved, err = s.stories.Save(ctx, contestID, bodies); err != nil {
			return err
		}
		return s.record(ctx, actorID, audit.ActionContestStoryChange, contestID, map[string]any{
			"languages": langCodes(bodies),
		})
	})
	if err != nil {
		return Story{}, err
	}
	return saved, nil
}
