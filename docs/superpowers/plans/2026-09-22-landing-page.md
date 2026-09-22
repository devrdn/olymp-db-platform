# Главная страница — план реализации

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** у продукта появляется публичный корень `/`: имя установки, описание,
живые числа, три пункта «как это устроено», превью консоли, ближайшие и
недавние олимпиады, строка для организаторов — с молдавским орнаментом,
вписанным в существующую систему токенов.

**Architecture:** два новых публичных чтения в API под префиксом `/public/`
(вне аутентификации, за бюджетом частоты по адресу и кэшем на минуту) и одна
страница Next в группе `(public)`, собранная из существующих примитивов
(`Band`, `FocusShell`) плюс один новый параметрический SVG-модуль орнамента.
Ни одной новой шрифтовой ступени, ни одного произвольного цвета.

**Tech Stack:** Go (chi, pgx, `internal/platform/cache`), Next 16 (App Router,
серверные компоненты), Tailwind v4 через `@theme`, vitest, zod.

**Spec:** `docs/superpowers/specs/2026-09-22-landing-page-design.md` — каждая
задача читает его целиком перед началом.

## Global Constraints

- `CLAUDE.md` в корне обязателен целиком. Особенно: правило 1 (отказ — объявленный
  сентинел, маппинг в `fail`, тест обработчика, код в `docs/api/error-codes.json`,
  тексты en/ru/ro), правило 2 (явные пределы), правило 5 и 13 (ограничитель до
  дорогой работы, отказы считаются), правило 7 (фильтр опирается на индекс),
  раскладка Go, английский в коде и коммитах, русский в `docs/`, линейная история.
- **Произвольные цвета запрещены** (`bg-[#…]`, `text-[#…]`, `border-[#…]`) — ловит
  ESLint. Единственный источник — `frontend/styles/tokens.css`.
- **Новых шрифтовых ступеней не заводить.** Использовать существующие:
  `text-display`, `text-h2`, `text-h3`, `text-lede`, `text-body`, `text-small`,
  `text-label`, `text-data`.
- **Тёмная тема переопределяет только семантический уровень** токенов.
- Ширины проверки: 1440, 1024, 768, 375. Мобильный первым, страница не едет вбок.
- Движение ≤ 200 мс, `prefers-reduced-motion` выключает его полностью.
- Весь видимый текст — в словарях `frontend/lib/i18n/dictionaries/{en,ru,ro}.ts`.
- Черновики олимпиад не показываются нигде и никогда.
- Реальные данные не трогать: только `dbcontest_core_test` (через `make test-db`).
  Никаких `make run`, `make runner`, `make dev-*` и docker-команд.
- В индекс добавлять файлы явно по путям; `git add -A` и `git add .` запрещены.

---

### Task 1: Публичные чтения в API

**Files:**
- Create: `backend/internal/showcase/showcase.go`
- Create: `backend/internal/showcase/showcase_test.go`
- Create: `backend/internal/postgres/showcase.go`
- Create: `backend/internal/postgres/showcase_test.go`
- Create: `backend/internal/api/public_handler.go`
- Create: `backend/internal/api/public_handler_test.go`
- Modify: `backend/internal/app/app.go` (зарегистрировать модуль в списке `Modules`)

**Interfaces:**
- Consumes: `auth.Limiter` (`Allow(ctx, subject string, limit int, window time.Duration) (bool, error)`),
  `cache.Cache`, `httpx.ClientSubject(r) string`, `storage.Querier`.
- Produces:
  - `showcase.Numbers{Contests, Participants, Queries, Solved int64}`
  - `showcase.Contest{ID uuid.UUID, Title string, Status string, StartsAt, EndsAt *time.Time, TableOpen bool}`
  - `showcase.Service.Numbers(ctx) (Numbers, error)`
  - `showcase.Service.Recent(ctx, lang string) ([]Contest, error)`
  - `showcase.MaxRecent = 6`, `showcase.CacheTTL = time.Minute`
  - `api.PublicReadsPerMinute = 120`

- [ ] **Step 1: Прочитать спеку и образцы**

Прочитать `docs/superpowers/specs/2026-09-22-landing-page-design.md` (разделы 1 и 4)
и, как образец публичного маршрута с бюджетом по адресу,
`backend/internal/api/leaderboard_handler.go` — функции `Mount`, `public`,
`admit`, `addressKey`, `noIndex` и константу `LeaderboardPublicPerMinute`.

- [ ] **Step 2: Написать падающий тест репозитория**

В `backend/internal/postgres/showcase_test.go`:

```go
func TestRecentContestsLeaveOutDrafts(t *testing.T) {
	withTx(t, func(ctx context.Context) {
		repo := NewShowcase(testPool)
		author := makeUser(t, ctx, "author-showcase")
		published := makeContest(t, ctx, author.ID)
		exec(t, ctx, `UPDATE contests SET status = 'published' WHERE id = $1`, published)
		draft := makeContest(t, ctx, author.ID) // makeContest leaves it a draft

		got, err := repo.Recent(ctx, 6)
		if err != nil {
			t.Fatalf("Recent() = %v", err)
		}

		var seen []uuid.UUID
		for _, c := range got {
			seen = append(seen, c.ID)
		}
		if !slices.Contains(seen, published) {
			t.Errorf("Recent() = %v, want the published contest %s", seen, published)
		}
		if slices.Contains(seen, draft) {
			t.Error("a draft contest reached the public page")
		}
	})
}
```

- [ ] **Step 3: Запустить и убедиться, что падает**

Run: `make test-db` (из корня репозитория)
Expected: FAIL — `undefined: NewShowcase`.

- [ ] **Step 4: Реализовать репозиторий**

В `backend/internal/postgres/showcase.go`. Числа считаются по
`registration_activity`, а не `count(*)` по `query_log` — это требование спеки,
раздел 4:

```go
// Recent returns the contests a visitor may see, newest first.
func (r *Showcase) Recent(ctx context.Context, limit int) ([]showcase.Contest, error) {
	rows, err := r.querier(ctx).Query(ctx, `
		SELECT c.id, c.status, c.starts_at, c.ends_at,
		       (c.status IN ('finished', 'archived') OR c.leaderboard_revealed_at IS NOT NULL) AS table_open
		FROM contests c
		WHERE c.status IN ('published', 'running', 'finished', 'archived')
		ORDER BY COALESCE(c.starts_at, c.created_at) DESC, c.id DESC
		LIMIT $1`, limit)
	...
}
```

Название берётся из перевода на язык посетителя — посмотреть, как это уже
делает каталог олимпиад (`internal/postgres/contests.go`, чтение
`contest_translations`), и повторить тот же приём, а не изобретать второй.

Числа:

```go
func (r *Showcase) Numbers(ctx context.Context) (showcase.Numbers, error) {
	var n showcase.Numbers
	err := r.querier(ctx).QueryRow(ctx, `
		SELECT (SELECT count(*) FROM contests WHERE status IN ('finished', 'archived')),
		       (SELECT count(*) FROM registrations),
		       (SELECT COALESCE(sum(queries), 0) FROM registration_activity),
		       (SELECT count(*) FROM submissions WHERE is_correct)`).
		Scan(&n.Contests, &n.Participants, &n.Queries, &n.Solved)
	return n, err
}
```

- [ ] **Step 5: Проверить, что тест проходит**

Run: `make test-db`
Expected: PASS.

- [ ] **Step 6: Написать падающий тест обработчика**

В `backend/internal/api/public_handler_test.go` — три теста: отдаёт 200 без
сессии; отказ по частоте отвечает 429; черновик не попадает в ответ. Образец
фикстуры — соседние `*_handler_test.go` в том же пакете.

```go
func TestPublicReadsAnswerWithoutASession(t *testing.T) {
	f := newPublicFixture(t)
	f.showcase.numbers = showcase.Numbers{Contests: 3, Participants: 40, Queries: 900, Solved: 120}

	rec := f.get("/public/stats")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Queries int64 `json:"queries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if body.Queries != 900 {
		t.Errorf("queries = %d, want 900", body.Queries)
	}
}

func TestPublicReadsAreRefusedPastTheirBudget(t *testing.T) {
	f := newPublicFixture(t)

	var last *httptest.ResponseRecorder
	for i := 0; i <= api.PublicReadsPerMinute; i++ {
		last = f.get("/public/stats")
	}

	if last.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 once the budget is spent", last.Code)
	}
	if f.showcase.reads > api.PublicReadsPerMinute {
		t.Errorf("the repository was read %d times: a refusal must cost no database work", f.showcase.reads)
	}
}
```

- [ ] **Step 7: Убедиться, что падает, затем реализовать обработчик**

Run: `cd backend && go test ./internal/api/ -run TestPublic -v`
Expected сначала: FAIL. Затем реализовать в `backend/internal/api/public_handler.go`:

```go
// Mount registers the two reads a visitor without a session may make.
//
// Outside Authenticate, beside the public leaderboard, and under /public/
// because /contests is a path whose other routes require a session: two
// access rules on one path is the mistake nobody notices later.
func (h *PublicHandler) Mount(r chi.Router) {
	r.Get("/public/stats", h.stats)
	r.Get("/public/contests", h.contests)
}

func (h *PublicHandler) stats(w http.ResponseWriter, r *http.Request) {
	// Before any database work: a refused caller still spends its own budget
	// (CLAUDE.md rule 13).
	if !h.admit(w, r) {
		return
	}
	numbers, err := h.service.Numbers(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, numbersResponse(numbers))
}
```

`admit` повторяет `LeaderboardHandler.admit`: ключ `"public:ip:"+addressKey(r)`,
предел `PublicReadsPerMinute = 120`, окно — минута, отказ 429 с уже
существующим кодом слишком частого чтения (новый сентинел не нужен, если
подходящий код уже есть; если нет — завести по правилу 1 целиком).

- [ ] **Step 8: Кэш на минуту**

В `backend/internal/showcase/showcase.go` — тот же приём, что у
`internal/leaderboard`: кэш на минуту плюс `singleflight`, чтобы сотня
одновременных посетителей стоила одного чтения базы. Тест:

```go
func TestNumbersAreReadOnceForManyCallers(t *testing.T) {
	// Ten concurrent calls, a repository that counts its reads: one read.
}
```

- [ ] **Step 9: Зарегистрировать модуль и прогнать проверки**

Добавить обработчик в список модулей в `backend/internal/app/app.go` рядом с
остальными. Затем:

Run: `cd backend && gofmt -l . && go vet ./... && go test ./internal/api/ ./internal/showcase/`
Run: `make test-db`
Run: `make api-contract` — если появился новый код ошибки, добавить тексты в
`frontend/lib/i18n/dictionaries/{en,ru,ro}.ts` и прогнать
`cd frontend && npm run error-codes`.

- [ ] **Step 10: Коммит**

```bash
git add backend/internal/showcase backend/internal/postgres/showcase.go backend/internal/postgres/showcase_test.go backend/internal/api/public_handler.go backend/internal/api/public_handler_test.go backend/internal/app/app.go
git commit -m "feat(api): two reads a visitor without a session may make"
```

---

### Task 2: Орнамент — токены и модуль

**Files:**
- Modify: `frontend/styles/tokens.css` (под-палитра «орнамент», светлая и тёмная)
- Modify: `frontend/app/globals.css` (вывести токены в `@theme`)
- Create: `frontend/components/product/ornament.tsx`
- Create: `frontend/components/product/ornament.test.tsx`
- Modify: `docs/design/SPEC.md` (новый подраздел про под-палитру, рядом с 3.5)

**Interfaces:**
- Produces:
  - `<OrnamentBand repeats?: number, className?: string />` — горизонтальная лента
  - `<OrnamentStar className?: string />` — восьмиконечная звезда, знак в герое

- [ ] **Step 1: Прочитать спеку и правила палитры**

Раздел 3 спеки главной; `docs/design/SPEC.md` разделы 3.1–3.5 (как описаны две
существующие под-палитры) — новая делается по тому же образцу.

- [ ] **Step 2: Добавить под-палитру**

В `frontend/styles/tokens.css`, рядом с существующими под-палитрами:

```css
  /* The ornament sub-palette: muted natural dyes of Moldovan embroidery —
     madder, walnut, indigo. One job, and it is decorative: these never carry
     "good", "bad" or "live", which have tokens of their own. Madder is muted
     deliberately — a pure red already means an error in this system, and two
     reds on one screen are two messages in one colour. */
  --ornament-madder: #9c4a3c;
  --ornament-walnut: #8a6b45;
  --ornament-indigo: #3f5a7a;
```

И в блоке тёмной темы — те же имена, светлее, как того требует правило про
удвоенную альфу и тёмный фон:

```css
    --ornament-madder: #c2705f;
    --ornament-walnut: #b08f66;
    --ornament-indigo: #6d89ac;
```

Вывести в `@theme` в `frontend/app/globals.css` тем же способом, что и
соседние токены, чтобы появились утилиты `text-ornament-madder` и подобные.

- [ ] **Step 3: Написать падающий тест модуля**

`frontend/components/product/ornament.test.tsx`:

```tsx
test("the band repeats its motif and says nothing to a screen reader", () => {
  const { container } = render(<OrnamentBand repeats={8} />);

  const svg = container.querySelector("svg");
  expect(svg).toHaveAttribute("aria-hidden", "true");
  expect(container.querySelectorAll("[data-motif]")).toHaveLength(8);
});

test("its strokes come from tokens, never from a literal colour", () => {
  const { container } = render(<OrnamentBand repeats={2} />);
  expect(container.innerHTML).not.toMatch(/#[0-9a-f]{3,6}/i);
});
```

- [ ] **Step 4: Запустить и убедиться, что падает**

Run: `cd frontend && npx vitest run components/product/ornament.test.tsx`
Expected: FAIL — модуля нет.

- [ ] **Step 5: Реализовать модуль**

Геометрия строится на квадратной сетке крестика. Единица — 8 единиц viewBox;
модуль шириной 24 несёт ромб между двумя «реками». Штрих — `currentColor`,
поэтому цвет задаётся классом-токеном снаружи и тема переключает его сама:

```tsx
/** One motif: a rhombus between two runs of the river zig-zag. */
function Motif({ x }: { x: number }) {
  return (
    <g data-motif transform={`translate(${x} 0)`}>
      {/* râuri — the river, a zig-zag across one row of the grid */}
      <path d="M0 4 L4 0 L8 4 L12 0 L16 4 L20 0 L24 4" />
      {/* romb — the rhombus, from the midpoints of a cell */}
      <path d="M8 12 L12 8 L16 12 L12 16 Z" />
      <path d="M0 20 L4 24 L8 20 L12 24 L16 20 L20 24 L24 20" />
    </g>
  );
}

export function OrnamentBand({ repeats = 24, className }: { repeats?: number; className?: string }) {
  return (
    <svg
      aria-hidden="true"
      viewBox={`0 0 ${repeats * 24} 24`}
      preserveAspectRatio="xMinYMid slice"
      fill="none"
      stroke="currentColor"
      strokeWidth={1}
      className={cn("h-6 w-full text-ornament-indigo", className)}
    >
      {Array.from({ length: repeats }, (_, i) => (
        <Motif key={i} x={i * 24} />
      ))}
    </svg>
  );
}
```

`preserveAspectRatio="xMinYMid slice"` — то самое, что обрезает ленту по краю
полосы, а не сжимает её: геометрия крестика, сжатая по горизонтали, перестаёт
быть геометрией крестика (требование спеки, раздел 3).

Звезда — восьмиконечная, два наложенных квадрата, в отдельном `<svg>` со своим
`viewBox="0 0 24 24"`, размером со строчную букву (`h-[0.72em] w-[0.72em]`).

Три цвета под-палитры распределяются так: лента под героем — индиго, лента
подвала — орех, звезда — марена. Один орнамент — один цвет; смешение в одной
ленте превращает узор в пестроту.

- [ ] **Step 6: Проверить, что тесты проходят**

Run: `cd frontend && npx vitest run components/product/ornament.test.tsx`
Expected: PASS.

- [ ] **Step 7: Записать решение в дизайн-спеку**

В `docs/design/SPEC.md` — подраздел про третью под-палитру: что это, где
живёт, три правила из раздела 3 спеки главной. По-русски.

- [ ] **Step 8: Коммит**

```bash
git add frontend/styles/tokens.css frontend/app/globals.css frontend/components/product/ornament.tsx frontend/components/product/ornament.test.tsx docs/design/SPEC.md
git commit -m "feat(design): the ornament, as a third named sub-palette"
```

---

### Task 3: Страница, герой и статические разделы

**Files:**
- Create: `frontend/app/(public)/page.tsx`
- Create: `frontend/app/(public)/home/hero.tsx`
- Create: `frontend/app/(public)/home/hero.test.tsx`
- Create: `frontend/app/(public)/home/how-it-works.tsx`
- Create: `frontend/app/(public)/home/organiser-line.tsx`
- Create: `frontend/app/(public)/home/site-footer.tsx`
- Create: `frontend/app/(public)/home/site-footer.test.tsx`
- Modify: `frontend/lib/auth/guard.ts` (корень — публичный путь)
- Modify: `frontend/lib/auth/guard.test.ts`
- Modify: `frontend/lib/i18n/dictionaries/{en,ru,ro}.ts` (секция `home`)

**Interfaces:**
- Consumes: `Band` (`@/components/layout/band`), `OrnamentBand`, `OrnamentStar`
  (Task 2), `branding()` (`@/lib/api/branding`), `activeDictionary`,
  `activeLocale` (`@/lib/i18n/server`), `hasSession` — как его читают соседние
  серверные компоненты.
- Produces: `<Hero name={string} signedIn={boolean} dict={Dictionary} />`,
  `<HowItWorks dict />`, `<OrganiserLine dict />`.

- [ ] **Step 1: Написать падающий тест на корень в страже**

В `frontend/lib/auth/guard.test.ts`:

```ts
test("the front page is open to a visitor without a session", () => {
  expect(guardRedirect("/", "", false)).toBeNull();
});

test("and it is still only the front page that is open", () => {
  expect(guardRedirect("/my", "", false)).toBe("/login?next=%2Fmy");
});
```

- [ ] **Step 2: Запустить, убедиться, что первый падает, добавить корень**

Run: `cd frontend && npx vitest run lib/auth/guard.test.ts`
Expected: FAIL на первом.

Затем в `frontend/lib/auth/guard.ts` добавить `"/"` в `PUBLIC_PATHS`. Проверить
в комментарии то, что делает это безопасным: сравнение идёт на точное
равенство, а `startsWith("//")` не совпадает ни с одним реальным путём,
поэтому корень не открывает всё дерево.

- [ ] **Step 3: Тест героя**

`frontend/app/(public)/home/hero.test.tsx`:

```tsx
test("offers the way in to a visitor, and the way to work to an account", () => {
  const { rerender } = render(<Hero name="Olymp Database System" signedIn={false} dict={en} />);
  expect(screen.getByRole("link", { name: en.home.hero.signIn })).toHaveAttribute("href", "/login");

  rerender(<Hero name="Olymp Database System" signedIn dict={en} />);
  expect(screen.getByRole("link", { name: en.home.hero.mine })).toHaveAttribute("href", "/my");
});

test("sends a visitor to the contests on this page, not to a catalogue behind sign-in", () => {
  render(<Hero name="X" signedIn={false} dict={en} />);
  expect(screen.getByRole("link", { name: en.home.hero.browse })).toHaveAttribute("href", "#contests");
});
```

- [ ] **Step 4: Запустить, убедиться, что падает, реализовать герой**

Имя приходит снаружи: `branding()` отдаёт имя установки, а страница подставляет
`"Olymp Database System"`, когда его нет. Ступень — `text-display`; звезда
орнамента слева от имени размером со строчную букву.

- [ ] **Step 5: Три пункта и строка организатора**

`how-it-works.tsx` — три пункта в ряд, разделённые вертикальными линейками, на
узком экране в столбец. Никаких иконок и карточек. `organiser-line.tsx` — одна
фраза и ссылка на вход.

- [ ] **Step 6: Подвал**

Подвала в проекте пока нет — этот первый. `site-footer.tsx`: имя установки и
контакт из настроек (`branding()` отдаёт и то и другое), переключатели языка и
темы — те же компоненты, что стоят в `AppBar`
(`components/layout/language-switcher.tsx`, `theme-toggle.tsx`), и узкая лента
орнамента. Тест:

```tsx
test("names the installation and offers the same language and theme controls as the bar", () => {
  render(<SiteFooter name="Olymp Database System" contact="admin@example.edu" locale="en" theme="system" dict={en} />);

  expect(screen.getByText("Olymp Database System")).toBeInTheDocument();
  expect(screen.getByRole("link", { name: /admin@example\.edu/ })).toHaveAttribute("href", "mailto:admin@example.edu");
  expect(screen.getByRole("button", { name: en.chrome.language })).toBeInTheDocument();
});

test("keeps quiet about a contact the installation never set", () => {
  render(<SiteFooter name="X" contact="" locale="en" theme="system" dict={en} />);
  expect(screen.queryByRole("link", { name: /mailto/ })).toBeNull();
});
```

- [ ] **Step 7: Собрать страницу**

`frontend/app/(public)/page.tsx` — серверный компонент: читает `branding()`,
словарь, локаль, тему и признак сессии, раскладывает разделы в `Band`, между
героем и числами ставит `<OrnamentBand />`, в конце — `<SiteFooter />`.

- [ ] **Step 8: Тексты в трёх словарях**

Секция `home` в `en`, `ru`, `ro`: заголовок по умолчанию, описание, подписи
действий, три пункта, строка организатора. Тексты пишутся под смысл, а не
переводятся дословно.

- [ ] **Step 9: Проверки и коммит**

Run: `cd frontend && npx vitest run "app/(public)" lib/auth && npm run typecheck`

```bash
git add "frontend/app/(public)/page.tsx" "frontend/app/(public)/home" frontend/lib/auth/guard.ts frontend/lib/auth/guard.test.ts frontend/lib/i18n/dictionaries/en.ts frontend/lib/i18n/dictionaries/ru.ts frontend/lib/i18n/dictionaries/ro.ts
git commit -m "feat(home): a front page that says what this is"
```

---

### Task 4: Числа и список олимпиад на странице

**Files:**
- Create: `frontend/lib/api/showcase.ts` (схемы zod + чтения)
- Create: `frontend/app/(public)/home/numbers.tsx`
- Create: `frontend/app/(public)/home/numbers.test.tsx`
- Create: `frontend/app/(public)/home/recent-contests.tsx`
- Create: `frontend/app/(public)/home/recent-contests.test.tsx`
- Modify: `frontend/app/(public)/page.tsx`
- Modify: `frontend/lib/i18n/dictionaries/{en,ru,ro}.ts`

**Interfaces:**
- Consumes: `GET /public/stats`, `GET /public/contests` (Task 1),
  `serverRequest` (`@/lib/api/server`).
- Produces: `publicStatsSchema`, `publicContestsSchema`, типы `PublicStats`,
  `PublicContest`; `<Numbers stats={PublicStats | null} dict />`,
  `<RecentContests contests={PublicContest[] | null} dict locale />`.

- [ ] **Step 1: Тест на то, что неудавшееся чтение убирает строку целиком**

```tsx
test("a failed read costs the strip, not the page", () => {
  const { container } = render(<Numbers stats={null} dict={en} />);
  expect(container).toBeEmptyDOMElement();
});

test("shows what the installation has done, in mono figures", () => {
  render(<Numbers stats={{ contests: 12, participants: 340, queries: 91_244, solved: 1_108 }} dict={en} />);
  expect(screen.getByText("91244")).toHaveClass(/tabular-nums/);
});
```

Витрина без цифр — витрина; витрина с нулями — сломанная система (спека, 2.3).

- [ ] **Step 2: Запустить, убедиться, что падает, реализовать**

Числа — моноширинным, число над подписью, на узком экране в два ряда. Тот же
приём, что в `frontend/app/(session)/profile/summary.tsx` — посмотреть его и
повторить, а не изобретать.

- [ ] **Step 3: Тест списка олимпиад**

Строки на линейках, свежие сверху, не больше шести; у идущей — акцентная точка;
ссылка на публичную таблицу только когда `tableOpen`; пустое состояние —
объяснение и ссылка, а не пустая таблица.

```tsx
test("leads to the public table only where there is one to read", () => {
  render(<RecentContests contests={[running({ tableOpen: false })]} dict={en} locale="en" />);
  expect(screen.queryByRole("link", { name: en.home.contests.table })).toBeNull();
});
```

- [ ] **Step 4: Реализовать список и подключить к странице**

В `page.tsx` читать оба маршрута через `serverRequest` с `Promise.allSettled`:
одно упавшее чтение не должно забирать страницу. Якорь `id="contests"` — на
разделе со списком (герой ссылается на него).

- [ ] **Step 5: Проверки и коммит**

Run: `cd frontend && npx vitest run "app/(public)" && npm run typecheck`

```bash
git add frontend/lib/api/showcase.ts "frontend/app/(public)/home/numbers.tsx" "frontend/app/(public)/home/numbers.test.tsx" "frontend/app/(public)/home/recent-contests.tsx" "frontend/app/(public)/home/recent-contests.test.tsx" "frontend/app/(public)/page.tsx" frontend/lib/i18n/dictionaries/en.ts frontend/lib/i18n/dictionaries/ru.ts frontend/lib/i18n/dictionaries/ro.ts
git commit -m "feat(home): the installation's own numbers and its contests"
```

---

### Task 5: Превью консоли

**Files:**
- Create: `frontend/app/(public)/home/console-preview.tsx`
- Create: `frontend/app/(public)/home/console-preview.test.tsx`
- Modify: `frontend/app/(public)/page.tsx`

**Interfaces:**
- Consumes: существующая подсветка SQL — найти её в `components/product/`
  (`sql-block.tsx`) и использовать её же.
- Produces: `<ConsolePreview dict />`.

**Отступление от спеки, сделанное сознательно:** спека говорит «кадр продукта».
Снимок экрана требует поднятого стека и превращается в файл, который устаревает
при первой же правке консоли и существует в двух темах. Вместо него —
статичная, некликабельная реплика консоли из тех же компонентов, что и сама
консоль: она честна по построению, переключает тему сама и не добавляет
бинарного файла в репозиторий. Если контролёр не согласен — вернуть снимок.

- [ ] **Step 1: Тест**

```tsx
test("shows the product without offering to be used", () => {
  render(<ConsolePreview dict={en} />);
  expect(screen.queryByRole("button")).toBeNull();
  expect(screen.queryByRole("textbox")).toBeNull();
  expect(screen.getByText(/SELECT/)).toBeInTheDocument();
});
```

- [ ] **Step 2: Запустить, убедиться, что падает, реализовать**

Запрос в примере — из игровой базы детективного сюжета, короткий и читаемый,
с результатом в три-четыре строки под ним. Тонкая линейка вместо тени. На
узком экране (375) блок не показывается вовсе.

- [ ] **Step 3: Проверки и коммит**

Run: `cd frontend && npx vitest run "app/(public)" && npm run typecheck`

```bash
git add "frontend/app/(public)/home/console-preview.tsx" "frontend/app/(public)/home/console-preview.test.tsx" "frontend/app/(public)/page.tsx"
git commit -m "feat(home): the console, shown rather than described"
```

---

### Task 6: Документация и полная проверка

**Files:**
- Modify: `docs/ARCHITECTURE.md` (новый подраздел про главную и два публичных чтения)
- Modify: `docs/design/SPEC.md` (раздел про экран главной)

- [ ] **Step 1: Архитектура**

Подраздел рядом с разделом 9: что показывает главная, откуда берутся числа
(`registration_activity`, а не `count(*)` по журналу, и почему), два маршрута
под `/public/`, бюджет частоты по адресу, кэш на минуту. Плюс строка в
разделе 15. По-русски.

- [ ] **Step 2: Дизайн-спека**

Раздел про экран главной: разделы страницы, где живёт орнамент, поведение на
четырёх ширинах, правило движения. По-русски.

- [ ] **Step 3: Полная проверка**

Run: `cd backend && gofmt -l . && go vet ./... && go test ./...`
Run: `make test-db`
Run: `make api-contract` (дрейфа быть не должно)
Run: `make sec`
Run: `make front-check`

- [ ] **Step 4: Коммит**

```bash
git add docs/ARCHITECTURE.md docs/design/SPEC.md
git commit -m "docs(home): the front page and where its numbers come from"
```

- [ ] **Step 5: Проверка в браузере — её делает контроллер**

1440, 1024, 768 и 375 px, светлая и тёмная тема: герой не переносится по
слогам, цифры перестраиваются в два ряда, орнамент обрезается по краю и не
сжимается, страница не едет вбок.
