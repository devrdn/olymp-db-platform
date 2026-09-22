# Обложка олимпиады — план реализации

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** у олимпиады появляется обложка: организатор загружает снимок, сервер
обрезает его до `16/9`, уменьшает, перекодирует и кладёт в объектное
хранилище; витрина показывает карточки, а у олимпиады без снимка — рисованная
обложка, выведенная из её идентификатора.

**Architecture:** доменный пакет `internal/covers` держит правила (пределы,
кадрирование, перекодирование) и объявляет два порта — хранилище объектов и
репозиторий. Реализация хранилища — MinIO по протоколу S3 в
`internal/platform/objects`, реализация репозитория — в `internal/postgres`.
Байты отдаёт API, а не хранилище: один источник для браузера и заголовки кэша
наши.

**Tech Stack:** Go (chi, pgx, `golang.org/x/image` — уже в зависимостях,
minio-go), MinIO в docker compose, Next 16, vitest.

**Spec:** `docs/superpowers/specs/2026-09-22-contest-covers-design.md` — каждая
задача читает его целиком перед началом.

## Global Constraints

- `CLAUDE.md` в корне обязателен целиком. Особенно: правило 1 (отказ —
  объявленный сентинел, маппинг в `fail`, тест обработчика, код в
  `docs/api/error-codes.json`, тексты en/ru/ro), правило 2 (явные пределы),
  правило 12 (память ограничивается там, где приходят байты), правило 13
  (ограничитель до дорогой работы, отказы считаются), раскладка Go
  (`internal/platform/*` не импортирует доменные пакеты; интерфейсы объявляет
  потребитель), английский в коде и коммитах, русский в `docs/`.
- **Пределы — дословно:** запрос ≤ 8 МиБ; исходник ≤ 8000 × 8000 пикселей;
  типы только JPEG, PNG, WebP (определяются по байтам); выход — два JPEG
  качества 82, `1600×900` и `800×450`; ключ объекта
  `covers/<sha256>-<width>.jpg`.
- **SVG не принимается ни под каким видом.**
- `image.DecodeConfig` вызывается **до** полного декодирования, и отказ по
  размерам происходит там.
- Недостижимое хранилище — ошибка при старте, как недостижимый Redis.
- Реальные данные не трогать: только `dbcontest_core_test` и тестовый бакет.
  Никаких `make run`, `make runner`, `make dev-*` и docker-команд, которые
  останавливают или удаляют контейнеры, которые исполнитель не создавал.
- В индекс добавлять файлы явно по путям; `git add -A` запрещён.

---

### Task 1: Хранилище объектов

**Files:**
- Create: `backend/internal/platform/objects/objects.go`
- Create: `backend/internal/platform/objects/objects_test.go`
- Modify: `backend/internal/platform/config/config.go` (+ его тест)
- Modify: `backend/internal/app/app.go` (сборка и проверка готовности)
- Modify: `deploy/docker-compose.yml`, `deploy/docker-compose.dev.yml`, `deploy/.env.example`
- Modify: `Makefile` (бэкап тома), `backend/README.md`

**Interfaces:**
- Produces: `objects.Store` с методами
  `Put(ctx, key string, contentType string, body []byte) error`,
  `Get(ctx, key string) ([]byte, string, error)`,
  `Delete(ctx, key string) error`,
  `Ping(ctx) error`; `objects.ErrNotFound`.
- Consumes: `config.Config.ObjectStore{Endpoint, AccessKey, SecretKey, Bucket, UseTLS}`.

- [ ] **Step 1: Прочитать спеку и образцы**

Разделы 1 и 4 спеки. Образец инфраструктурного пакета с проверкой готовности —
`backend/internal/platform/cache` (`NewRedis`, `Ping`, фатальность
недостижимого сервера). Образец настроек — как читается `REDIS_ADDR` в
`config.go`.

- [ ] **Step 2: Падающий тест порта**

`objects_test.go`, против настоящего MinIO из dev-оверлея; пропускается, когда
адрес не задан (как `storagetest` пропускает без `CORE_DB_DSN`):

```go
func TestPutThenGetReturnsTheSameBytes(t *testing.T) {
	store := testStore(t) // t.Skip when OBJECT_STORE_ENDPOINT is unset
	key := "covers/" + t.Name() + ".jpg"
	t.Cleanup(func() { _ = store.Delete(context.Background(), key) })

	if err := store.Put(context.Background(), key, "image/jpeg", []byte("not really a jpeg")); err != nil {
		t.Fatalf("Put() = %v", err)
	}

	body, contentType, err := store.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("Get() = %v", err)
	}
	if string(body) != "not really a jpeg" || contentType != "image/jpeg" {
		t.Errorf("Get() = %q, %q", body, contentType)
	}
}

func TestGettingWhatIsNotThereIsNotFound(t *testing.T) {
	store := testStore(t)
	if _, _, err := store.Get(context.Background(), "covers/absent"); !errors.Is(err, objects.ErrNotFound) {
		t.Errorf("Get() = %v, want ErrNotFound", err)
	}
}
```

- [ ] **Step 3: Запустить, убедиться, что падает, реализовать**

Run: `cd backend && go test ./internal/platform/objects/`
Expected: FAIL — пакета нет.

Реализация поверх `minio-go`: клиент, проверка существования бакета при
старте (создать, если нет), `Ping` через `BucketExists`.

- [ ] **Step 4: Настройки и старт**

`OBJECT_STORE_ENDPOINT`, `OBJECT_STORE_ACCESS_KEY`, `OBJECT_STORE_SECRET_KEY`,
`OBJECT_STORE_BUCKET` (по умолчанию `dbcontest`), `OBJECT_STORE_TLS`.
Пустой адрес — ошибка старта, а не тихий пропуск: обложки не опциональная
часть витрины. Проверка добавляется в `/readyz` рядом с базой и кэшем.

- [ ] **Step 5: Сервис в compose**

`minio` рядом с `redis`: образ `minio/minio`, команда `server /data
--console-address :9001`, том `minio-data`, healthcheck по
`/minio/health/live`, пароли из `.env`. API зависит от него по
`service_healthy`. В dev-оверлее — проброс портов на `127.0.0.1`.

**И бэкап:** `make backup` дампит базу; том MinIO обязан попасть в копию.
Добавить в тот же таргет и описать в `backend/README.md`, иначе восстановление
вернёт олимпиады без обложек.

- [ ] **Step 6: Проверки и коммит**

Run: `cd backend && gofmt -l . && go vet ./... && go test ./internal/platform/...`
Run: `docker compose -f deploy/docker-compose.yml --env-file <временный> config` — только проверка синтаксиса, ничего не поднимать.

```bash
git add backend/internal/platform/objects backend/internal/platform/config backend/internal/app/app.go deploy Makefile backend/README.md
git commit -m "feat(objects): the store the covers live in"
```

---

### Task 2: Приём, обработка и отдача обложки

**Files:**
- Create: `backend/migrations/000038_contest_covers.{up,down}.sql`
- Create: `backend/internal/covers/covers.go`, `covers_test.go`
- Create: `backend/internal/covers/image.go`, `image_test.go`
- Create: `backend/internal/postgres/covers.go`, `covers_test.go`
- Create: `backend/internal/api/cover_handler.go`, `cover_handler_test.go`
- Modify: `backend/internal/contests/publish.go` (атрибуция в шлюзе), `backend/internal/app/app.go`, `docs/api/error-codes.json`, три словаря

**Interfaces:**
- Produces: `covers.Service.Upload(ctx, contestID uuid.UUID, actorID uuid.UUID, src io.Reader, attribution string) (covers.Cover, error)`,
  `covers.Service.Read(ctx, hash string, size int) ([]byte, string, error)`,
  `covers.Cover{ContestID, Hash string, Attribution string, Width, Height int}`,
  `covers.MaxUploadBytes = 8 << 20`, `covers.MaxSourcePixels = 8000`,
  `covers.Sizes = []int{1600, 800}`.

- [ ] **Step 1: Падающий тест обработки изображения**

`image_test.go` — самая важная часть задачи:

```go
func TestASourceTooLargeIsRefusedBeforeItIsDecoded(t *testing.T) {
	// A 100-byte PNG header claiming 30000x30000: decoding it would be 3.6 GB.
	src := pngHeaderClaiming(t, 30000, 30000)

	_, err := covers.Process(bytes.NewReader(src))

	if !errors.Is(err, covers.ErrImageTooLarge) {
		t.Fatalf("Process() = %v, want ErrImageTooLarge", err)
	}
}

func TestAnSVGIsNotAnImageHere(t *testing.T) {
	_, err := covers.Process(strings.NewReader(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`))
	if !errors.Is(err, covers.ErrImageKind) {
		t.Fatalf("Process() = %v, want ErrImageKind", err)
	}
}

func TestTheOutputCarriesNothingOfTheInputButThePicture(t *testing.T) {
	// A JPEG with an EXIF comment; the re-encoded output must not contain it.
	src := jpegWithComment(t, "Taken at 47.0105, 28.8638")

	out, err := covers.Process(bytes.NewReader(src))
	if err != nil {
		t.Fatalf("Process() = %v", err)
	}

	for _, size := range out.Renditions {
		if bytes.Contains(size.Bytes, []byte("47.0105")) {
			t.Error("the author's location survived into the file we serve")
		}
	}
}

func TestTheOutputIsSixteenByNine(t *testing.T) {
	out, err := covers.Process(bytes.NewReader(jpegOf(t, 1000, 1000)))
	if err != nil {
		t.Fatalf("Process() = %v", err)
	}
	if out.Renditions[0].Width != 1600 || out.Renditions[0].Height != 900 {
		t.Errorf("first rendition is %dx%d, want 1600x900", out.Renditions[0].Width, out.Renditions[0].Height)
	}
}
```

- [ ] **Step 2: Запустить, убедиться, что падает, реализовать обработку**

Run: `cd backend && go test ./internal/covers/`

Порядок внутри `Process`, и он же порядок защиты:
1. читать не больше `MaxUploadBytes + 1` байт (`io.LimitReader`) — правило 12;
2. определить тип по байтам (`http.DetectContentType` плюс явный список);
3. `image.DecodeConfig` — отказ по размерам **до** `image.Decode`;
4. декодировать, кадрировать по центру до `16/9`, уменьшить
   (`golang.org/x/image/draw`, `draw.CatmullRom`);
5. закодировать в JPEG качества 82 — на выходе файл, который написали мы.

- [ ] **Step 3: Миграция**

`contest_covers`: `contest_id uuid PRIMARY KEY REFERENCES contests ON DELETE
CASCADE`, `hash text NOT NULL`, `attribution text NOT NULL`, `width`,
`height`, `uploaded_at`, `uploaded_by uuid REFERENCES users ON DELETE SET
NULL`. Файл открывается `SET lock_timeout = '5s';` — соглашение из
`migrations/migrations_test.go`.

- [ ] **Step 4: Маршруты**

- `PUT /contests/{id}/cover` — право `contest.edit`, бюджет частоты на
  аккаунт до чтения тела, `multipart/form-data` с полями `file` и
  `attribution`. Отказы: `cover_too_large`, `cover_kind`,
  `cover_dimensions`, `cover_attribution_required`.
- `DELETE /contests/{id}/cover`.
- `GET /public/contests/{id}/cover?size=800` — публичный, отдаёт байты из
  хранилища, `Cache-Control: public, max-age=31536000, immutable`, `ETag` —
  хеш.

Каждый отказ — по правилу 1 целиком.

- [ ] **Step 5: Шлюз публикации**

Обложка без атрибуции не публикуется (раздел 10.1 дизайн-спеки): новый код
проблемы `cover_needs_attribution` в `publish.go`, тест, тексты в трёх
словарях.

- [ ] **Step 6: Проверки и коммит**

Run: `cd backend && gofmt -l . && go vet ./... && go test ./internal/covers/ ./internal/api/`
Run: `make test-db`
Run: `make api-contract` и `cd frontend && npm run error-codes`

---

### Task 3: Экран организатора

**Files:**
- Create: `frontend/app/(admin)/contests/[contestId]/settings/cover-panel.tsx` (+ тест)
- Modify: страница настроек олимпиады, `frontend/lib/api/contests.ts`, три словаря

- [ ] **Step 1: Падающий тест**

Панель показывает текущую обложку или рисованную; выбор файла; поле атрибуции
обязательно, когда файл выбран; кнопка удаления; отказы сервера
показываются рядом с полем, а не поверх страницы.

- [ ] **Step 2: Реализовать и проверить**

Run: `cd frontend && npx vitest run "app/(admin)/contests" && npm run typecheck`

---

### Task 4: Карточки на витрине и рисованная обложка

**Files:**
- Create: `frontend/components/product/drawn-cover.tsx` (+ тест)
- Modify: `frontend/app/(public)/home/recent-contests.tsx` (+ тест), `frontend/lib/api/showcase.ts`
- Modify: `backend/internal/api/public_handler.go` (+ тест) — список несёт хеш обложки
- Modify: три словаря

- [ ] **Step 1: Рисованная обложка**

Детерминирована по идентификатору олимпиады: из `id` берётся число, оно
выбирает угол градиента и расположение геометрии. Только существующие токены.
Тест: один и тот же `id` даёт одинаковую разметку дважды; разные `id` —
разную.

- [ ] **Step 2: Карточки**

Сетка: три в ряд на широком, две на планшете, одна на телефоне. Обложка
`16/9`, `object-fit: cover`, `loading="lazy"`, явные `width`/`height`.
Скрим `--scrim-a` → `--scrim-b` → прозрачный, на нём название. Под карточкой —
статус, даты, ссылка на таблицу там, где она открыта; атрибуция мелким, когда
обложка загруженная.

- [ ] **Step 3: Проверки и коммит**

Run: `cd frontend && npx vitest run "app/(public)" components/product && npm run typecheck && npm run lint`

---

### Task 5: Обложка над историей олимпиады

**Files:**
- Modify: `frontend/app/(participant)/contests/[contestId]/play/page.tsx` и компонент истории (+ тесты)

То, ради чего писался раздел 10 дизайн-спеки: та же обложка большого размера
над текстом истории, тот же скрим, тот же заголовок. Без обложки — рисованная,
ни одного сдвига в раскладке.

---

### Task 6: Документация и полная проверка

- [ ] `docs/ARCHITECTURE.md`: обложки, хранилище, порт и шов, бэкап тома, строка в разделе 15.
- [ ] `docs/design/SPEC.md`: раздел 10 — адресное исключение (изображений нет в реестре организатора; они есть на витрине и над историей).
- [ ] Полная проверка: `go test ./...`, `make test-db`, `make test-game`, `make api-contract`, `make sec`, `make front-check`.
- [ ] Проверка в браузере на 1440, 1024, 768, 375 — её делает контроллер.
