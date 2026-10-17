package provisioning

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// ReadyTemplate is a template the core database records as built, with its
// contest's status, which decides what bringing the game back takes.
type ReadyTemplate struct {
	ContestID     uuid.UUID
	ContestStatus string
	Database      string
}

// ReadyTemplateStore lists every template the core database records as
// 'ready'.
type ReadyTemplateStore interface {
	ReadyTemplates(ctx context.Context) ([]ReadyTemplate, error)
}

// TemplatePresence is the game cluster as the check sees it: it can only look.
type TemplatePresence interface {
	// MissingDatabases returns the names that do not exist on the cluster,
	// in the order given.
	MissingDatabases(ctx context.Context, names []string) ([]string, error)
}

// TemplateCheck finds the templates the core database calls ready that are
// not on the game cluster: after a restore onto a new host every row still
// says 'ready', and a contest whose template is gone has no game.
//
// It changes nothing. It is run by hand from cmd/gamecheck.
type TemplateCheck struct {
	store   ReadyTemplateStore
	cluster TemplatePresence
}

// NewTemplateCheck assembles the check.
func NewTemplateCheck(store ReadyTemplateStore, cluster TemplatePresence) *TemplateCheck {
	return &TemplateCheck{store: store, cluster: cluster}
}

// Missing returns the ready templates whose database the cluster lacks, in
// the store's order.
func (c *TemplateCheck) Missing(ctx context.Context) ([]ReadyTemplate, error) {
	ready, err := c.store.ReadyTemplates(ctx)
	if err != nil {
		return nil, fmt.Errorf("read the ready templates: %w", err)
	}
	if len(ready) == 0 {
		return nil, nil
	}

	names := make([]string, len(ready))
	for i, template := range ready {
		names[i] = template.Database
	}
	absent, err := c.cluster.MissingDatabases(ctx, names)
	if err != nil {
		return nil, fmt.Errorf("look the templates up on the game cluster: %w", err)
	}

	gone := make(map[string]bool, len(absent))
	for _, name := range absent {
		gone[name] = true
	}
	var missing []ReadyTemplate
	for _, template := range ready {
		if gone[template.Database] {
			missing = append(missing, template)
		}
	}
	return missing, nil
}
