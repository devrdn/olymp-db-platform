package userstest

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"slices"
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

	unlinkedOf := func(t *testing.T, ctx context.Context, target IdentityTarget, userID uuid.UUID) []users.UnlinkedIdentity {
		t.Helper()
		unlinked, err := target.Store.UnlinkedIdentitiesOf(ctx, userID)
		if err != nil {
			t.Fatalf("UnlinkedIdentitiesOf() = %v", err)
		}
		return unlinked
	}
	record := func(t *testing.T, ctx context.Context, target IdentityTarget, id users.ExternalIdentity) users.UnlinkedIdentity {
		t.Helper()
		recorded, err := target.Store.RecordUnlinked(ctx, id)
		if err != nil {
			t.Fatalf("RecordUnlinked(%s) = %v", id.Subject, err)
		}
		return recorded
	}
	isUnlinked := func(t *testing.T, ctx context.Context, target IdentityTarget, id users.ExternalIdentity) bool {
		t.Helper()
		unlinked, err := target.Store.IsUnlinked(ctx, id.UserID, id.Provider, id.Issuer, id.Subject)
		if err != nil {
			t.Fatalf("IsUnlinked() = %v", err)
		}
		return unlinked
	}

	t.Run("RecordUnlinked keeps that external account from that account only", func(t *testing.T) {
		each(t, func(ctx context.Context, target IdentityTarget) {
			ana, other := target.NewUser(), target.NewUser()
			gone := users.ExternalIdentity{UserID: ana, Provider: users.ProviderMicrosoft, Issuer: "https://login.example/a/v2.0", Subject: "sub-gone", Email: "ana@example.edu"}
			recorded := record(t, ctx, target, gone)

			unlinked := unlinkedOf(t, ctx, target, ana)

			if len(unlinked) != 1 || unlinked[0].ExternalIdentity != gone || unlinked[0].ID == uuid.Nil || unlinked[0].UnlinkedAt.IsZero() {
				t.Fatalf("unlinked = %+v, want %+v with an id and a time", unlinked, gone)
			}
			if recorded.ID != unlinked[0].ID || recorded.ExternalIdentity != gone || !recorded.UnlinkedAt.Equal(unlinked[0].UnlinkedAt) {
				t.Errorf("RecordUnlinked() returned %+v, want %+v", recorded, unlinked[0])
			}
			if !isUnlinked(t, ctx, target, gone) {
				t.Error("IsUnlinked() = false for the recorded identity")
			}
			rightful := gone
			rightful.Subject = "sub-rightful"
			elsewhere := gone
			elsewhere.UserID = other
			for name, id := range map[string]users.ExternalIdentity{"another subject": rightful, "another account": elsewhere} {
				if isUnlinked(t, ctx, target, id) {
					t.Errorf("IsUnlinked(%s) = true, want only the recorded identity unlinked from that account", name)
				}
			}
			if unlinked := unlinkedOf(t, ctx, target, other); len(unlinked) != 0 {
				t.Errorf("the other account's unlinked = %+v, want none", unlinked)
			}
		})
	})

	t.Run("RecordUnlinked takes an identity of every provider", func(t *testing.T) {
		each(t, func(ctx context.Context, target IdentityTarget) {
			ana := target.NewUser()
			for _, provider := range users.Providers {
				id := users.ExternalIdentity{UserID: ana, Provider: provider, Issuer: "https://" + provider + ".example", Subject: "sub-" + provider}
				record(t, ctx, target, id)
				if !isUnlinked(t, ctx, target, id) {
					t.Errorf("IsUnlinked(%s) = false after RecordUnlinked", provider)
				}
			}
		})
	})

	t.Run("RecordUnlinked of an identity already recorded keeps one record", func(t *testing.T) {
		each(t, func(ctx context.Context, target IdentityTarget) {
			ana := target.NewUser()
			gone := users.ExternalIdentity{UserID: ana, Provider: users.ProviderGitHub, Issuer: "https://github.example", Subject: "4242"}
			record(t, ctx, target, gone)
			first := unlinkedOf(t, ctx, target, ana)[0]

			if again := record(t, ctx, target, gone); again.ID != first.ID {
				t.Errorf("the second RecordUnlinked() returned %s, want the record's id %s", again.ID, first.ID)
			}
			if unlinked := unlinkedOf(t, ctx, target, ana); len(unlinked) != 1 || unlinked[0].ID != first.ID || unlinked[0].UnlinkedAt.Before(first.UnlinkedAt) {
				t.Errorf("unlinked = %+v, want the one record %s, no older", unlinked, first.ID)
			}
		})
	})

	t.Run("UnlinkedIdentitiesOf lists at most MaxIdentitiesListed, newest first", func(t *testing.T) {
		each(t, func(ctx context.Context, target IdentityTarget) {
			ana := target.NewUser()
			for range users.MaxIdentitiesListed + 1 {
				record(t, ctx, target, users.ExternalIdentity{
					UserID: ana, Provider: users.ProviderMicrosoft, Issuer: "https://login.example/" + uuid.NewString() + "/v2.0", Subject: "sub-ana",
				})
			}

			unlinked := unlinkedOf(t, ctx, target, ana)

			if len(unlinked) != users.MaxIdentitiesListed {
				t.Errorf("%d unlinked identities listed, want %d", len(unlinked), users.MaxIdentitiesListed)
			}
			// PostgreSQL stamps every record with the transaction's start, so
			// there the identifier decides.
			newestFirst := slices.IsSortedFunc(unlinked, func(a, b users.UnlinkedIdentity) int {
				return cmp.Or(b.UnlinkedAt.Compare(a.UnlinkedAt), bytes.Compare(b.ID[:], a.ID[:]))
			})
			if !newestFirst {
				t.Errorf("unlinked = %+v, want the newest first, then by id descending", unlinked)
			}
		})
	})

	t.Run("AllowUnlinked lifts the record it names, of its own account only", func(t *testing.T) {
		each(t, func(ctx context.Context, target IdentityTarget) {
			ana, other := target.NewUser(), target.NewUser()
			gone := users.ExternalIdentity{UserID: ana, Provider: users.ProviderGoogle, Issuer: "https://accounts.example", Subject: "sub-gone"}
			record(t, ctx, target, gone)
			unlinked := unlinkedOf(t, ctx, target, ana)[0]

			if _, err := target.Store.AllowUnlinked(ctx, other, unlinked.ID); !errors.Is(err, users.ErrUnlinkedIdentityNotFound) {
				t.Errorf("AllowUnlinked(another account's) = %v, want ErrUnlinkedIdentityNotFound", err)
			}
			lifted, err := target.Store.AllowUnlinked(ctx, ana, unlinked.ID)
			if err != nil {
				t.Fatalf("AllowUnlinked() = %v", err)
			}

			if lifted.ID != unlinked.ID || lifted.ExternalIdentity != gone {
				t.Errorf("AllowUnlinked() returned %+v, want %+v", lifted, unlinked)
			}
			if isUnlinked(t, ctx, target, gone) {
				t.Error("IsUnlinked() = true after the record was lifted")
			}
			if _, err := target.Store.AllowUnlinked(ctx, ana, unlinked.ID); !errors.Is(err, users.ErrUnlinkedIdentityNotFound) {
				t.Errorf("a second AllowUnlinked() = %v, want ErrUnlinkedIdentityNotFound", err)
			}
		})
	})
}
