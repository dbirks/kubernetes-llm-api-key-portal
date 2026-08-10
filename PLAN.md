# Implementation Plan — Go portal

Companion to `ai-birks-portal-implementation-brief.md`. The brief is the *what*; this is the
*how, in what order, and who owns which files*.

Two agents are working on this repo:

- **Backend (this plan):** Go server, auth, keystore, config, branding, tests, Docker, CI, README.
- **UI agent (separate):** `web/templates/*.html` and `web/static/app.css` + `web/static/app.js`.

Section 8 is the frozen contract between the two. Everything the UI agent needs is there;
everything else in this document is backend-internal.

---

## 1. Scope decisions

| Decision | Choice | Why |
|---|---|---|
| Module path | `github.com/dbirks/kubernetes-llm-api-key-portal` | Matches repo name; rename is one `go mod edit` if the repo moves. |
| Binary | `cmd/ai-account` | As specified in brief §16. |
| Image | `ghcr.io/dbirks/kubernetes-llm-api-key-portal` | Brief §25. |
| Go version | 1.26 (toolchain pinned in `go.mod`) | Gets stdlib CSRF and `crypto/rand.Text`. |
| Router | stdlib `http.ServeMux` (method + wildcard patterns) | No router dependency needed since Go 1.22. |

**Confirm before I start:** module path and image name. Everything else follows the brief.

---

## 2. Dependencies

Deliberately short. Each one earns its place.

| Module | Used for | Notes |
|---|---|---|
| `github.com/coreos/go-oidc/v3` | OIDC discovery, JWKS refresh, ID-token verification | Brief §5 forbids hand-rolling these. |
| `golang.org/x/oauth2` | Authorization-code exchange | Companion to the above. |
| `k8s.io/client-go`, `k8s.io/api`, `k8s.io/apimachinery` | Secret CRUD | Heavy but unavoidable; only imported by `internal/keystore/kubernetes`. |

**Not** taken as dependencies, because the stdlib now covers them:

- **CSRF** → `net/http.CrossOriginProtection` (Go 1.25+). Rejects non-safe cross-origin requests
  via `Sec-Fetch-Site`/`Origin`. This removes per-form hidden tokens entirely, which also removes
  a whole class of "the token expired, please retry" UX. `AddTrustedOrigin(PUBLIC_BASE_URL)` and
  nothing else. Since the login POST goes *out* to Microsoft rather than in, no bypass patterns
  are needed. I'll add a test asserting a cross-site `POST /keys` is rejected.
- **Session cookie** → `internal/auth/session.go`, ~80 lines: HKDF-SHA256 (`crypto/hkdf`) to derive
  separate encryption/MAC material from `SESSION_KEY`, then AES-256-GCM seal of a JSON payload.
  This is `Seal`/`Open` with a random nonce, not novel cryptography, and it keeps
  `gorilla/securecookie` out of the tree. Key rotation supported by accepting a comma-separated
  `SESSION_KEY` list and sealing with the first.
- **Random key material** → `crypto/rand.Read` into 32 bytes, base64url-unpadded. (`rand.Text` is
  only 128 bits, below the brief's 256-bit floor, so it's used for the opaque client-id suffix
  and OAuth state/nonce, not for the credential itself.)

---

## 3. Repo layout

Follows brief §16, with `internal/brand` added.

```
cmd/ai-account/main.go          wiring, signal handling, graceful shutdown
internal/config/                env parsing, validation, fail-fast
internal/brand/                 white-label name/logo/color  (§5 below)
internal/auth/
  oidc.go                       provider discovery, login, callback
  session.go                    AES-GCM sealed cookie
  middleware.go                 RequireUser, session load
  fake.go                       dev/test authenticator (build-gated, off by default)
internal/keystore/
  keystore.go                   KeyStore interface, Owner/KeyMetadata/CreatedKey, name validation
  generate.go                   credential generation, suffix, opaque IDs
  memory/                       in-memory impl
  kubernetes/                   client-go impl, Secret shape, owner-label filtering
internal/onboarding/
  clients.go                    ClientGuide data for the 5 agents
  snippets.go                   template rendering of snippets
  testdata/                     golden files
internal/httpapp/
  routes.go  handlers.go  middleware.go  viewmodels.go  render.go  errors.go
web/templates/                  UI agent owns
web/static/                     UI agent owns (app.css, app.js)
Dockerfile  README.md  .github/workflows/
```

`web/` is embedded with `//go:embed`, with a `-tags devassets` variant that reads from disk so the
UI agent gets edit-and-refresh without rebuilding.

---

## 4. Milestones

Ordered so the UI agent is unblocked on day one.

### M0 — Scaffold + UI seam (do first, ~half a day)

Goal: `go run ./cmd/ai-account` serves every page with realistic fake data, no Entra, no cluster.

- `go.mod`, repo skeleton, embedded assets with the `devassets` live-reload tag.
- All view model types (§8) with fields final.
- Memory keystore pre-seeded with 2–3 fake keys when `DEV_FAKE_AUTH=1`.
- Every route wired to a placeholder template so the UI agent can see real HTML immediately.
- `internal/brand` complete, including the generated-CSS route — the UI agent needs the CSS
  custom property names to exist before styling.
- Health endpoints.

Handoff to the UI agent happens at the end of M0.

### M1 — Key lifecycle on the memory store

- `KeyStore` interface + memory impl + generation/validation.
- `POST /keys` → one-time key page (direct response, not a redirect — brief §12).
- `POST /keys/{id}/revoke` with a GET confirmation step.
- Flash messages via a short-lived signed cookie (no server-side store).
- Security headers middleware incl. `no-store` on the one-time page.
- Handler tests with `httptest`.

### M2 — Entra OIDC

- Discovery against `https://login.microsoftonline.com/{tenant}/v2.0`.
- `GET /login` → state+nonce in short-lived `__Host-` cookies → redirect.
- `GET /auth/callback` → code exchange → ID-token verify → tenant check → session.
- `POST /logout` clears session only.
- `CrossOriginProtection` enabled.
- CSP finalized against the *real* login redirect (`form-action` must permit
  `https://login.microsoftonline.com`).
- Config fails fast: in non-dev mode, missing `ENTRA_*` or `SESSION_KEY` is a startup error.

### M3 — Kubernetes keystore

- client-go with in-cluster config; `KUBECONFIG` accepted only when explicitly opted in.
- Immutable Secret with the exact label/annotation shape from brief §7.
- List via label selector on `owner-tid` + `owner-oid`; revoke re-reads and compares both labels
  before deleting.
- Tests against `k8s.io/client-go/kubernetes/fake` asserting the generated object shape.
- `/readyz` performs a cheap authorization/list check.

### M4 — Onboarding snippets

Verify all five against current upstream docs (brief §32) before freezing goldens:
Claude Code, Pi, OpenCode, Codex, Crush. Golden files in `internal/onboarding/testdata/`,
refreshable with `go test ./internal/onboarding -update`.

Two snippet variants per client: one with `YOUR_API_KEY` placeholder (account page) and one
pre-filled (one-time key page only, never persisted or logged).

### M5 — Delivery

- Multi-stage Dockerfile, distroless/static base, non-root, CA certs, no shell.
- CI: `go test`, `go vet`, `staticcheck`, `govulncheck` on PRs; build/push amd64+arm64 with SHA
  tags on main. Renovate config.
- README (§9 below).
- Security checklist walkthrough (brief §28).

### M6 — UI merge

Drop in the UI agent's templates/CSS/JS, resolve any view-model gaps, re-run tests, verify CSP
holds with the real `app.js`.

---

## 5. Branding / white-label subsystem

*Your addition to the brief.* Goal: same binary, different deployment → different company name,
logo, and accent color, with a generic default that looks intentional out of the box.

### Configuration

| Variable | Default | Notes |
|---|---|---|
| `BRAND_NAME` | `AI Portal` | Shown in header, `<title>`, footer. |
| `BRAND_SHORT_NAME` | = `BRAND_NAME` | For tight spots / mobile header. |
| `BRAND_TAGLINE` | `Private self-hosted AI endpoint` | Landing subhead. |
| `BRAND_LOGO_FILE` | *(none)* | Path to a mounted PNG/SVG/WebP/JPEG. Absent → text wordmark. |
| `BRAND_LOGO_ALT` | = `BRAND_NAME` | |
| `BRAND_FAVICON_FILE` | *(none)* | Falls back to a built-in neutral mark. |
| `BRAND_ACCENT` | `#4f46e5` | `#rgb`/`#rrggbb`. |
| `BRAND_ACCENT_DARK` | *(derived)* | Optional explicit dark-mode accent. |
| `BRAND_SUPPORT_EMAIL` / `BRAND_SUPPORT_URL` | *(none)* | Used by error copy ("contact the service owner"). |

In Kubernetes this is one ConfigMap for the strings and one ConfigMap/volume for the logo file —
no rebuild to rebrand.

### Logo serving

Read once at startup into memory. Then:

- Sniff the content type; accept only `image/png`, `image/jpeg`, `image/webp`, `image/svg+xml`.
  Anything else is a startup error (fail fast, don't silently drop the logo).
- Cap at 512 KB.
- Serve at `/assets/brand/logo-<sha256[:12]>.<ext>` with
  `Cache-Control: public, max-age=31536000, immutable`. The content hash in the path means a logo
  swap busts caches for free.
- **SVG hardening:** an SVG served same-origin can carry `<script>`. It's inert inside `<img>`, but
  a direct navigation to the asset URL would execute it. So SVG responses get
  `Content-Security-Policy: default-src 'none'; style-src 'unsafe-inline'; sandbox` plus
  `X-Content-Type-Options: nosniff`, and startup rejects SVGs containing `<script`, `on*=` handlers,
  `<foreignObject>`, or external `href`/`xlink:href` references. Documented in the README so
  nobody is surprised when their animated SVG is refused.

### Accent color → CSS

CSP stays `style-src 'self'` with **no inline styles and no nonce**. The accent is delivered as a
tiny generated stylesheet at `/assets/brand-<hash>.css`, computed once at startup:

```css
:root {
  --brand-accent: #4f46e5;
  --brand-accent-hover: #4338ca;
  --brand-accent-fg: #ffffff;     /* auto-chosen for contrast */
  --brand-accent-subtle: #eef2ff; /* tinted surface */
}
@media (prefers-color-scheme: dark) {
  :root {
    --brand-accent: #818cf8;
    --brand-accent-hover: #a5b4fc;
    --brand-accent-fg: #10101a;
    --brand-accent-subtle: #1e1b3a;
  }
}
```

Derivation rules, all in `internal/brand/color.go` with unit tests:

- `--brand-accent-fg` is whichever of near-white/near-black scores a higher WCAG contrast ratio
  against the accent. If the winner is still below 4.5:1, log a startup **warning** naming the
  measured ratio (warn, not fail — an operator's brand color is their call, per the brief's
  preference for not blocking on taste).
- Dark-mode accent, when not explicitly set, is lightened until it clears 4.5:1 against the dark
  surface color.
- Hover/subtle are fixed-percentage mixes toward black/white in linear space.

`app.css` (UI agent) consumes these with fallbacks so it renders correctly even if brand.css
fails to load: `background: var(--brand-accent, #4f46e5);`

### Microsoft sign-in button

The surrounding page is fully brandable, but the button itself must stay compliant with
Microsoft's branding guidelines: the Microsoft logo mark plus the words "Sign in with Microsoft".
The logo mark ships as a built-in embedded asset — it is **not** operator-configurable, and
`BRAND_ACCENT` deliberately does not recolor it. I'll note this constraint in the README and
mark the button partial as off-limits for restyling in the UI contract.

### What is *not* configurable in v1

Fonts (system stack only, brief §10), layout, dark/light behavior, and copy other than the fields
above. Adding a full theming engine would violate the "keep it boring" instruction; the four knobs
above cover "make it look like our company."

---

## 6. Full config surface

Brief §17 plus branding and dev flags.

```
PORT=8080
PUBLIC_BASE_URL=https://ai.birks.dev
LOG_LEVEL=info

ENTRA_TENANT_ID=<uuid>
ENTRA_CLIENT_ID=<uuid>
ENTRA_CLIENT_SECRET=<secret>          # required unless DEV_FAKE_AUTH
SESSION_KEY=<base64, 32+ bytes>       # comma-separated list for rotation

KEYSTORE_MODE=memory|kubernetes       # default memory
KUBERNETES_NAMESPACE=llm-access       # required when kubernetes
KUBERNETES_ALLOW_KUBECONFIG=false     # explicit opt-in for out-of-cluster dev

API_KEY_PREFIX=llm_
DEFAULT_MODEL=<served-model-name>
INFERENCE_BASE_URL=<defaults to PUBLIC_BASE_URL>

BRAND_*                               # see §5

DEV_FAKE_AUTH=false                   # dev-only login bypass
```

`DEV_FAKE_AUTH` guard rails (brief §22): refuses to start if it is set while `KEYSTORE_MODE=kubernetes`
or `PUBLIC_BASE_URL` is not a localhost URL; renders a persistent high-contrast banner on every page;
logs a warning line at every startup.

`INFERENCE_BASE_URL` is split out because the portal and `/v1/*` may not always share a hostname,
and the onboarding snippets need the inference one.

---

## 7. Cross-cutting behaviors

**Middleware order:** request-ID → structured logger → panic recovery → security headers →
`CrossOriginProtection` → session load → (per-route) `RequireUser`.

**Logging:** `log/slog` JSON handler. Allowlist approach — a `logfields` helper builds the record
from explicit fields only, so nothing sensitive can be added by accident. A test asserts that a
generated credential never appears in captured log output for the create-key handler.

**Errors:** `internal/httpapp/errors.go` maps internal errors to short user-facing copy plus the
request ID, and logs the real cause. No stack traces, no Kubernetes objects, no OIDC error bodies.

**Graceful shutdown:** SIGTERM → stop accepting → 20s drain. Server timeouts:
`ReadHeaderTimeout` 5s, `ReadTimeout` 15s, `WriteTimeout` 30s, `IdleTimeout` 120s.

---

## 8. Contract for the UI agent

Frozen at end of M0. The UI agent should not need to read any Go code.

### Templates to author (`web/templates/`)

| File | Purpose |
|---|---|
| `base.html` | Shell: `<head>`, header, footer, flash region, dev banner. Defines blocks `title`, `main`. |
| `landing.html` | Signed-out landing + Sign in with Microsoft (brief §11.1). |
| `account.html` | Key list + connect-a-coding-agent section (brief §11.2). |
| `key_new.html` | Create form (brief §11.3). |
| `key_created.html` | One-time key display (brief §11.4). |
| `key_revoke.html` | Revoke confirmation (brief §11.5). |
| `error.html` | Short message + request ID. |
| `_guides.html` | Client-guide tabs; reused by `account.html` and `key_created.html`. |
| `_ms_button.html` | Microsoft sign-in button — **do not restyle the mark or change the wording**. |

### View models

Every template receives a struct embedding `Page`:

```go
type Page struct {
    Brand     Brand      // Name, ShortName, Tagline, LogoURL, LogoAlt, FaviconURL,
                         // SupportEmail, SupportURL, HasLogo
    Title     string
    User      *User      // nil when signed out; Name, Email, Initials
    Flashes   []Flash    // Kind: "success"|"error"|"info"; Message string
    RequestID string
    DevMode   bool       // render the dev-only banner
}

type AccountPage struct {
    Page
    Keys   []KeyView    // ID, Name, CreatedAt time.Time, Suffix
    Guides []Guide
}

type CreatedKeyPage struct {
    Page
    KeyName string
    Secret  string      // cleartext, shown exactly once
    Guides  []Guide     // snippets pre-filled with Secret
}

type NewKeyPage    struct { Page; Name, NameError string }
type RevokePage    struct { Page; Key KeyView }
type ErrorPage     struct { Page; Heading, Message string }

type Guide struct {
    ID, Name, Description string
    Files    []GuideFile   // Path, Language, Content
    Commands []GuideBlock  // Language, Content
    Notes    []string
    AgentPrompt string      // paste-into-your-other-agent text
}
```

### CSS custom properties provided by the backend

`--brand-accent`, `--brand-accent-hover`, `--brand-accent-fg`, `--brand-accent-subtle`.
Always reference them with a literal fallback. Everything else in the palette is the UI agent's
to define in `app.css`.

### Rules

- No inline `<style>` or `<script>`; CSP is `script-src 'self'; style-src 'self'`.
- No third-party assets, fonts, or CDNs of any kind.
- Site must work with JavaScript disabled. `app.js` may only add copy buttons, guide tabs, and
  non-essential feedback — the tabs must degrade to all-panels-visible or `<details>`.
- All mutations are `<form method="post">`. No `fetch()` for state changes.
- System font stack, `prefers-color-scheme` dark mode, semantic HTML, visible focus states.
- Never render `Secret` anywhere except `key_created.html`, and never into an attribute, `id`,
  `data-*`, or URL.

---

## 9. README outline

Covers brief §27 plus branding:

1. What this is / what it deliberately isn't
2. Architecture diagram + the four-way separation of responsibilities
3. Quick start (`KEYSTORE_MODE=memory DEV_FAKE_AUTH=1 go run ./cmd/ai-account`)
4. Entra app registration walkthrough — app type, redirect URIs (prod + localhost), client secret,
   `openid profile email` scopes, where to find tenant/client IDs
5. Environment variable reference (full table)
6. **Branding guide** — the four knobs, logo file requirements and the SVG restrictions, accent
   contrast behavior, example ConfigMap, and the Microsoft-button compliance note
7. Memory vs Kubernetes keystore, and why memory is the default
8. Kubernetes Secret contract (annotated YAML) + the RBAC the ServiceAccount needs
9. Security notes — cleartext-at-rest tradeoff and the compensating controls (brief §7)
10. Container build/run
11. Testing, including how to refresh onboarding goldens
12. Pointer to `dbirks/home-k8s` for manifests, kgateway, Cloudflare

---

## 10. Risks / open questions

1. **Snippet accuracy (M4).** Five fast-moving clients. Golden tests make drift reviewable, but
   the snippets need a real end-to-end test against the cluster before you tell coworkers to use
   them. Realistically this is the item most likely to be wrong at v1.
2. **`wire_api = "responses"` for Codex** requires vLLM's Responses API to be enabled and routed —
   worth confirming on the home-k8s side before the Codex tab ships.
3. **kgateway credential wire formats** (brief §19) can't be verified from this repo. I'll write
   the Secret shape to the documented contract and put an explicit integration-test checklist in
   the README for the home-k8s side.
4. **client-go pulls a large dependency tree.** Acceptable, but it dominates build time and the
   vulnerability surface. Keeping it behind the `KeyStore` interface means it stays swappable.
5. **`CrossOriginProtection` allows requests with no `Sec-Fetch-Site` and no `Origin`** (assumed
   non-browser). That's correct for browsers from 2023 on and this app has no non-browser API,
   so the residual risk is limited to very old browsers. Noting it since it differs from a
   classic synchronizer-token model.

---

## 11. Suggested first commits

1. `go.mod` + scaffold + embedded assets + health endpoints
2. `internal/config` with tests
3. `internal/brand` with color/logo tests
4. View models + placeholder templates → **UI agent unblocked**
5. `internal/keystore` interface + memory + generation tests
6. Handlers + handler tests
