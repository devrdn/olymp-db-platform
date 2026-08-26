# DB Contest — interface

The participant and administrator interface: Next.js 16 (App Router), React 19,
Tailwind v4, TypeScript.

The visual system it implements is specified in
[`docs/design/SPEC.md`](../docs/design/SPEC.md); the product it serves is
specified in [`docs/ARCHITECTURE.md`](../docs/ARCHITECTURE.md). This file is
about the code: where things are, and where a new thing goes.

## Running it

```bash
npm install
npm run dev
```

The interface needs the Core API. Bring the stack up from `deploy/` first, or
run the Go service on `:8080`, which is where a development build looks by
default.

| Script | What it does |
|---|---|
| `npm run dev` | development server on `:3000` |
| `npm test` | unit and component tests (Vitest) |
| `npm run typecheck` | `tsc --noEmit` |
| `npm run lint` | ESLint, including the three design-system rules |
| `npm run contrast` | every token pair against its WCAG threshold, in both themes |
| `npm run error-codes` | every API error code has a message in the dictionary |
| `npm run build` | production build |

CI runs all of them on every pull request (`.github/workflows/frontend.yml`).

## Environment

| Variable | Required | Meaning |
|---|---|---|
| `API_ORIGIN` | in production | where the Next **server** dials the Core API, e.g. `http://api:8080` |

Copy `.env.example` to `.env.local` for a local override; Next reads it
automatically and it is git-ignored. In the deployed stack the value comes from
`deploy/.env`, which `docker-compose.yml` passes through.

There are no `NEXT_PUBLIC_` variables, and adding one should be a decision
rather than a reflex: those are inlined into the browser bundle at build time,
so they are both public and frozen into the image.

`API_ORIGIN` is the private address of the API on the internal network, not the
public one. The browser never uses it: a page request is same-origin through
Caddy, which is what makes the `SameSite=Lax` session cookie sufficient. In
production a missing value is a startup error rather than a guess, because a
guessed address turns a broken deployment into a page that says "server
unreachable" and names no cause.

## The map

```
app/                 routes; a folder is a URL
  layout.tsx           <html>, fonts, lang, data-theme, dictionary provider
  error.tsx            the last boundary before the framework's own
  not-found.tsx        404
  global-error.tsx     the failure that took the layout with it
  login/               page, sign-in-form, actions.ts
  contests/            page, layout, loading, error, contest-register

components/
  ui/                  primitives with no domain knowledge: Button, Input, Field, Tag, Skeleton
  product/             domain components more than one route uses: StateView
  layout/              the frame: Band, AppBar, shells, the language and theme switchers

lib/
  api/                 transport, wire schemas, the API's address
  auth/                session, guards, sign-in, where an account lands
  i18n/                dictionaries, locale resolution, error messages
  theme/               the theme cookie and its attribute
  format/              dates and numbers by locale
  utils.ts             cn(), which also teaches tailwind-merge our type scale

scripts/               the checks CI runs
styles/tokens.css      the single source of every colour, size, duration and radius
proxy.ts               route protection (Next 16 calls this middleware "proxy")
```

**A route owns what only it uses.** Its page, the components that page renders
and the Server Actions it submits to live in the route folder. `login/` has an
`actions.ts` because signing in is a mutation; `contests/` has none yet because
listing is a read. When a second route reaches for a component, that is the
moment it moves to `components/product/` — one route, keep it; two, move it.

**A test sits next to its source.** `foo.tsx` is tested by `foo.test.tsx`. A
mirrored `__tests__` tree has to be kept in step by hand, and renaming a file
becomes a two-file operation whose second half is easy to forget; both Vitest
and the Next testing guide colocate by default. End-to-end tests are the
exception and will get their own top-level directory when they arrive, because
they belong to a journey rather than to a file.

## Which direction things point

```
app/  →  components/  →  lib/
```

Nothing in `lib/` imports a component. Nothing in `components/ui/` knows the
domain: a primitive that mentions a contest belongs in `components/product/`.
Within `lib/`, `api` is the lowest layer and does not import `auth`, `i18n` or
`theme`; `api/server.ts` is the one crossing, and deliberately, because an
authenticated request needs both.

## Where does a new thing go?

**A page.** A folder under `app/`. If it needs a session, it needs nothing extra:
`proxy.ts` already redirects a visitor without one, and `serverRequest` already
carries the cookie. Give it `loading.tsx` if the wait is visible, and an
`error.tsx` only if the section's failure differs from the root one.

**A component.** Rendered by one route? Put it in that route's folder. Reached
by a second? Move it to `components/product/`. Chrome the whole app wears?
`components/layout/`. No domain knowledge at all? `components/ui/`. Primitives
take their sizing from `--control-h` and their colour from tokens, never from a
literal.

**A string.** `lib/i18n/dictionaries/en.ts` first: its shape is the type the
other locales must satisfy, so adding a key there makes the build fail until
`ro.ts` and `ru.ts` have it too. Components take a `dict`; they hold no copy of
their own.

**A language.** A file in `lib/i18n/dictionaries/` and a code in
`lib/i18n/config.ts`. No component changes — the same rule the server follows by
keeping languages in a table rather than an enum.

**A colour, size, duration or radius.** `styles/tokens.css`, then a utility in
the `@theme` block of `app/globals.css`. Writing one inline is an ESLint error,
and that is the point: a design system nothing enforces is a document, not a
system.

**An API call.** A schema in `lib/api/`, parsed at the boundary with Zod, and a
`serverRequest` from a Server Component. Parsing where the data arrives means a
contract change surfaces with a field name instead of as `undefined` three
components later.

**An error code.** The Go side adds it; `npm run error-codes` then fails until
all three dictionaries have a message. That check exists because the two sides
drifted silently once already.

## Things worth knowing before changing them

**The theme and the language are cookies the server reads.** Both are resolved
before the first byte of HTML, which is why there is no flash and no blocking
inline script. `localStorage` cannot do this: it arrives after the page is
built, and after the API has been asked for content in the wrong language.

**The session is httpOnly.** `proxy.ts` can only see that the cookie exists; the
API decides whether it is still worth anything. A stale one comes back as
`unauthenticated` and is sent to the sign-in form rather than to the recoverable
error screen, whose retry could never work.

**There is no weight 700 and no arbitrary size.** Tailwind's stock `--text-*`,
`--radius-*` and `--tracking-*` scales are erased in `globals.css`, so a size can
only be picked from the system. `cn()` teaches tailwind-merge the replacement
scale; without that a custom step is mistaken for a colour and silently dropped.

**`error.tsx` is a Client Component and can never await a dictionary.** That is
why the provider sits in the root layout.

## Known next steps

- **A nonce-based CSP.** The production policy currently allows
  `script-src 'unsafe-inline'` for Next's bootstrap. The stricter shape mints a
  nonce per request in `proxy.ts` and pairs it with `strict-dynamic`. Worth doing
  once the interface is actually deployed and the policy can be verified against
  a real page.
- **The `/design` showcase.** Specified in section 12 of the design spec and
  deliberately absent: it needs contests to show, and invented data does not
  belong in the repository. It returns with the constructor.
