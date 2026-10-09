package settings

import (
	"context"
	"fmt"
	"slices"

	"github.com/devrdn/db-contest/backend/internal/audit"
	"github.com/devrdn/db-contest/backend/internal/platform/storage"
	"github.com/google/uuid"
)

type Service struct {
	repo   Repository
	images ImageRepository
	audit  *audit.Recorder
	uow    storage.UnitOfWork
}

func NewService(repo Repository, images ImageRepository, recorder *audit.Recorder, uow storage.UnitOfWork) *Service {
	return &Service{repo: repo, images: images, audit: recorder, uow: uow}
}

// SaveImage stores a picture in one of the installation's slots. Only the
// bytes are believed (see inspect).
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
// own mark.
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

func (s *Service) Image(ctx context.Context, kind string) (Image, error) {
	if !slices.Contains(ImageKinds, kind) {
		return Image{}, fmt.Errorf("%w: %q", ErrUnknownImageKind, kind)
	}
	return s.images.ByKind(ctx, kind)
}

// All returns every setting in the catalogue, with the fallback where nothing
// has been saved. Rows outside the catalogue are ignored.
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

// Public returns the settings a page with no session may read
// (Definition.Public).
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

// Save replaces the given settings. Everything is checked before anything is
// written, and the write shares one transaction with its audit entry, so a
// refused save changes nothing.
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
