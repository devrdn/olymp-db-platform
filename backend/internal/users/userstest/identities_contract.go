package userstest

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/devrdn/olymp-db-platform/backend/internal/users"
	"github.com/google/uuid"
)

// IdentityTarget is a store holding no identities yet, and the means to
// create the accounts an identity is linked to.
type IdentityTarget struct {
	Store   users.ExternalIdentities
	NewUser func() uuid.UUID
}

// ExternalIdentitiesContract is what every users.ExternalIdentities must do;
// both the in-memory Repository and postgres.Users run it. each prepares a
// fresh target for one case, calls run with it, and cleans up.
func ExternalIdentitiesContract(t *testing.T, each func(t *testing.T, run func(context.Context, IdentityTarget))) {
	link := func(t *testing.T, ctx context.Context, target IdentityTarget, id users.ExternalIdentity) {
		t.Helper()
		if err := target.Store.LinkExternalIdentity(ctx, id); err != nil {
			t.Fatalf("LinkExternalIdentity(%s) = %v", id.Subject, err)
		}
	}
	identitiesOf := func(t *testing.T, ctx context.Context, target IdentityTarget, userID uuid.UUID) []users.LinkedIdentity {
		t.Helper()
		linked, err := target.Store.IdentitiesOf(ctx, userID)
		if err != nil {
			t.Fatalf("IdentitiesOf() = %v", err)
		}
		return linked
	}

	t.Run("IdentitiesOf lists the account's identities in the order they were linked", func(t *testing.T) {
		each(t, func(ctx context.Context, target IdentityTarget) {
			ana, other := target.NewUser(), target.NewUser()
			first := users.ExternalIdentity{UserID: ana, Provider: users.ProviderMicrosoft, Issuer: "https://login.example/a/v2.0", Subject: "sub-a", Email: "ana@example.edu"}
			second := users.ExternalIdentity{UserID: ana, Provider: users.ProviderGoogle, Issuer: "https://accounts.example", Subject: "sub-b"}
			link(t, ctx, target, first)
			link(t, ctx, target, second)
			link(t, ctx, target, users.ExternalIdentity{UserID: other, Provider: users.ProviderGitHub, Issuer: "https://github.example", Subject: "sub-c"})

			linked := identitiesOf(t, ctx, target, ana)

			if len(linked) != 2 {
				t.Fatalf("identities = %+v, want ana's two", linked)
			}
			// PostgreSQL stamps both links with the transaction's start, so
			// there the identifier decides between them.
			if a, b := linked[0], linked[1]; a.LinkedAt.After(b.LinkedAt) ||
				a.LinkedAt.Equal(b.LinkedAt) && bytes.Compare(a.ID[:], b.ID[:]) > 0 {
				t.Errorf("identities = %+v, want them ordered by when they were linked, then by id", linked)
			}
			byIssuer := map[string]users.LinkedIdentity{}
			for _, l := range linked {
				byIssuer[l.Issuer] = l
			}
			for _, want := range []users.ExternalIdentity{first, second} {
				got, ok := byIssuer[want.Issuer]
				if !ok || got.ExternalIdentity != want {
					t.Errorf("identity at %s = %+v, want %+v", want.Issuer, got.ExternalIdentity, want)
				}
				if got.ID == uuid.Nil || got.LinkedAt.IsZero() || got.LastUsedAt.Before(got.LinkedAt) {
					t.Errorf("identity at %s has id %s, linked %v, last used %v; want an id and a last use no earlier than the link",
						want.Issuer, got.ID, got.LinkedAt, got.LastUsedAt)
				}
			}
		})
	})

	t.Run("IdentitiesOf lists at most MaxIdentitiesListed", func(t *testing.T) {
		each(t, func(ctx context.Context, target IdentityTarget) {
			ana := target.NewUser()
			for range users.MaxIdentitiesListed + 1 {
				link(t, ctx, target, users.ExternalIdentity{
					UserID: ana, Provider: users.ProviderMicrosoft, Issuer: "https://login.example/" + uuid.NewString() + "/v2.0", Subject: "sub-ana",
				})
			}

			if linked := identitiesOf(t, ctx, target, ana); len(linked) != users.MaxIdentitiesListed {
				t.Errorf("%d identities listed, want %d", len(linked), users.MaxIdentitiesListed)
			}
		})
	})

	t.Run("IdentitiesOf an account with none is empty", func(t *testing.T) {
		each(t, func(ctx context.Context, target IdentityTarget) {
			if linked := identitiesOf(t, ctx, target, target.NewUser()); len(linked) != 0 {
				t.Errorf("identities = %+v, want none", linked)
			}
		})
	})

	t.Run("UnlinkExternalIdentity removes only the identity it names", func(t *testing.T) {
		each(t, func(ctx context.Context, target IdentityTarget) {
			ana := target.NewUser()
			link(t, ctx, target, users.ExternalIdentity{UserID: ana, Provider: users.ProviderMicrosoft, Issuer: "https://login.example/a/v2.0", Subject: "sub-gone"})
			link(t, ctx, target, users.ExternalIdentity{UserID: ana, Provider: users.ProviderGoogle, Issuer: "https://accounts.example", Subject: "sub-kept"})
			before := identitiesOf(t, ctx, target, ana)
			gone := before[0]

			unlinked, err := target.Store.UnlinkExternalIdentity(ctx, ana, gone.ID)
			if err != nil {
				t.Fatalf("UnlinkExternalIdentity() = %v", err)
			}

			if unlinked.ID != gone.ID || unlinked.ExternalIdentity != gone.ExternalIdentity || !unlinked.LinkedAt.Equal(gone.LinkedAt) {
				t.Errorf("UnlinkExternalIdentity() returned %+v, want %+v", unlinked, gone)
			}
			if after := identitiesOf(t, ctx, target, ana); len(after) != 1 || after[0].ID != before[1].ID {
				t.Errorf("identities = %+v, want only %s", after, before[1].ID)
			}
			if _, err := target.Store.ByExternalIdentity(ctx, gone.Provider, gone.Issuer, gone.Subject); !errors.Is(err, users.ErrNotFound) {
				t.Errorf("ByExternalIdentity(unlinked) = %v, want ErrNotFound", err)
			}
		})
	})

	t.Run("UnlinkExternalIdentity refuses an identity of another account and one already unlinked", func(t *testing.T) {
		each(t, func(ctx context.Context, target IdentityTarget) {
			ana, other := target.NewUser(), target.NewUser()
			link(t, ctx, target, users.ExternalIdentity{UserID: ana, Provider: users.ProviderMicrosoft, Issuer: "https://login.example/a/v2.0", Subject: "sub-ana"})
			held := identitiesOf(t, ctx, target, ana)[0]

			if _, err := target.Store.UnlinkExternalIdentity(ctx, other, held.ID); !errors.Is(err, users.ErrIdentityNotFound) {
				t.Errorf("UnlinkExternalIdentity(another account's) = %v, want ErrIdentityNotFound", err)
			}
			if linked := identitiesOf(t, ctx, target, ana); len(linked) != 1 {
				t.Errorf("identities = %+v, want ana's still linked", linked)
			}
			if _, err := target.Store.UnlinkExternalIdentity(ctx, ana, held.ID); err != nil {
				t.Fatalf("UnlinkExternalIdentity() = %v", err)
			}
			if _, err := target.Store.UnlinkExternalIdentity(ctx, ana, held.ID); !errors.Is(err, users.ErrIdentityNotFound) {
				t.Errorf("a second UnlinkExternalIdentity() = %v, want ErrIdentityNotFound", err)
			}
		})
	})

	t.Run("an unlinked identity can be linked again", func(t *testing.T) {
		each(t, func(ctx context.Context, target IdentityTarget) {
			ana := target.NewUser()
			identity := users.ExternalIdentity{UserID: ana, Provider: users.ProviderMicrosoft, Issuer: "https://login.example/a/v2.0", Subject: "sub-ana"}
			link(t, ctx, target, identity)
			if _, err := target.Store.UnlinkExternalIdentity(ctx, ana, identitiesOf(t, ctx, target, ana)[0].ID); err != nil {
				t.Fatalf("UnlinkExternalIdentity() = %v", err)
			}

			link(t, ctx, target, identity)

			if found, err := target.Store.ByExternalIdentity(ctx, identity.Provider, identity.Issuer, identity.Subject); err != nil || found.ID != ana {
				t.Errorf("ByExternalIdentity() = %v, %v; want the account linked again", found.ID, err)
			}
		})
	})
}
