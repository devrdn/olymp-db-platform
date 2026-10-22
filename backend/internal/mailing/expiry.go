package mailing

import (
	"context"
	"log/slog"
	"time"
)

// Expirer gives up the messages queued longer than maxAge ago that no sender
// holds (none claimed within lease), erasing their bodies, and counts them by
// kind.
type Expirer interface {
	Expire(ctx context.Context, maxAge, lease time.Duration, reason string) (map[Kind]int, error)
}

// Sweeper expires messages past MaxAge on an installation with mail off,
// where no dispatcher runs, so a one-time password queued before mail was
// turned off does not stay in the table.
type Sweeper struct {
	store  Expirer
	logger *slog.Logger
}

// NewSweeper sweeps store and logs what it abandons to logger.
func NewSweeper(store Expirer, logger *slog.Logger) *Sweeper {
	return &Sweeper{store: store, logger: logger}
}

// Sweep runs one expiry.
func (s *Sweeper) Sweep(ctx context.Context) error {
	return expire(ctx, s.store, nil, s.logger)
}

// expire abandons the messages past MaxAge; counters may be nil.
func expire(ctx context.Context, store Expirer, counters Counters, logger *slog.Logger) error {
	expired, err := store.Expire(ctx, MaxAge, ClaimLease, "abandoned unsent: queued more than "+MaxAge.String()+" ago")
	if err != nil {
		return err
	}
	for kind, n := range expired {
		if counters != nil {
			for range n {
				counters.Count(string(kind), OutcomeAbandoned)
			}
		}
		logger.WarnContext(ctx, "messages queued too long ago were abandoned and will not be sent",
			"kind", kind, "messages", n, "max_age", MaxAge.String())
	}
	return nil
}
