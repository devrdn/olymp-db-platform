package settings

import (
	"context"
	"fmt"
	"slices"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/google/uuid"
)

// Service holds the rules of the installation's own settings.
type Service struct {
	repo   Repository
	images ImageRepository
	audit  *audit.Recorder
	uow    storage.UnitOfWork
}

// NewService assembles the settings service.
func NewService(repo Repository, images ImageRepository, recorder *audit.Recorder, uow storage.UnitOfWork) *Service {
	return &Service{repo: repo, images: images, audit: recorder, uow: uow}
}

// SaveImage stores a picture in one of the installation's slots.
//
// What arrives is bytes and a slot, and nothing else is believed: the declared
// content type and the filename are written by whoever is uploading, so the
// answer comes from sniffing and then decoding the bytes themselves. See
// `inspect`, which is where the rules are.
func (s *Service) SaveImage(ctx context.Context, actorID uuid.UUID, kind string, data []byte) (Image, error) {
	img, err := inspect(kind, data)
	if err != nil {
		return Image{}, err
	}

	err = s.uow.Do(ctx, func(ctx context.Context) error {
		if err := s.images.Save(ctx, actorID, img); err != nil {
			return fmt.Errorf("save image: %w", err)
		}
		return s.record(ctx, actorID, map[string]any{
			"image":  kind,
			"sha256": img.SHA256,
			"size":   fmt.Sprintf("%dx%d", img.Width, img.Height),
		})
	})
	if err != nil {
		return Image{}, err
	}
	return img, nil
}

// RemoveImage empties a slot, so the installation falls back to the product's
// own mark rather than keeping a picture nobody wants.
func (s *Service) RemoveImage(ctx context.Context, actorID uuid.UUID, kind string) error {
	if !slices.Contains(ImageKinds, kind) {
		return fmt.Errorf("%w: %q", ErrUnknownImageKind, kind)
	}

	return s.uow.Do(ctx, func(ctx context.Context) error {
		if err := s.images.Delete(ctx, kind); err != nil {
			return fmt.Errorf("remove image: %w", err)
		}
		return s.record(ctx, actorID, map[string]any{"image": kind, "removed": true})
	})
}

// Images reports which slots hold a picture, and the hash its URL carries.
func (s *Service) Images(ctx context.Context) (map[string]string, error) {
	present, err := s.images.Present(ctx)
	if err != nil {
		return nil, fmt.Errorf("read images: %w", err)
	}
	return present, nil
}

// Image returns one stored picture, for serving it.
func (s *Service) Image(ctx context.Context, kind string) (Image, error) {
	if !slices.Contains(ImageKinds, kind) {
		return Image{}, fmt.Errorf("%w: %q", ErrUnknownImageKind, kind)
	}
	return s.images.ByKind(ctx, kind)
}

// All returns every setting the product knows about, with the fallback where
// nothing has been saved.
//
// The catalogue decides what comes back, never the table: a row nothing reads
// is not configuration, and it does not become configuration by existing.
func (s *Service) All(ctx context.Context) (Values, error) {
	stored, err := s.repo.All(ctx)
	if err != nil {
		return nil, fmt.Errorf("read settings: %w", err)
	}

	out := make(Values, len(Catalogue))
	for _, d := range Catalogue {
		if value, ok := stored[d.Key]; ok {
			out[d.Key] = value
			continue
		}
		out[d.Key] = d.Fallback
	}
	return out, nil
}

// Public returns the settings a page with no session may read.
//
// An allow-list, and it has to be. The sign-in screen carries the
// installation's name and logo and is seen before anybody signs in, so some of
// this is necessarily readable by anyone — which makes "return the table"
// exactly the wrong shape. The day somebody adds a mail server's password
// here, an endpoint that returned everything would publish it, and nothing in
// that change would look like a disclosure.
func (s *Service) Public(ctx context.Context) (Values, error) {
	all, err := s.All(ctx)
	if err != nil {
		return nil, err
	}

	out := Values{}
	for _, d := range Catalogue {
		if d.Public {
			out[d.Key] = all[d.Key]
		}
	}
	return out, nil
}

// Save replaces the given settings.
//
// Everything is checked before anything is written, and the write shares one
// transaction with its audit entry: an administrator told "no" should not have
// to work out which half of their change went through.
func (s *Service) Save(ctx context.Context, actorID uuid.UUID, values Values) error {
	known := Definitions()

	for key, value := range values {
		definition, ok := known[key]
		if !ok {
			return fmt.Errorf("%w: %q", ErrUnknownKey, key)
		}
		if err := definition.Validate(value); err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
	}

	current, err := s.All(ctx)
	if err != nil {
		return err
	}

	// What moved, and what it was. Changing what the whole installation is
	// called is exactly the kind of thing somebody asks about afterwards
	// (section 9.2), and the change set is the same shape every other
	// administrative edit records.
	changes := audit.NewChanges()
	for key, value := range values {
		changes.Set(key, current[key], value)
	}

	return s.uow.Do(ctx, func(ctx context.Context) error {
		if err := s.repo.Save(ctx, actorID, values); err != nil {
			return fmt.Errorf("save settings: %w", err)
		}
		return s.record(ctx, actorID, changes.Payload())
	})
}

func (s *Service) record(ctx context.Context, actorID uuid.UUID, payload map[string]any) error {
	var actor *uuid.UUID
	if actorID != uuid.Nil {
		actor = &actorID
	}
	return s.audit.Record(ctx, audit.Entry{
		ActorID: actor,
		Action:  audit.ActionSettingsChange,
		Entity:  "settings",
		Payload: payload,
	})
}
