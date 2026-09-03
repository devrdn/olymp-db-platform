# Удаление аккаунтов и массовые операции — план реализации

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** администратор может мягко удалить аккаунт с причиной и применить одно действие (статус, роли, сброс паролей) сразу к выбранной группе аккаунтов.

**Architecture:** удаление — третий статус `deleted`, потому что вход и middleware уже упираются в `IsActive()`. Массовая операция идёт в две фазы: чистый разбор выбора в памяти после одного `ByIDs`, затем применение выживших одной транзакцией множественными запросами. Поштучные `Block`/`Unblock`/`Delete`/`Restore` — обёртки над той же машиной с выбором из одного элемента, где единственный пропуск превращается в сентинел.

**Tech Stack:** Go 1.23, pgx/v5, chi, PostgreSQL 16, golang-migrate; Next.js 16 App Router, React 19, Tailwind v4.

**Спецификация:** `docs/superpowers/specs/2026-09-03-account-deletion-and-bulk-editing-design.md`

## Global Constraints

- `CLAUDE.md` правило 1: каждая ошибка, доходящая до HTTP, — объявленный сентинел с отображением в `fail` и тестом обработчика на конкретный 4xx.
- `CLAUDE.md` правило 2: у каждого поля и каждого списка явная граница в домене. Причина — `MaxStatusReasonLength = 500`, выбор — `MaxBulkAccounts = 500`.
- `CLAUDE.md` правило 7: фильтр, который предлагает API, приходит с индексом в том же изменении.
- `CLAUDE.md` правило 8: частичный успех классифицирует ошибки явно — в `Skipped` попадают только строковые причины из конечного словаря; всё прочее прерывает операцию.
- `CLAUDE.md` правило 5 (Go layout): `foo.go` тестируется `foo_test.go`.
- Фронтенд: клиентские компоненты импортируют константы из `lib/api/*-terms.ts`, никогда из модулей со схемами — `z.object()` на верхнем уровне затягивает zod в клиентский бандл.
- Разделители `---` в тексте не используются.

## File Structure

**Создаются**

- `backend/migrations/000015_account_deletion.up.sql` / `.down.sql` — статус `deleted`, колонки причины, частичные уникальные индексы, индекс по статусу.
- `backend/internal/users/bulk.go` — `MaxBulkAccounts`, словарь причин пропуска, `BulkResult`, разбор выбора, `adminBudget`, три массовых метода.
- `backend/internal/users/bulk_test.go` — доменные тесты массовых операций.
- `backend/internal/api/users_bulk_handler.go` — три массовые ручки и их DTO.
- `backend/internal/api/users_bulk_handler_test.go`.
- `frontend/app/(admin)/users/selection.tsx` — внешнее хранилище выбора, флажок строки, панель действий.
- `frontend/app/(admin)/users/selection.test.tsx`.
- `frontend/app/(admin)/users/bulk-actions.ts` — серверные действия.

**Изменяются**

- `backend/internal/users/users.go` — `StatusDeleted`, `Statuses`, поля причины в `User`, два сентинела, расширение `Repository`.
- `backend/internal/users/service.go` — `Block`/`Unblock` через общую машину, `Delete`, `Restore`.
- `backend/internal/postgres/users.go` — `ByIDs`, множественные записи, фильтр списка, `TakenAmong`.
- `backend/internal/audit/audit.go` — `RecordMany`, `Sink.AppendMany`.
- `backend/internal/postgres/audit.go` — пакетная вставка.
- `backend/internal/api/users_handler.go` — причина у `block`, ручки `delete`/`restore`, отображения в `fail`.
- `backend/internal/api/codes.go` — новые коды.
- `frontend/lib/api/accounts-terms.ts`, `accounts.ts`, `app/(admin)/users/*`, словари i18n.

## Task 1: статус `deleted` в схеме и в списке

**Files:**
- Create: `backend/migrations/000015_account_deletion.up.sql`, `backend/migrations/000015_account_deletion.down.sql`
- Modify: `backend/internal/users/users.go` (константы статусов, `Statuses`, `User`), `backend/internal/postgres/users.go:130-134` (условие фильтра), `backend/internal/postgres/users.go:25-41` (`userColumns`), `backend/internal/postgres/users.go:63-77` (`scanUser`)
- Test: `backend/internal/postgres/users_test.go`

**Interfaces:**
- Produces: `users.StatusDeleted`, поля `User.StatusReason string`, `User.StatusChangedAt *time.Time`, `User.StatusChangedBy *uuid.UUID`, тип `users.StatusChange{Reason string; By uuid.UUID; At time.Time}`.

- [ ] **Step 1: написать падающий тест репозитория**

В `backend/internal/postgres/users_test.go`:

```go
func TestListHidesDeletedUnlessAsked(t *testing.T) {
	ctx, repo := newUsersRepo(t)

	live := createUser(t, ctx, repo, "ivanov")
	gone := createUser(t, ctx, repo, "petrov")
	require.NoError(t, repo.SetStatus(ctx, []uuid.UUID{gone.ID}, users.StatusDeleted,
		users.StatusChange{Reason: "left the university", By: live.ID, At: time.Now()}))

	// The default listing is the register an administrator reads: a deleted
	// account is not in it.
	found, total, err := repo.List(ctx, users.Filter{})
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Equal(t, live.ID, found[0].ID)

	// Asked for by name, it is.
	found, total, err = repo.List(ctx, users.Filter{Status: users.StatusDeleted})
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Equal(t, gone.ID, found[0].ID)
	require.Equal(t, "left the university", found[0].StatusReason)
}

func TestDeletedAccountReleasesItsLogin(t *testing.T) {
	ctx, repo := newUsersRepo(t)

	first := createUser(t, ctx, repo, "ivanov")
	require.NoError(t, repo.SetStatus(ctx, []uuid.UUID{first.ID}, users.StatusDeleted,
		users.StatusChange{Reason: "created by mistake", By: first.ID, At: time.Now()}))

	// The whole point of releasing the login: the same one can be created again.
	second, err := repo.Create(ctx, users.User{Login: "ivanov", FullName: "Ivanov", PasswordHash: "x"})
	require.NoError(t, err)
	require.NotEqual(t, first.ID, second.ID)
}
```

Существующие помощники теста посмотреть в начале `users_test.go`; если `createUser` там называется иначе, использовать тамошнее имя.

- [ ] **Step 2: убедиться, что тест падает**

Run: `cd backend && go test ./internal/postgres/ -run 'TestListHidesDeleted|TestDeletedAccountReleases' -v`
Expected: FAIL — компиляция не проходит, `users.StatusDeleted` не объявлен.

- [ ] **Step 3: миграция**

`backend/migrations/000015_account_deletion.up.sql`:

```sql
-- Deletion is a third status rather than a deleted_at column.
--
-- Sign-in (internal/auth/service.go) and the auth middleware both already
-- refuse anything that is not `active`, and CountActiveWithRole counts by
-- status. A third status closes every one of those doors at once; a separate
-- column would need each of them edited by hand, and one forgotten place
-- means a deleted account still works.

ALTER TABLE users DROP CONSTRAINT users_status_check;
ALTER TABLE users ADD CONSTRAINT users_status_check
    CHECK (status IN ('active', 'blocked', 'deleted'));

-- What explains the current status. One set of columns for blocking and for
-- deletion, because what is being explained is the status.
ALTER TABLE users
    ADD COLUMN status_reason     text,
    ADD COLUMN status_changed_at timestamptz,
    ADD COLUMN status_changed_by uuid REFERENCES users;

-- A deleted account must not hold its login hostage: the common reason to
-- delete one is that it was created wrongly, and the next thing the
-- administrator does is create it again properly.
DROP INDEX users_login_lower_key;
CREATE UNIQUE INDEX users_login_lower_key ON users (lower(login))
    WHERE status <> 'deleted';

ALTER TABLE users DROP CONSTRAINT users_email_key;
CREATE UNIQUE INDEX users_email_key ON users (email)
    WHERE status <> 'deleted';

-- Every listing now filters on status, including the default one that hides
-- deleted accounts.
CREATE INDEX users_status_idx ON users (status);
```

`backend/migrations/000015_account_deletion.down.sql`:

```sql
DROP INDEX users_status_idx;

DROP INDEX users_email_key;
ALTER TABLE users ADD CONSTRAINT users_email_key UNIQUE (email);

DROP INDEX users_login_lower_key;
CREATE UNIQUE INDEX users_login_lower_key ON users (lower(login));

ALTER TABLE users
    DROP COLUMN status_changed_by,
    DROP COLUMN status_changed_at,
    DROP COLUMN status_reason;

-- Rolling back cannot leave rows the constraint forbids.
UPDATE users SET status = 'blocked' WHERE status = 'deleted';
ALTER TABLE users DROP CONSTRAINT users_status_check;
ALTER TABLE users ADD CONSTRAINT users_status_check
    CHECK (status IN ('active', 'blocked'));
```

- [ ] **Step 4: домен**

В `backend/internal/users/users.go` расширить константы и `Statuses`:

```go
const (
	StatusActive  = "active"
	StatusBlocked = "blocked"
	// StatusDeleted is an account an administrator has removed. The row stays
	// so results and the audit trail keep their subject; the account cannot
	// sign in, does not count as an administrator, and no longer holds its
	// login.
	StatusDeleted = "deleted"
)

var Statuses = []string{StatusActive, StatusBlocked, StatusDeleted}
```

Добавить в `User` рядом с `Status`:

```go
	// StatusReason is why the account is in its current status, as the
	// administrator who put it there wrote it. Empty for an account nobody has
	// blocked or deleted.
	StatusReason    string
	StatusChangedAt *time.Time
	StatusChangedBy *uuid.UUID
```

И тип изменения статуса:

```go
// StatusChange is the account of a status change: why, by whom, when.
//
// It travels with the new status rather than beside it so that storage cannot
// record a status without recording what explains it.
type StatusChange struct {
	Reason string
	By     uuid.UUID
	At     time.Time
}
```

В `Repository` заменить `SetStatus` на множественную форму:

```go
	// SetStatus moves every named account to the status, recording what
	// explains the move. One method rather than a single and a batch one: the
	// single-account path passes a slice of one, so the two cannot drift.
	SetStatus(ctx context.Context, ids []uuid.UUID, status string, change StatusChange) error
```

- [ ] **Step 5: репозиторий**

В `backend/internal/postgres/users.go` в `userColumns` строка со статусом становится

```
	u.id, u.login, COALESCE(u.email, ''), u.password_hash, u.full_name, u.status,
	COALESCE(u.status_reason, ''), u.status_changed_at, u.status_changed_by,
```

— причина хранится как `NULL`, когда её нет, а читается как пустая строка, чтобы `User.StatusReason` не был указателем ради одного случая. В `scanUser` соответственно после `&u.Status` добавить `&u.StatusReason, &u.StatusChangedAt, &u.StatusChangedBy,`.

Заменить `SetStatus`:

```go
// SetStatus moves the named accounts to the status.
func (r *Users) SetStatus(ctx context.Context, ids []uuid.UUID, status string, change users.StatusChange) error {
	_, err := r.querier(ctx).Exec(ctx, `
		UPDATE users
		SET status = $2, status_reason = $3, status_changed_at = $4,
		    status_changed_by = $5, updated_at = now()
		WHERE id = ANY($1)`,
		ids, status, nullIfEmpty(change.Reason), change.At, change.By)
	if err != nil {
		return fmt.Errorf("set account status: %w", mapUserConstraint(err))
	}
	return nil
}
```

Условие фильтра в `List`:

```go
	// An empty status means the register an administrator reads, which is not
	// "every row": a deleted account appears only when asked for by name.
	const where = `
		WHERE ($1 = '' OR u.login ILIKE '%' || $1 || '%'
		            OR u.full_name ILIKE '%' || $1 || '%'
		            OR COALESCE(u.email, '') ILIKE '%' || $1 || '%')
		  AND (CASE WHEN $2 = '' THEN u.status <> 'deleted' ELSE u.status = $2 END)`
```

Поправить вызовы `SetStatus` в `service.go` на срез из одного элемента, чтобы пакет собирался.

- [ ] **Step 6: прогнать тесты**

Run: `cd backend && make migrate-up && go test ./internal/postgres/ ./internal/users/ -count=1`
Expected: PASS.

- [ ] **Step 7: коммит**

```bash
git add backend/migrations backend/internal/users backend/internal/postgres
git commit -m "feat(users): a deleted account is a status, and gives back its login"
```

## Task 2: причина обязательна у смены статуса

**Files:**
- Modify: `backend/internal/users/users.go`, `backend/internal/users/service.go:139-181`
- Test: `backend/internal/users/service_test.go`

**Interfaces:**
- Consumes: `users.StatusChange` из задачи 1.
- Produces: `users.MaxStatusReasonLength = 500`, `users.ErrReasonRequired`, сигнатура `Block(ctx, actorID, userID uuid.UUID, reason string) error`.

- [ ] **Step 1: падающий тест**

```go
func TestBlockRequiresAReason(t *testing.T) {
	f := newFixture(t)
	target := f.createUser(t, "ivanov")

	err := f.service.Block(t.Context(), f.admin.ID, target.ID, "   ")
	require.ErrorIs(t, err, users.ErrReasonRequired)

	// And the account was not touched on the way to refusing.
	after, err := f.repo.ByID(t.Context(), target.ID)
	require.NoError(t, err)
	require.Equal(t, users.StatusActive, after.Status)
}

func TestBlockBoundsTheReason(t *testing.T) {
	f := newFixture(t)
	target := f.createUser(t, "ivanov")

	err := f.service.Block(t.Context(), f.admin.ID, target.ID, strings.Repeat("x", users.MaxStatusReasonLength+1))
	require.ErrorIs(t, err, users.ErrInvalidAccount)
}

func TestBlockKeepsTheReason(t *testing.T) {
	f := newFixture(t)
	target := f.createUser(t, "ivanov")

	require.NoError(t, f.service.Block(t.Context(), f.admin.ID, target.ID, "cheating in the October contest"))

	after, err := f.repo.ByID(t.Context(), target.ID)
	require.NoError(t, err)
	require.Equal(t, "cheating in the October contest", after.StatusReason)
	require.Equal(t, f.admin.ID, *after.StatusChangedBy)
}
```

Имена помощников взять из существующего `service_test.go`.

- [ ] **Step 2: убедиться, что падает**

Run: `cd backend && go test ./internal/users/ -run TestBlock -v`
Expected: FAIL — `Block` принимает три аргумента.

- [ ] **Step 3: границы и сентинел**

В `users.go` рядом с прочими границами:

```go
// MaxStatusReasonLength bounds the explanation stored with a status.
//
// The column is unbounded text and the request body is bounded at a megabyte,
// so without this a block reason could be a megabyte read by every
// administrator who opens the account.
const MaxStatusReasonLength = 500
```

и рядом с сентинелами:

```go
	// ErrReasonRequired refuses a status change nobody accounted for. Blocking
	// and deleting are answered to afterwards, and "no reason given" is not an
	// answer the trail can carry.
	ErrReasonRequired = errors.New("a reason is required")
```

- [ ] **Step 4: проверка причины и её применение**

В `service.go`:

```go
// validateReason checks the explanation a status change carries.
func validateReason(reason string) (string, error) {
	reason = strings.TrimSpace(reason)
	switch {
	case reason == "":
		return "", ErrReasonRequired
	case len(reason) > MaxStatusReasonLength:
		return "", fmt.Errorf("%w: the reason must be at most %d characters",
			ErrInvalidAccount, MaxStatusReasonLength)
	}
	return reason, nil
}
```

`Block` получает параметр `reason string`, вызывает `validateReason` первым делом и передаёт `StatusChange{Reason: reason, By: actorID, At: time.Now()}` в `SetStatus`, а в аудит — `map[string]any{"reason": reason}` вместо `nil`. `Unblock` причины не требует и передаёт пустую: возвращение к обычному состоянию не нуждается в оправдании, и пустая причина затирает старую.

- [ ] **Step 5: поправить вызывающих**

Run: `cd backend && go build ./... 2>&1 | head`
Обновить обработчик `block` (задача 4 доведёт его до конца) и тесты, которые звали `Block` с тремя аргументами.

- [ ] **Step 6: прогнать**

Run: `cd backend && go test ./internal/users/ -count=1`
Expected: PASS.

- [ ] **Step 7: коммит**

```bash
git add backend/internal/users
git commit -m "feat(users): a block that nobody has to account for is not a block"
```

## Task 3: множественные записи в репозитории и пакетный аудит

**Files:**
- Modify: `backend/internal/users/users.go` (`Repository`), `backend/internal/postgres/users.go`, `backend/internal/audit/audit.go`, `backend/internal/postgres/audit.go`
- Test: `backend/internal/postgres/users_test.go`, `backend/internal/postgres/audit_test.go`

**Interfaces:**
- Produces: `Repository.ByIDs`, `.BumpSessionGenerationMany`, `.ReplaceRolesMany`, `.SetPasswordMany`, `.TakenAmong`; `users.Credential{UserID uuid.UUID; Hash string}`; `audit.Recorder.RecordMany`, `audit.Sink.AppendMany`.

- [ ] **Step 1: падающие тесты**

```go
func TestByIDsReturnsWhatExists(t *testing.T) {
	ctx, repo := newUsersRepo(t)
	first := createUser(t, ctx, repo, "ivanov")
	second := createUser(t, ctx, repo, "petrov")

	// A missing id is not an error: telling the caller which of its ids exist
	// is the whole job, and the bulk path turns the absent ones into skips.
	found, err := repo.ByIDs(ctx, []uuid.UUID{first.ID, uuid.New(), second.ID})
	require.NoError(t, err)
	require.Len(t, found, 2)
}

func TestTakenAmongFindsRestoreConflicts(t *testing.T) {
	ctx, repo := newUsersRepo(t)
	gone := createUser(t, ctx, repo, "ivanov")
	require.NoError(t, repo.SetStatus(ctx, []uuid.UUID{gone.ID}, users.StatusDeleted,
		users.StatusChange{Reason: "mistake", By: gone.ID, At: time.Now()}))
	_, err := repo.Create(ctx, users.User{Login: "ivanov", FullName: "Ivanov", PasswordHash: "x"})
	require.NoError(t, err)

	// Restoring this one would collide with the live account that took the
	// login. Finding that out before the transaction is what keeps the rest of
	// a bulk restore working.
	taken, err := repo.TakenAmong(ctx, []uuid.UUID{gone.ID})
	require.NoError(t, err)
	require.Equal(t, []uuid.UUID{gone.ID}, taken)
}
```

- [ ] **Step 2: убедиться, что падает**

Run: `cd backend && go test ./internal/postgres/ -run 'TestByIDs|TestTakenAmong' -v`
Expected: FAIL — методы не объявлены.

- [ ] **Step 3: расширить `Repository`**

```go
	// ByIDs resolves the accounts that exist among the ids, in no particular
	// order. An id with no account is absent from the result rather than an
	// error: which of them exist is what the caller asked.
	ByIDs(ctx context.Context, ids []uuid.UUID) ([]User, error)
	// BumpSessionGenerationMany retires every session of every named account.
	BumpSessionGenerationMany(ctx context.Context, ids []uuid.UUID) error
	// ReplaceRolesMany sets the same roles on every named account.
	ReplaceRolesMany(ctx context.Context, ids []uuid.UUID, roleCodes []string) error
	// SetPasswordMany stores a digest per account and marks each one as
	// carrying a one-time password.
	SetPasswordMany(ctx context.Context, creds []Credential) error
	// TakenAmong returns the deleted accounts whose login or email a live
	// account now holds, so a restore that would collide is refused before the
	// transaction rather than by it.
	TakenAmong(ctx context.Context, ids []uuid.UUID) ([]uuid.UUID, error)
```

и рядом:

```go
// Credential is one account's new password digest.
type Credential struct {
	UserID uuid.UUID
	Hash   string
}
```

- [ ] **Step 4: реализовать в `internal/postgres/users.go`**

```go
func (r *Users) ByIDs(ctx context.Context, ids []uuid.UUID) ([]users.User, error) {
	rows, err := r.querier(ctx).Query(ctx,
		`SELECT `+userColumns+` FROM users u WHERE u.id = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("read accounts: %w", err)
	}
	defer rows.Close()

	var found []users.User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		found = append(found, u)
	}
	return found, rows.Err()
}

func (r *Users) BumpSessionGenerationMany(ctx context.Context, ids []uuid.UUID) error {
	_, err := r.querier(ctx).Exec(ctx, `
		UPDATE users SET session_generation = session_generation + 1, updated_at = now()
		WHERE id = ANY($1)`, ids)
	if err != nil {
		return fmt.Errorf("retire sessions: %w", err)
	}
	return nil
}

func (r *Users) ReplaceRolesMany(ctx context.Context, ids []uuid.UUID, roleCodes []string) error {
	q := r.querier(ctx)

	if _, err := q.Exec(ctx, `DELETE FROM user_roles WHERE user_id = ANY($1)`, ids); err != nil {
		return fmt.Errorf("clear roles: %w", err)
	}
	if len(roleCodes) == 0 {
		return nil
	}

	// The cross join is the batch form of the single-account insert: every
	// named account against every named role, in one statement.
	tag, err := q.Exec(ctx, `
		INSERT INTO user_roles (user_id, role_id)
		SELECT u.id, r.id
		FROM unnest($1::uuid[]) AS u(id)
		CROSS JOIN roles r
		WHERE r.code = ANY($2)`, ids, roleCodes)
	if err != nil {
		return fmt.Errorf("assign roles: %w", err)
	}
	// Unknown codes are dropped by the join rather than refused by it, so the
	// row count is what turns a typo into an error instead of a silent
	// half-applied change: every account should have gained every code.
	if want := len(ids) * len(roleCodes); int(tag.RowsAffected()) != want {
		return fmt.Errorf("assign roles: %d of %d codes are not known roles",
			len(roleCodes)-int(tag.RowsAffected())/len(ids), len(roleCodes))
	}
	return nil
}

func (r *Users) SetPasswordMany(ctx context.Context, creds []users.Credential) error {
	ids := make([]uuid.UUID, len(creds))
	hashes := make([]string, len(creds))
	for i, c := range creds {
		ids[i], hashes[i] = c.UserID, c.Hash
	}
	_, err := r.querier(ctx).Exec(ctx, `
		UPDATE users u
		SET password_hash = c.hash, must_change_password = true,
		    password_changed_at = now(), updated_at = now()
		FROM unnest($1::uuid[], $2::text[]) AS c(id, hash)
		WHERE u.id = c.id`, ids, hashes)
	if err != nil {
		return fmt.Errorf("set passwords: %w", err)
	}
	return nil
}

func (r *Users) TakenAmong(ctx context.Context, ids []uuid.UUID) ([]uuid.UUID, error) {
	rows, err := r.querier(ctx).Query(ctx, `
		SELECT d.id
		FROM users d
		WHERE d.id = ANY($1) AND d.status = 'deleted' AND EXISTS (
			SELECT 1 FROM users a
			WHERE a.status <> 'deleted'
			  AND (lower(a.login) = lower(d.login)
			       OR (a.email IS NOT NULL AND a.email = d.email)))`, ids)
	if err != nil {
		return nil, fmt.Errorf("check restore conflicts: %w", err)
	}
	defer rows.Close()

	var taken []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan restore conflict: %w", err)
		}
		taken = append(taken, id)
	}
	return taken, rows.Err()
}
```

- [ ] **Step 5: пакетный аудит**

В `internal/audit/audit.go` расширить `Sink` и добавить `RecordMany`:

```go
type Sink interface {
	Append(ctx context.Context, e Entry) error
	// AppendMany stores entries in one statement. A bulk operation writes one
	// entry per account, and a round trip each would undo the reason the
	// operation is batched at all.
	AppendMany(ctx context.Context, entries []Entry) error
}

// RecordMany writes entries that belong to one operation.
func (r *Recorder) RecordMany(ctx context.Context, entries []Entry) error {
	if len(entries) == 0 {
		return nil
	}
	prepared := make([]Entry, len(entries))
	for i, e := range entries {
		if e.Action == "" {
			return errors.New("audit entry has no action")
		}
		e.Payload = redact(e.Payload)
		if meta, ok := ctx.Value(metaKey{}).(requestMeta); ok {
			if e.IP == "" {
				e.IP = meta.ip
			}
			if e.UserAgent == "" {
				e.UserAgent = meta.userAgent
			}
		}
		prepared[i] = e
	}
	return r.sink.AppendMany(ctx, prepared)
}
```

Вынести подготовку одной записи в общий приватный метод, чтобы `Record` и `RecordMany` не разошлись.

В `internal/postgres/audit.go` — `AppendMany` через `pgx.Batch` или один `INSERT ... SELECT * FROM unnest(...)`; предпочесть `unnest`, это один проход.

Обновить тестовые двойники `Sink` в `*test` пакетах.

- [ ] **Step 6: прогнать**

Run: `cd backend && go test ./internal/postgres/ ./internal/audit/ ./internal/users/ -count=1`
Expected: PASS.

- [ ] **Step 7: коммит**

```bash
git add backend/internal
git commit -m "feat(users): the storage a batch needs to be a batch"
```

## Task 4: разбор выбора и `BulkSetStatus`

**Files:**
- Create: `backend/internal/users/bulk.go`, `backend/internal/users/bulk_test.go`
- Modify: `backend/internal/users/service.go` (`Block`, `Unblock` через общую машину)

**Interfaces:**
- Consumes: всё из задач 1–3.
- Produces: `MaxBulkAccounts`, `SkipNotFound`/`SkipSelf`/`SkipLastAdministrator`/`SkipAlreadyInStatus`/`SkipDeleted`/`SkipLoginTaken`, `SkippedAccount{ID uuid.UUID; Login, Reason string}`, `BulkResult{Changed []uuid.UUID; Skipped []SkippedAccount}`, `ErrTooManyAccounts`, `BulkSetStatus(ctx, actorID uuid.UUID, ids []uuid.UUID, status, reason string) (BulkResult, error)`.

- [ ] **Step 1: падающие тесты**

`backend/internal/users/bulk_test.go`:

```go
// The defect a per-account loop has and this design does not: refuseIfLastAdmin
// recounts on every call, so two administrators checked one after another each
// see the other still standing and both go.
func TestBulkBlockKeepsOneAdministrator(t *testing.T) {
	f := newFixture(t)
	first := f.createAdmin(t, "admin-one")
	second := f.createAdmin(t, "admin-two")

	res, err := f.service.BulkSetStatus(t.Context(), f.admin.ID,
		[]uuid.UUID{first.ID, second.ID}, users.StatusBlocked, "end of term")
	require.NoError(t, err)

	require.Len(t, res.Changed, 1)
	require.Len(t, res.Skipped, 1)
	require.Equal(t, users.SkipLastAdministrator, res.Skipped[0].Reason)
}

func TestBulkStatusSkipsRatherThanFails(t *testing.T) {
	f := newFixture(t)
	target := f.createUser(t, "ivanov")
	missing := uuid.New()

	res, err := f.service.BulkSetStatus(t.Context(), f.admin.ID,
		[]uuid.UUID{target.ID, missing, f.admin.ID}, users.StatusDeleted, "graduated")
	require.NoError(t, err)

	require.Equal(t, []uuid.UUID{target.ID}, res.Changed)
	reasons := map[uuid.UUID]string{}
	for _, s := range res.Skipped {
		reasons[s.ID] = s.Reason
	}
	require.Equal(t, users.SkipNotFound, reasons[missing])
	require.Equal(t, users.SkipSelf, reasons[f.admin.ID])
}

func TestBulkStatusBoundsTheSelection(t *testing.T) {
	f := newFixture(t)
	ids := make([]uuid.UUID, users.MaxBulkAccounts+1)
	for i := range ids {
		ids[i] = uuid.New()
	}

	_, err := f.service.BulkSetStatus(t.Context(), f.admin.ID, ids, users.StatusBlocked, "why")
	require.ErrorIs(t, err, users.ErrTooManyAccounts)
}

// The two phases are the point: deciding costs no transaction, and applying
// costs exactly one however many accounts survived.
func TestBulkStatusAppliesInOneTransaction(t *testing.T) {
	f := newFixture(t)
	ids := []uuid.UUID{f.createUser(t, "a").ID, f.createUser(t, "b").ID, f.createUser(t, "c").ID}

	_, err := f.service.BulkSetStatus(t.Context(), f.admin.ID, ids, users.StatusBlocked, "end of term")
	require.NoError(t, err)
	require.Equal(t, 1, f.uow.Calls)
}

func TestBulkStatusOpensNoTransactionWhenNothingSurvives(t *testing.T) {
	f := newFixture(t)

	res, err := f.service.BulkSetStatus(t.Context(), f.admin.ID,
		[]uuid.UUID{uuid.New()}, users.StatusBlocked, "end of term")
	require.NoError(t, err)
	require.Empty(t, res.Changed)
	require.Equal(t, 0, f.uow.Calls)
}
```

Тестовый `UnitOfWork` со счётчиком `Calls` уже есть в `internal/contests/conteststest/fixture.go`; если в `userstest` такого нет, завести по тому же образцу.

- [ ] **Step 2: убедиться, что падает**

Run: `cd backend && go test ./internal/users/ -run TestBulk -v`
Expected: FAIL — `BulkSetStatus` не объявлен.

- [ ] **Step 3: написать `bulk.go`**

```go
package users

// Bulk operations apply one action to a selection of accounts.
//
// They run in two phases, and the split is not an optimisation but a
// correctness requirement. Deciding who may change has to see the selection as
// a whole: refuseIfLastAdmin asks storage how many administrators remain, so a
// loop that called it per account would let a selection holding the last two
// administrators through — each call sees the other one still standing.
//
// So the first phase resolves the selection with one read and decides
// everything in memory, spending a single administrator budget as it goes; the
// second applies the survivors in one transaction with one statement per kind
// of write. A selection of five hundred costs a handful of queries rather than
// a couple of thousand.

// MaxBulkAccounts bounds one operation. The request body is bounded at a
// megabyte, which is thirty thousand identifiers: a bound on the body is not a
// bound on the work.
const MaxBulkAccounts = 500

// Why an account in a selection did not change. A closed vocabulary: the
// client renders each of these, and anything else aborts the operation rather
// than being reported as a row somebody has to go and fix.
const (
	SkipNotFound          = "not_found"
	SkipSelf              = "self"
	SkipLastAdministrator = "last_administrator"
	SkipAlreadyInStatus   = "already_in_status"
	SkipDeleted           = "deleted"
)

// ErrTooManyAccounts refuses a selection above MaxBulkAccounts.
var ErrTooManyAccounts = errors.New("too many accounts in one operation")

// SkippedAccount is one account the operation did not touch.
type SkippedAccount struct {
	ID uuid.UUID
	// Login identifies the account to a person reading the result, who chose
	// the selection by name and not by identifier.
	Login  string
	Reason string
}

// BulkResult reports what an operation did and what it declined to do.
type BulkResult struct {
	Changed []uuid.UUID
	Skipped []SkippedAccount
}

// selection is the outcome of the deciding phase.
type selection struct {
	accepted []User
	skipped  []SkippedAccount
}

// classify reads the selection once and asks decide about each account.
//
// decide returns "" to accept an account or one of the skip reasons. It is
// called in the order the caller gave the ids, so a budget it closes over is
// spent predictably.
func (s *Service) classify(ctx context.Context, ids []uuid.UUID, decide func(User) string) (selection, error) {
	found, err := s.repo.ByIDs(ctx, ids)
	if err != nil {
		return selection{}, err
	}
	byID := make(map[uuid.UUID]User, len(found))
	for _, u := range found {
		byID[u.ID] = u
	}

	var sel selection
	seen := make(map[uuid.UUID]bool, len(ids))
	for _, id := range ids {
		if seen[id] {
			continue // The same account named twice is one account.
		}
		seen[id] = true

		user, ok := byID[id]
		if !ok {
			sel.skipped = append(sel.skipped, SkippedAccount{ID: id, Reason: SkipNotFound})
			continue
		}
		if reason := decide(user); reason != "" {
			sel.skipped = append(sel.skipped, SkippedAccount{ID: id, Login: user.Login, Reason: reason})
			continue
		}
		sel.accepted = append(sel.accepted, user)
	}
	return sel, nil
}

// adminBudget is how many administrators may still be taken away.
//
// Counted once for the whole selection, which is the difference between this
// and asking storage per account.
type adminBudget struct{ remaining int }

func (s *Service) adminBudget(ctx context.Context) (*adminBudget, error) {
	remaining, err := s.repo.CountActiveWithRole(ctx, RoleAdmin)
	if err != nil {
		return nil, fmt.Errorf("count administrators: %w", err)
	}
	return &adminBudget{remaining: remaining}, nil
}

// spend takes one administrator away, or refuses because it is the last.
func (b *adminBudget) spend() bool {
	if b.remaining <= 1 {
		return false
	}
	b.remaining--
	return true
}

// BulkSetStatus moves a selection of accounts to one status.
func (s *Service) BulkSetStatus(ctx context.Context, actorID uuid.UUID, ids []uuid.UUID, status, reason string) (BulkResult, error) {
	if !slices.Contains(Statuses, status) {
		return BulkResult{}, fmt.Errorf("%w: %q is not an account status", ErrInvalidAccount, status)
	}
	// Returning to the ordinary state needs no justification, and an empty
	// reason is what clears the old one.
	if status != StatusActive {
		checked, err := validateReason(reason)
		if err != nil {
			return BulkResult{}, err
		}
		reason = checked
	} else {
		reason = ""
	}
	if err := boundSelection(ids); err != nil {
		return BulkResult{}, err
	}

	budget, err := s.adminBudget(ctx)
	if err != nil {
		return BulkResult{}, err
	}

	// Restoring an account whose login a live one has taken collides with the
	// partial unique index. Asked once, before the transaction, so one
	// collision skips its own account instead of failing the whole operation.
	taken := map[uuid.UUID]bool{}
	if status == StatusActive {
		conflicting, err := s.repo.TakenAmong(ctx, ids)
		if err != nil {
			return BulkResult{}, err
		}
		for _, id := range conflicting {
			taken[id] = true
		}
	}

	sel, err := s.classify(ctx, ids, func(u User) string {
		switch {
		case u.ID == actorID:
			return SkipSelf
		case u.Status == status:
			return SkipAlreadyInStatus
		case taken[u.ID]:
			return SkipLoginTaken
		}
		// Only an account that can administer today is one to protect, and
		// only a status that cannot administer takes it away.
		if holdsAdmin(u.Roles) && u.IsActive() && !budget.spend() {
			return SkipLastAdministrator
		}
		return ""
	})
	if err != nil {
		return BulkResult{}, err
	}
	if len(sel.accepted) == 0 {
		return BulkResult{Skipped: sel.skipped}, nil
	}

	changed := make([]uuid.UUID, len(sel.accepted))
	entries := make([]audit.Entry, len(sel.accepted))
	for i, u := range sel.accepted {
		changed[i] = u.ID
		entries[i] = s.entry(actorID, statusAction(u.Status, status), u.ID,
			map[string]any{"from": u.Status, "reason": reason})
	}

	change := StatusChange{Reason: reason, By: actorID, At: time.Now()}
	err = s.uow.Do(ctx, func(ctx context.Context) error {
		if err := s.repo.SetStatus(ctx, changed, status, change); err != nil {
			return err
		}
		// A status nobody can work in has to stop the tabs that are already
		// open, which is the situation blocking and deletion exist to end.
		if status != StatusActive {
			if err := s.repo.BumpSessionGenerationMany(ctx, changed); err != nil {
				return err
			}
		}
		return s.audit.RecordMany(ctx, entries)
	})
	if err != nil {
		return BulkResult{}, err
	}
	return BulkResult{Changed: changed, Skipped: sel.skipped}, nil
}

func boundSelection(ids []uuid.UUID) error {
	if len(ids) == 0 {
		return fmt.Errorf("%w: no accounts were selected", ErrInvalidAccount)
	}
	if len(ids) > MaxBulkAccounts {
		return fmt.Errorf("%w: %d accounts, the most in one operation is %d",
			ErrTooManyAccounts, len(ids), MaxBulkAccounts)
	}
	return nil
}

// statusAction names the audit action this move is.
//
// It takes both ends because coming back to active is two different events:
// an account returning from a block was unblocked, one returning from deletion
// was restored, and the trail has to say which.
func statusAction(from, to string) string {
	switch {
	case to == StatusBlocked:
		return audit.ActionUserBlock
	case to == StatusDeleted:
		return audit.ActionUserDelete
	case from == StatusDeleted:
		return audit.ActionUserRestore
	default:
		return audit.ActionUserUnblock
	}
}
```

`s.entry` — извлечь из существующего `record` построение `audit.Entry`, чтобы `record` и массовый путь строили её одинаково. В `internal/audit/audit.go` добавить `ActionUserDelete = "user.delete"` и `ActionUserRestore = "user.restore"`.

- [ ] **Step 4: прогнать тесты**

Run: `cd backend && go test ./internal/users/ -run TestBulk -v -count=1`
Expected: PASS.

- [ ] **Step 5: проверить тесты мутацией**

Убрать `budget.spend()` из условия и убедиться, что `TestBulkBlockKeepsOneAdministrator` падает; вернуть. Заменить `if len(sel.accepted) == 0` на `if false` и убедиться, что падает `TestBulkStatusOpensNoTransactionWhenNothingSurvives`; вернуть.

- [ ] **Step 6: коммит**

```bash
git add backend/internal/users backend/internal/audit
git commit -m "feat(users): decide about the whole selection before touching any of it"
```

## Task 5: поштучные операции поверх той же машины

**Files:**
- Modify: `backend/internal/users/service.go:139-181`
- Test: `backend/internal/users/service_test.go`

**Interfaces:**
- Produces: `Delete(ctx, actorID, userID uuid.UUID, reason string) error`, `Restore(ctx, actorID, userID uuid.UUID) error`; `Block`/`Unblock` сохраняют сигнатуры.

- [ ] **Step 1: падающие тесты**

```go
func TestDeleteRefusesTheLastAdministrator(t *testing.T) {
	f := newFixture(t)
	only := f.createAdmin(t, "admin-one")

	err := f.service.Delete(t.Context(), f.admin.ID, only.ID, "left the university")
	require.ErrorIs(t, err, users.ErrLastAdministrator)
}

func TestDeleteRefusesYourself(t *testing.T) {
	f := newFixture(t)

	err := f.service.Delete(t.Context(), f.admin.ID, f.admin.ID, "why not")
	require.ErrorIs(t, err, users.ErrCannotActOnSelf)
}

func TestDeleteRetiresTheSessions(t *testing.T) {
	f := newFixture(t)
	target := f.createUser(t, "ivanov")
	before := target.SessionGeneration

	require.NoError(t, f.service.Delete(t.Context(), f.admin.ID, target.ID, "graduated"))

	after, err := f.repo.ByID(t.Context(), target.ID)
	require.NoError(t, err)
	require.Equal(t, users.StatusDeleted, after.Status)
	require.Greater(t, after.SessionGeneration, before)
}

func TestRestoreRefusesWhenTheLoginWasTaken(t *testing.T) {
	f := newFixture(t)
	gone := f.createUser(t, "ivanov")
	require.NoError(t, f.service.Delete(t.Context(), f.admin.ID, gone.ID, "mistake"))
	f.createUser(t, "ivanov")

	err := f.service.Restore(t.Context(), f.admin.ID, gone.ID)
	require.ErrorIs(t, err, users.ErrLoginTaken)
}
```

- [ ] **Step 2: убедиться, что падает**

Run: `cd backend && go test ./internal/users/ -run 'TestDelete|TestRestore' -v`
Expected: FAIL — `Delete` не объявлен.

- [ ] **Step 3: обёртки**

```go
// setStatus is the single-account form of BulkSetStatus.
//
// One implementation serves both surfaces, so the guards cannot drift apart;
// what differs is only how a refusal is reported. A bulk caller gets a skip
// with a reason because the rest of its selection still applies; a
// single-account caller gets the sentinel, because for them the skip is the
// whole outcome and a silent success would be a lie.
func (s *Service) setStatus(ctx context.Context, actorID, userID uuid.UUID, status, reason string) error {
	res, err := s.BulkSetStatus(ctx, actorID, []uuid.UUID{userID}, status, reason)
	if err != nil {
		return err
	}
	if len(res.Skipped) == 0 {
		return nil
	}
	switch res.Skipped[0].Reason {
	case SkipNotFound:
		return ErrNotFound
	case SkipSelf:
		return ErrCannotActOnSelf
	case SkipLastAdministrator:
		return ErrLastAdministrator
	case SkipLoginTaken:
		return ErrLoginTaken
	default:
		// Already in the asked-for status. The caller wanted it there and it
		// is there; that is a success.
		return nil
	}
}

// Block closes an account. The reason is required: blocking is answered to.
func (s *Service) Block(ctx context.Context, actorID, userID uuid.UUID, reason string) error {
	return s.setStatus(ctx, actorID, userID, StatusBlocked, reason)
}

// Unblock restores access. Existing sessions stay retired: the account has to
// sign in again.
func (s *Service) Unblock(ctx context.Context, actorID, userID uuid.UUID) error {
	return s.setStatus(ctx, actorID, userID, StatusActive, "")
}

// Delete removes an account without removing what it did.
//
// The row stays, so results and the audit trail keep their subject, and the
// account cannot sign in, does not count as an administrator and no longer
// holds its login.
func (s *Service) Delete(ctx context.Context, actorID, userID uuid.UUID, reason string) error {
	return s.setStatus(ctx, actorID, userID, StatusDeleted, reason)
}

// Restore brings a deleted account back. It refuses when a live account has
// taken the login in the meantime — the price of releasing it on deletion.
func (s *Service) Restore(ctx context.Context, actorID, userID uuid.UUID) error {
	return s.setStatus(ctx, actorID, userID, StatusActive, "")
}
```

Старые тела `Block`/`Unblock` удалить целиком; `refuseIfLastAdmin` остаётся для `ReplaceRoles`.

- [ ] **Step 4: прогнать весь пакет**

Run: `cd backend && go test ./internal/users/ -count=1`
Expected: PASS, включая прежние тесты блокировки.

- [ ] **Step 5: коммит**

```bash
git add backend/internal/users
git commit -m "feat(users): one account is a selection of one"
```

## Task 6: `BulkReplaceRoles` и `BulkResetPassword`

**Files:**
- Modify: `backend/internal/users/bulk.go`, `backend/internal/users/bulk_test.go`

**Interfaces:**
- Produces: `BulkReplaceRoles(ctx, actorID uuid.UUID, ids []uuid.UUID, roleCodes []string) (BulkResult, error)`, `BulkResetPassword(ctx, actorID uuid.UUID, ids []uuid.UUID) (BulkPasswordResult, error)`, `IssuedPassword{ID uuid.UUID; Login, OneTimePassword string}`, `BulkPasswordResult{Issued []IssuedPassword; Skipped []SkippedAccount}`.

- [ ] **Step 1: падающие тесты**

```go
func TestBulkRolesKeepsOneAdministrator(t *testing.T) {
	f := newFixture(t)
	first := f.createAdmin(t, "admin-one")
	second := f.createAdmin(t, "admin-two")

	res, err := f.service.BulkReplaceRoles(t.Context(), f.admin.ID,
		[]uuid.UUID{first.ID, second.ID}, []string{"student"})
	require.NoError(t, err)
	require.Len(t, res.Changed, 1)
	require.Equal(t, users.SkipLastAdministrator, res.Skipped[0].Reason)
}

func TestBulkRolesSkipsDeletedAccounts(t *testing.T) {
	f := newFixture(t)
	gone := f.createUser(t, "ivanov")
	require.NoError(t, f.service.Delete(t.Context(), f.admin.ID, gone.ID, "graduated"))

	res, err := f.service.BulkReplaceRoles(t.Context(), f.admin.ID,
		[]uuid.UUID{gone.ID}, []string{"student"})
	require.NoError(t, err)
	require.Empty(t, res.Changed)
	require.Equal(t, users.SkipDeleted, res.Skipped[0].Reason)
}

func TestBulkResetPasswordIssuesOnePerAccount(t *testing.T) {
	f := newFixture(t)
	first := f.createUser(t, "ivanov")
	second := f.createUser(t, "petrov")

	res, err := f.service.BulkResetPassword(t.Context(), f.admin.ID, []uuid.UUID{first.ID, second.ID})
	require.NoError(t, err)
	require.Len(t, res.Issued, 2)

	// Two accounts, two different passwords: one password for a group would be
	// one password to share.
	require.NotEqual(t, res.Issued[0].OneTimePassword, res.Issued[1].OneTimePassword)

	after, err := f.repo.ByID(t.Context(), first.ID)
	require.NoError(t, err)
	require.True(t, after.MustChangePassword)
	require.Greater(t, after.SessionGeneration, first.SessionGeneration)
}
```

- [ ] **Step 2: убедиться, что падает**

Run: `cd backend && go test ./internal/users/ -run 'TestBulkRoles|TestBulkResetPassword' -v`
Expected: FAIL — методы не объявлены.

- [ ] **Step 3: роли**

```go
// BulkReplaceRoles sets the same roles on a selection of accounts.
func (s *Service) BulkReplaceRoles(ctx context.Context, actorID uuid.UUID, ids []uuid.UUID, roleCodes []string) (BulkResult, error) {
	if err := boundSelection(ids); err != nil {
		return BulkResult{}, err
	}
	budget, err := s.adminBudget(ctx)
	if err != nil {
		return BulkResult{}, err
	}
	keepsAdmin := slices.Contains(roleCodes, RoleAdmin)

	sel, err := s.classify(ctx, ids, func(u User) string {
		if u.Status == StatusDeleted {
			return SkipDeleted
		}
		if holdsAdmin(u.Roles) && u.IsActive() && !keepsAdmin && !budget.spend() {
			return SkipLastAdministrator
		}
		return ""
	})
	if err != nil {
		return BulkResult{}, err
	}
	if len(sel.accepted) == 0 {
		return BulkResult{Skipped: sel.skipped}, nil
	}

	changed := make([]uuid.UUID, len(sel.accepted))
	entries := make([]audit.Entry, len(sel.accepted))
	for i, u := range sel.accepted {
		changed[i] = u.ID
		changes := audit.NewChanges()
		changes.Set("roles", u.Roles, roleCodes)
		entries[i] = s.entry(actorID, audit.ActionUserRolesChange, u.ID, changes.Payload())
	}

	err = s.uow.Do(ctx, func(ctx context.Context) error {
		if err := s.repo.ReplaceRolesMany(ctx, changed, roleCodes); err != nil {
			return err
		}
		// New limits have to bite immediately: a demotion that waited for the
		// next login would leave someone exercising rights they no longer hold.
		if err := s.repo.BumpSessionGenerationMany(ctx, changed); err != nil {
			return err
		}
		return s.audit.RecordMany(ctx, entries)
	})
	if err != nil {
		return BulkResult{}, err
	}
	return BulkResult{Changed: changed, Skipped: sel.skipped}, nil
}
```

- [ ] **Step 4: пароли с параллельным хешированием**

```go
// IssuedPassword is one account's new one-time password, to be handed over.
type IssuedPassword struct {
	ID    uuid.UUID
	Login string
	// OneTimePassword is shown once and never stored in the clear.
	OneTimePassword string
}

// BulkPasswordResult reports the passwords issued and the accounts skipped.
type BulkPasswordResult struct {
	Issued  []IssuedPassword
	Skipped []SkippedAccount
}

// hashWorkers bounds the parallel hashing.
//
// argon2id is deliberately expensive — tens of milliseconds and a large buffer
// per call — so five hundred of them in sequence is most of a minute, and five
// hundred at once is a memory spike an administrator can trigger from a form.
// The same reasoning, and the same number, as provisioning.DefaultWorkers.
const hashWorkers = 3

// BulkResetPassword issues a new one-time password per selected account.
func (s *Service) BulkResetPassword(ctx context.Context, actorID uuid.UUID, ids []uuid.UUID) (BulkPasswordResult, error) {
	if err := boundSelection(ids); err != nil {
		return BulkPasswordResult{}, err
	}

	sel, err := s.classify(ctx, ids, func(u User) string {
		if u.Status == StatusDeleted {
			return SkipDeleted
		}
		return ""
	})
	if err != nil {
		return BulkPasswordResult{}, err
	}
	if len(sel.accepted) == 0 {
		return BulkPasswordResult{Skipped: sel.skipped}, nil
	}

	issued := make([]IssuedPassword, len(sel.accepted))
	creds := make([]Credential, len(sel.accepted))
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(hashWorkers)
	for i, u := range sel.accepted {
		i, u := i, u
		group.Go(func() error {
			if groupCtx.Err() != nil {
				return groupCtx.Err()
			}
			oneTime, err := generatePassword()
			if err != nil {
				return err
			}
			hash, err := password.Hash(oneTime)
			if err != nil {
				return fmt.Errorf("hash password: %w", err)
			}
			issued[i] = IssuedPassword{ID: u.ID, Login: u.Login, OneTimePassword: oneTime}
			creds[i] = Credential{UserID: u.ID, Hash: hash}
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return BulkPasswordResult{}, err
	}

	changed := make([]uuid.UUID, len(sel.accepted))
	entries := make([]audit.Entry, len(sel.accepted))
	for i, u := range sel.accepted {
		changed[i] = u.ID
		entries[i] = s.entry(actorID, audit.ActionUserPasswordReset, u.ID, nil)
	}

	err = s.uow.Do(ctx, func(ctx context.Context) error {
		if err := s.repo.SetPasswordMany(ctx, creds); err != nil {
			return err
		}
		if err := s.repo.BumpSessionGenerationMany(ctx, changed); err != nil {
			return err
		}
		return s.audit.RecordMany(ctx, entries)
	})
	if err != nil {
		return BulkPasswordResult{}, err
	}
	return BulkPasswordResult{Issued: issued, Skipped: sel.skipped}, nil
}
```

`golang.org/x/sync/errgroup` уже в зависимостях (используется в `internal/provisioning`); проверить и при необходимости добавить.

- [ ] **Step 5: прогнать**

Run: `cd backend && go test ./internal/users/ -count=1`
Expected: PASS.

- [ ] **Step 6: коммит**

```bash
git add backend/internal/users
git commit -m "feat(users): roles and passwords for a whole selection"
```

## Task 7: HTTP

**Files:**
- Create: `backend/internal/api/users_bulk_handler.go`, `backend/internal/api/users_bulk_handler_test.go`
- Modify: `backend/internal/api/users_handler.go` (маршруты, `block`, `delete`, `restore`, `fail`), `backend/internal/api/codes.go`
- Test: `backend/internal/api/users_handler_test.go`

**Interfaces:**
- Consumes: всё из задач 4–6.
- Produces: `POST /users/{userID}/delete`, `/restore`, `POST /users/bulk/status`, `/bulk/roles`, `/bulk/password-reset`; коды `reason_required`, `too_many_accounts`.

- [ ] **Step 1: падающие тесты обработчиков**

```go
func TestBlockWithoutAReasonIsABadRequest(t *testing.T) {
	h := newUsersHandler(t)

	res := h.post(t, "/users/"+h.target.ID.String()+"/block", `{"reason":"  "}`)
	requireError(t, res, http.StatusBadRequest, "reason_required")
}

func TestBulkAboveTheBoundIsABadRequest(t *testing.T) {
	h := newUsersHandler(t)
	ids := make([]string, users.MaxBulkAccounts+1)
	for i := range ids {
		ids[i] = uuid.NewString()
	}

	res := h.post(t, "/users/bulk/status", `{"ids":["`+strings.Join(ids, `","`)+`"],"status":"blocked","reason":"end of term"}`)
	requireError(t, res, http.StatusBadRequest, "too_many_accounts")
}

// One unusable identifier is a row in the answer, not a failure of the
// operation: the rest of the selection still applied.
func TestBulkReportsUnknownAccountsAsSkipped(t *testing.T) {
	h := newUsersHandler(t)

	res := h.post(t, "/users/bulk/status",
		`{"ids":["`+h.target.ID.String()+`","`+uuid.NewString()+`"],"status":"blocked","reason":"end of term"}`)
	require.Equal(t, http.StatusOK, res.Code)

	var body struct {
		Changed []string `json:"changed"`
		Skipped []struct {
			Reason string `json:"reason"`
		} `json:"skipped"`
	}
	require.NoError(t, json.Unmarshal(res.Body.Bytes(), &body))
	require.Len(t, body.Changed, 1)
	require.Equal(t, "not_found", body.Skipped[0].Reason)
}
```

Помощники `newUsersHandler`, `post`, `requireError` взять из существующего `users_handler_test.go`.

- [ ] **Step 2: убедиться, что падает**

Run: `cd backend && go test ./internal/api/ -run 'TestBlockWithout|TestBulk' -v`
Expected: FAIL — маршрута нет, ответ 404.

- [ ] **Step 3: коды**

В `internal/api/codes.go` в разделе аккаунтов:

```go
	codeReasonRequired = httpx.NewCode("reason_required",
		"Blocking or deleting an account has to say why. The reason is stored with the account and read by whoever asks about it later.")
	codeTooManyAccounts = httpx.NewCode("too_many_accounts",
		"More accounts were selected than one operation carries. The message says the limit.")
```

- [ ] **Step 4: отображения и маршруты**

В `fail` добавить перед общей веткой `ErrInvalidAccount`:

```go
	case errors.Is(err, users.ErrReasonRequired):
		httpx.Error(w, r, http.StatusBadRequest, codeReasonRequired,
			"A reason is required")
	case errors.Is(err, users.ErrTooManyAccounts):
		httpx.Error(w, r, http.StatusBadRequest, codeTooManyAccounts, err.Error())
```

В `Mount`, внутри `r.Route("/users", ...)` до маршрута с параметром:

```go
		// A sibling of the single-account routes rather than a query on them:
		// the selection is the subject.
		r.Route("/bulk", func(r chi.Router) {
			r.Post("/status", h.bulkStatus)
			r.Post("/roles", h.bulkRoles)
			r.Post("/password-reset", h.bulkResetPassword)
		})
```

и внутри `r.Route("/{"+userIDParam+"}", ...)`:

```go
			r.Post("/delete", h.deleteAccount)
			r.Post("/restore", h.restore)
```

`block` читает тело `struct{ Reason string `json:"reason"` }` и передаёт причину. `deleteAccount` — то же с `service.Delete`; `restore` — без тела.

- [ ] **Step 5: массовые ручки**

`internal/api/users_bulk_handler.go`:

```go
package api

// Bulk account operations.
//
// Each answers 200 with an account of what happened, never a failure because
// one identifier in the selection was unusable: the rest of the selection
// applied, and an error would misreport that. Only a refusal of the request as
// a whole — an unbounded selection, a missing reason — is a 4xx.

// bulkStatusRequest is a selection and the status to move it to.
type bulkStatusRequest struct {
	IDs    []uuid.UUID `json:"ids"`
	Status string      `json:"status"`
	Reason string      `json:"reason"`
}

// bulkResponse is what an operation did.
type bulkResponse struct {
	Changed []uuid.UUID       `json:"changed"`
	Skipped []skippedResponse `json:"skipped"`
}

// skippedResponse is one account the operation declined to touch. The reason
// is a code from a closed vocabulary, which the client renders in its own
// language.
type skippedResponse struct {
	ID     uuid.UUID `json:"id"`
	Login  string    `json:"login"`
	Reason string    `json:"reason"`
}

func (h *UsersHandler) bulkStatus(w http.ResponseWriter, r *http.Request) {
	var req bulkStatusRequest
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	identity, _ := auth.IdentityFrom(r.Context())

	res, err := h.service.BulkSetStatus(r.Context(), identity.UserID, req.IDs, req.Status, req.Reason)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, asBulkResponse(res))
}
```

`bulkRoles` и `bulkResetPassword` — по тому же образцу; у второго ответ несёт `issued` с логином и одноразовым паролем. Имя декодера и функции ответа взять те же, что в `users_handler.go`.

- [ ] **Step 6: прогнать**

Run: `cd backend && go test ./internal/api/ -count=1 && go vet ./...`
Expected: PASS.

- [ ] **Step 7: коммит**

```bash
git add backend/internal/api
git commit -m "feat(api): the endpoints a selection of accounts needs"
```

## Task 8: клиент API и словарь на фронтенде

**Files:**
- Modify: `frontend/lib/api/accounts-terms.ts`, `frontend/lib/api/accounts.ts`
- Test: `frontend/lib/api/accounts.test.ts`

**Interfaces:**
- Produces: `ACCOUNT_STATUSES` с `"deleted"`, `SKIP_REASONS`, типы `BulkResult`, `IssuedPassword`; функции `bulkSetStatus`, `bulkReplaceRoles`, `bulkResetPassword`, `deleteAccount`, `restoreAccount`.

- [ ] **Step 1: расширить термины**

`frontend/lib/api/accounts-terms.ts` — файл существует и намеренно не содержит `z.object()`, потому что его импортируют клиентские компоненты:

```ts
export const ACCOUNT_STATUSES = ["active", "blocked", "deleted"] as const;
export type AccountStatus = (typeof ACCOUNT_STATUSES)[number];

/**
 * Why an account in a selection did not change.
 *
 * The server's own vocabulary, mirrored here so the selection bar can render
 * every outcome. A reason the list does not name is shown raw rather than
 * dropped: an unnamed outcome is still an outcome the administrator has to see.
 */
export const SKIP_REASONS = [
  "not_found",
  "self",
  "last_administrator",
  "already_in_status",
  "deleted",
  "login_taken",
] as const;
export type SkipReason = (typeof SKIP_REASONS)[number];

/** The most accounts one operation carries. Mirrors users.MaxBulkAccounts. */
export const MAX_BULK_ACCOUNTS = 500;
```

- [ ] **Step 2: схемы и вызовы**

В `frontend/lib/api/accounts.ts` — re-export терминов и схемы ответов по образцу соседних модулей, плюс пять функций через существующий клиент. Статус аккаунта в схеме берётся из `ACCOUNT_STATUSES`, чтобы «удалённый» не пришлось добавлять дважды.

- [ ] **Step 3: тест разбора ответа**

```ts
it("reads a bulk result, unknown skip reasons included", () => {
  const parsed = bulkResultSchema.parse({
    changed: ["8f1a…"],
    skipped: [{ id: "…", login: "ivanov", reason: "invented_later" }],
  });
  expect(parsed.skipped[0].reason).toBe("invented_later");
});
```

- [ ] **Step 4: прогнать**

Run: `cd frontend && npm test -- lib/api/accounts`
Expected: PASS.

- [ ] **Step 5: коммит**

```bash
git add frontend/lib/api
git commit -m "feat(accounts): the vocabulary a selection speaks"
```

## Task 9: выбор строк на экране `/users`

**Files:**
- Create: `frontend/app/(admin)/users/selection.tsx`, `frontend/app/(admin)/users/selection.test.tsx`
- Modify: `frontend/app/(admin)/users/account-register.tsx`, `frontend/app/(admin)/users/page.tsx`

**Interfaces:**
- Produces: `<SelectionProvider>`, `<RowCheckbox id>`, `<SelectionBar>`, хук `useSelectedIds()`.

- [ ] **Step 1: падающий тест**

```tsx
it("selecting a row re-renders that row and not its neighbours", async () => {
  const renders = { first: 0, second: 0 };
  // Counting renders is the assertion: a context that re-renders every row on
  // every click is exactly what this store exists to avoid, and only a count
  // catches it.
  render(
    <SelectionProvider>
      <Counted onRender={() => renders.first++} id="a" />
      <Counted onRender={() => renders.second++} id="b" />
    </SelectionProvider>,
  );
  const before = renders.second;

  await userEvent.click(screen.getByRole("checkbox", { name: /a/ }));

  expect(renders.second).toBe(before);
});
```

- [ ] **Step 2: убедиться, что падает**

Run: `cd frontend && npm test -- users/selection`
Expected: FAIL — модуля нет.

- [ ] **Step 3: хранилище**

`frontend/app/(admin)/users/selection.tsx`:

```tsx
"use client";

/**
 * Which accounts the administrator has picked.
 *
 * An external store read through useSyncExternalStore rather than a context
 * value, because a context re-renders every consumer on every change: with
 * fifty rows on a page, ticking one box would re-render fifty checkboxes and
 * the bar. Here each checkbox subscribes to its own membership and the bar to
 * the count, so a click re-renders one row.
 *
 * The page itself stays a server component. Only the boxes and the bar are
 * client code, which is the whole of what needs state.
 */

class SelectionStore {
  private ids = new Set<string>();
  private listeners = new Set<() => void>();

  subscribe = (listener: () => void) => {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  };

  has = (id: string) => this.ids.has(id);
  size = () => this.ids.size;
  selected = () => [...this.ids];

  toggle = (id: string) => {
    if (!this.ids.delete(id)) this.ids.add(id);
    this.emit();
  };

  replace = (ids: string[]) => {
    this.ids = new Set(ids);
    this.emit();
  };

  private emit() {
    for (const listener of this.listeners) listener();
  }
}
```

Провайдер держит один экземпляр через `useState(() => new SelectionStore())`; `RowCheckbox` читает `useSyncExternalStore(store.subscribe, () => store.has(id), () => false)` — третий аргумент даёт серверу «не выбрано» и снимает расхождение при гидратации.

- [ ] **Step 4: вставить в реестр**

В `account-register.tsx` — колонка с `<RowCheckbox id={account.id} label={account.fullName} />` первой, и заголовок с флажком «выбрать страницу». Файл остаётся серверным: клиентский компонент можно импортировать в серверный.

В `page.tsx` обернуть реестр и панель в `<SelectionProvider>`.

- [ ] **Step 5: прогнать**

Run: `cd frontend && npm test -- users/selection && npm run typecheck`
Expected: PASS.

- [ ] **Step 6: коммит**

```bash
git add "frontend/app/(admin)/users"
git commit -m "feat(users): pick the rows without re-rendering the page"
```

## Task 10: действия над выбором и их итог

**Files:**
- Create: `frontend/app/(admin)/users/bulk-actions.ts`
- Modify: `frontend/app/(admin)/users/selection.tsx` (панель и диалоги)
- Test: `frontend/app/(admin)/users/selection.test.tsx`

- [ ] **Step 1: падающий тест**

```tsx
it("refuses to send a block without a reason", async () => {
  renderBar({ selected: ["a", "b"] });
  await userEvent.click(screen.getByRole("button", { name: /заблокировать/i }));

  await userEvent.click(screen.getByRole("button", { name: /подтвердить/i }));

  expect(submitted).not.toHaveBeenCalled();
  expect(screen.getByText(/укажите причину/i)).toBeVisible();
});

it("names the accounts it did not touch", async () => {
  renderBar({ selected: ["a"], result: { changed: [], skipped: [{ id: "a", login: "ivanov", reason: "last_administrator" }] } });
  expect(await screen.findByText(/ivanov/)).toBeVisible();
  expect(screen.getByText(/последний администратор/i)).toBeVisible();
});
```

- [ ] **Step 2: убедиться, что падает**

Run: `cd frontend && npm test -- users/selection`
Expected: FAIL.

- [ ] **Step 3: серверные действия**

`bulk-actions.ts` с `"use server"`: по действию на каждую ручку, каждое зовёт клиент API и заканчивается `revalidatePath("/users")`. Форма ответа — то же, что у сервера, плюс поле ошибки для `useActionState`.

- [ ] **Step 4: панель и диалоги**

Панель показывается, когда выбор непуст. Диалог блокировки и удаления требует причину и не даёт отправить пустую; диалог ролей выбирает набор из каталога; сброс паролей подтверждается и показывает выданные пароли списком с возможностью скопировать.

Итог — панель «изменено N / пропущено M» с логином и переведённой причиной по каждому пропуску; неизвестный код причины показывается как есть.

- [ ] **Step 5: прогнать**

Run: `cd frontend && npm test -- users/ && npm run typecheck`
Expected: PASS.

- [ ] **Step 6: коммит**

```bash
git add "frontend/app/(admin)/users"
git commit -m "feat(users): one action over a selection, and an honest account of it"
```

## Task 11: удалённые в фильтре и в карточке аккаунта

**Files:**
- Modify: `frontend/app/(admin)/users/filters.tsx`, `frontend/app/(admin)/users/account-register.tsx` (`STATUS_TONE`), `frontend/app/(admin)/users/[userId]/account-card.tsx`, `frontend/app/(admin)/users/[userId]/actions.ts`, `frontend/app/(admin)/users/[userId]/offered.ts`
- Test: `frontend/app/(admin)/users/filters.test.tsx`, `frontend/app/(admin)/users/[userId]/offered.test.ts`

- [ ] **Step 1: падающие тесты**

```ts
it("offers restore for a deleted account and nothing that assumes it works", () => {
  const offered = offeredFor({ status: "deleted" });
  expect(offered).toContain("restore");
  expect(offered).not.toContain("block");
  expect(offered).not.toContain("password-reset");
});
```

- [ ] **Step 2: убедиться, что падает**

Run: `cd frontend && npm test -- users/`
Expected: FAIL.

- [ ] **Step 3: реализовать**

`STATUS_TONE` получает `deleted: "bad"` (или отдельный приглушённый тон, если он есть в системе тонов). Фильтр статуса перечисляет `ACCOUNT_STATUSES`, поэтому «удалённые» появляются сами; проверить, что пустое значение по-прежнему называется «все» и что оно означает «кроме удалённых» — подпись поправить.

`offered.ts` перестаёт предлагать блокировку, сброс пароля и смену ролей для удалённого и предлагает восстановление. Карточка показывает причину, автора и дату смены статуса.

- [ ] **Step 4: прогнать**

Run: `cd frontend && npm test -- users/ && npm run typecheck`
Expected: PASS.

- [ ] **Step 5: коммит**

```bash
git add "frontend/app/(admin)/users"
git commit -m "feat(users): what a deleted account looks like"
```

## Task 12: строки интерфейса

**Files:**
- Modify: словари i18n (`frontend/lib/i18n/dictionaries/*`)

- [ ] **Step 1: добавить строки**

Причины пропуска, подписи действий панели, заголовки диалогов, обязательность причины, статус «удалён», подписи восстановления, подписи выданных паролей — на обоих языках. Ключи под `accounts.bulk` и `accounts.status.deleted`.

- [ ] **Step 2: проверить полноту**

Run: `cd frontend && npm test -- i18n && npm run typecheck`
Expected: PASS — тест сравнения словарей не находит расхождений.

- [ ] **Step 3: коммит**

```bash
git add frontend/lib/i18n
git commit -m "feat(i18n): the words the selection bar needs"
```

## Task 13: сквозная проверка

**Files:**
- Modify: `backend/internal/auth/service_test.go`, `backend/internal/api/integration_test.go`

- [ ] **Step 1: тесты**

```go
func TestDeletedAccountCannotSignIn(t *testing.T) {
	f := newAuthFixture(t)
	account := f.createUser(t, "ivanov", "correct-horse-battery")
	f.users.Delete(t.Context(), f.admin.ID, account.ID, "graduated")

	_, err := f.service.SignIn(t.Context(), "ivanov", "correct-horse-battery", auth.Origin{})
	require.ErrorIs(t, err, auth.ErrAccountBlocked)
}
```

и в `integration_test.go` — сценарий: создать двух, выделить обоих, удалить массово, убедиться что список по умолчанию их не показывает, восстановить одного, убедиться что он снова входит.

- [ ] **Step 2: прогнать всё**

Run: `cd backend && go test ./... -count=1 && make static-check && cd ../frontend && npm test && npm run typecheck && npm run build`
Expected: PASS.

- [ ] **Step 3: коммит**

```bash
git add backend frontend
git commit -m "test(users): deletion closes both doors"
```
