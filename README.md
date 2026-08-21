# API key portal

A small server-rendered Go web application that lets trusted colleagues sign in with a Microsoft
work account, create an API key for a self-hosted LLM endpoint, and connect their coding agent.

## Read this first: it is opinionated

This is not a general-purpose product. It is built for one specific deployment — a self-hosted
vLLM endpoint at `llm.birks.dev`, running on a home Kubernetes cluster, used by colleagues at
[E-gineering](https://www.e-gineering.com) who sign in with their work Microsoft accounts.

Those opinions are baked in rather than abstracted away:

- **Microsoft Entra ID is the only way to sign in.** There is no local account, no password, no
  second identity provider, and no plan for one.
- **One Entra tenant.** Anyone in the configured tenant may sign in. The code keeps tenant ID
  explicit throughout so a future multi-tenant allowlist stays possible, but v1 accepts exactly one.
- **Envoy Gateway enforces the keys.** Its `SecurityPolicy.apiKeyAuth` reads every valid
  credential from **one aggregate Opaque Secret** referenced by name — there is no label selector
  and no per-key Secret. So the portal keeps all issued keys as entries in that single Secret
  (`llm-apikeys` in `llm-portal`), each entry `client-<id>: llm_<credential>`. The Secret's name
  and namespace are configurable (`KUBERNETES_SECRET_NAME`, `KUBERNETES_NAMESPACE`). Key
  *enforcement* stays with the gateway regardless of what serves the models.
- **A multi-model catalog is the backend.** Behind the gateway is a KServe/llm-d catalog on a
  **single GPU** — an RTX PRO 6000 Blackwell (96 GB) in a homelab — fronted by an Envoy AI Gateway.
  Models are quantised to **NVFP4 (W4A4)** so they compute natively on the card's Blackwell FP4
  tensor cores, and the GPU is shared across the resident models by **HAMi**. The catalog is growing
  toward roughly half a dozen models spanning coding, reasoning, and general-purpose work — e.g.
  **Qwen3.8-27B** (coding) on the portal origin's `/v1` and **Muse-Glimmer-30B** (reasoning) under
  `/muse/v1` — each authenticated by the same key and addressed by name. The setup picker
  parameterises every client guide per model, so a selection produces the exact base URL and model
  name for that model. Because it is one GPU, models **scale to zero** and load on demand, so a first
  request to an idle model pays a cold start of a minute or two. The client guides assume the
  Anthropic Messages API, OpenAI-compatible endpoints, and the Responses API are all served from the
  same origin (per model base path).
- **Kubernetes Secrets are the database.** There is no SQL, no ORM, and no user table.

If you want to run this somewhere else, it will work — the branding, hostname, tenant, namespace,
and models are all configuration — but expect to change the label constant and re-check the gateway
contract. Treat it as a starting point, not a product.

```
Microsoft Entra ID   ->  who is this person?
This portal          ->  may they create and revoke their own credentials?
Kubernetes Secret    ->  where are the credentials stored? (one aggregate Secret)
Envoy Gateway        ->  is this API request carrying a valid credential?
Envoy AI Gateway     ->  which model does "model": "…" route to?
KServe / llm-d       ->  load the model on demand and run the request.
```

That separation is the whole design. Each layer does one job and knows nothing about the others'
internals.

## What it does

- Signs a user in against a single Microsoft Entra tenant using OpenID Connect.
- Lists the API keys that user owns.
- Creates a new key, shown exactly once.
- Revokes a key.
- Generates copy-paste setup instructions through a **model × client picker**: choose a model
  (e.g. `qwen3.8-nvfp4` or `muse-glimmer-30b`) and a client (Claude Code, a generic
  OpenAI-compatible client, Cursor, a raw `curl`, plus Pi, OpenCode, Codex, and Crush), and get the
  exact config with the correct base URL for that model.
- Lists the servable models at `GET /models`, a public page read from the cluster (not from the
  auth-gated `/v1/models`), with a per-model status hint and a cold-start note.
- Surfaces a metrics dashboard (`GRAFANA_URL`, e.g. `https://llm.birks.dev/grafana`) when one is
  configured: a header link plus a prominent "Live metrics & GPU dashboard" card on the landing,
  account, and How it works pages.
- Explains the service at `GET /how-it-works`, which is public so it can be read before signing in:
  what it is, the key lifecycle, the full **architecture stack** (Cloudflare + Envoy Gateway → Envoy
  AI Gateway → KServe/llm-d → vLLM → NVFP4 weights → the GPU), and the shape of the model catalog.

### How to use (OpenAI-compatible)

Each model has a base URL and a model name; the same key works for all of them:

| Model | Kind | Base URL | `model` field |
|---|---|---|---|
| Qwen3.8-27B | coding | `https://llm.birks.dev/v1` | `qwen3.8-nvfp4` |
| Muse-Glimmer-30B | reasoning | `https://llm.birks.dev/muse/v1` | `muse-glimmer-30b` |

Point any OpenAI-compatible client at the model's base URL, send the key as a `Bearer` token, and
set the `model` field to match. Claude Code uses the base **without** `/v1` as `ANTHROPIC_BASE_URL`
(it appends `/v1/messages` itself). The first call to an idle model cold-starts while it loads on
demand; later calls are warm.

**Muse-Glimmer is a reasoning model** — it thinks before it answers, so allow a generous
`max_tokens` (2048 or more) or the reply can be cut off mid-thought.

The live list, including any models added later, is on [`/models`](https://llm.birks.dev/models),
and your account page generates the copy-paste snippet for each model and client.

## What it deliberately does not do

No user database. No password handling. No inference proxying — `/v1/*`
never touches this service. No API-key validation on the request path; that is the gateway's job.
No admin UI, quotas, billing, usage dashboards, or key expiry. No React, no build chain, no
JavaScript framework, and no third-party assets of any kind.

There is also no "show key" feature, and there never will be. A key is displayed once at creation.
If it is lost, create a replacement and revoke the old one.

## Architecture

```
                    Microsoft Entra ID
                          |
                    OIDC sign-in
                          v
                 +--------------------+
                 |   this portal      |
                 +---------+----------+
                           | Kubernetes API
                           |   - upserts entries in the aggregate API-key Secret (patch/create)
                           |   - reads LLMInferenceServices (list, for /models)
                           v
                 aggregate API-key Secret
                     (llm-apikeys)
                           | referenced by name
                           v
Internet -> Cloudflare Tunnel -> Envoy Gateway -> Envoy AI Gateway -> KServe / llm-d -> vLLM
                                  (checks the key)  (routes by model name)  (scale-to-zero
                                                                             on-demand models)
                                                                                     |
                              NVFP4 (W4A4) weights on one RTX PRO 6000 Blackwell (96 GB), shared by HAMi
```

The portal's read of `LLMInferenceServices` is read-only and only powers the `/models` page; it is
independent of key enforcement, which remains the gateway's job.

Deployment manifests — Gateway, HTTPRoute, SecurityPolicy, RBAC, Cloudflare Tunnel, Envoy AI Gateway,
and the KServe/llm-d model set — live in the cluster repository (`dbirks/home-k8s`), not here. This
repository produces one container image and documents the contract that image expects.

## Quick start

No cluster and no Entra tenant required:

```bash
export PUBLIC_BASE_URL=http://localhost:8080
export SESSION_KEY=$(openssl rand -base64 32)
export KEYSTORE_MODE=memory
export DEV_FAKE_AUTH=1
# One model, or a picker over several with per-model base paths:
export ONBOARDING_MODELS='[{"id":"qwen3.8-nvfp4","label":"Qwen3.8","kind":"coding","path":""},{"id":"muse-glimmer-30b","label":"Muse-Glimmer 30B","kind":"reasoning","path":"/muse"}]'
# Optional metrics link in the header:
export GRAFANA_URL=https://llm.birks.dev/grafana

go run ./cmd/ai-account
```

Then open http://localhost:8080. `DEV_FAKE_AUTH` signs in a fixed fake user, seeds a couple of
example keys, and renders a red banner on every page. It refuses to start unless the public URL is
loopback and the keystore is `memory`, so it cannot be switched on in a real deployment by accident.

To iterate on templates and CSS without rebuilding:

```bash
DEV_ASSETS_DIR=./web go run ./cmd/ai-account
```

Assets are then read from disk and re-parsed per request. Without it, everything is served from the
copy embedded in the binary.

To exercise the real sign-in flow locally, drop `DEV_FAKE_AUTH`, set the `ENTRA_*` variables, and
register `http://localhost:8080/auth/callback` as an additional redirect URI.

## Entra app registration

In the Microsoft Entra admin center, under **App registrations → New registration**:

1. **Supported account types**: *Accounts in this organizational directory only* (single tenant).
2. **Redirect URI**: platform **Web**, value `https://your-host/auth/callback`. Add
   `http://localhost:8080/auth/callback` as a second URI if you want to run the real flow locally.
3. From the **Overview** page, copy the *Application (client) ID* into `ENTRA_CLIENT_ID` and the
   *Directory (tenant) ID* into `ENTRA_TENANT_ID`.
4. Under **Certificates & secrets**, create a client secret and put its **value** (not its ID) into
   `ENTRA_CLIENT_SECRET`. Note the expiry date — a silently expired secret breaks sign-in.
5. Under **API permissions**, the delegated `User.Read` is enough — and is what the default profile
   photo feature needs. Beyond sign-in the portal makes exactly one Graph call, for the signed-in
   user's own photo. Set `ENTRA_AVATARS=false` to drop that scope and show initials instead.

   Never grant an **Application** permission, and never `User.Read.All`, `User.ReadBasic.All`,
   `Directory.Read.All`, or `offline_access`. Delegated `User.Read` lets the portal read the photo of
   whoever is signed in and nothing else; the tenant-wide variants would turn a low-risk sign-in app
   into a directory reader whose client secret is worth stealing.

The portal does not require Enterprise Application user assignment. Any account in the configured
tenant may sign in.

Identity is the `tid` + `oid` claim pair. Email is display-only and is never used for
authorization — it is mutable and can be reassigned.

## Configuration

All configuration is environment variables, read once at startup. Invalid configuration is a
startup failure, and every problem is reported at once rather than one per restart.

### Required

| Variable | Description |
|---|---|
| `PUBLIC_BASE_URL` | Canonical external origin, e.g. `https://ai.example.com`. Used to build the OAuth redirect URI. Plain `http` is rejected except on loopback. |
| `SESSION_KEY` | Base64-encoded secret, 32+ bytes. Generate with `openssl rand -base64 32`. |
| `ENTRA_TENANT_ID` | Directory (tenant) ID. |
| `ENTRA_CLIENT_ID` | Application (client) ID. |
| `ENTRA_CLIENT_SECRET` | Client secret **value**. |

`ENTRA_*` are not required when `DEV_FAKE_AUTH` is set.

### Profile photos

`ENTRA_AVATARS` (default `true`) shows each user's Microsoft profile photo in the header. Set it to
`false` to turn the feature off, which also removes the Graph `User.Read` scope from the sign-in
request — the setting to reach for in a tenant that has disabled user consent.

Everything about this is best-effort, and initials are the fallback in every failure case:

- The photo is fetched **once**, during the sign-in callback, using the access token from that
  exchange. The token is then discarded. Nothing here is ever stored, so there is no refresh-token
  story and no Graph credential at rest.
- The cache is **per-process and in memory**. With more than one replica, a user who signs in on one
  pod and is later served by another sees initials there until their next sign-in. That is the price
  of not persisting a Graph-capable token, and it is the right side of that trade.
- Many users have no photo at all. Sized variants exist only for photos stored in Exchange Online,
  so a user without a mailbox has only the unsized endpoint, and a user who never uploaded one has
  neither. Both render as initials, which is not a fault.
- Fetched bytes are only served back if they decode as JPEG or PNG, are under 1 MiB, and are at most
  2048px on a side. The content type is derived from the bytes, never from what Graph claimed.
- `DEV_FAKE_AUTH` never fetches photos: the fake sign-in has no token and no real user behind it.

### Optional

| Variable | Default | Description |
|---|---|---|
| `PORT` | `8080` | Listen port. |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error`. |
| `KEYSTORE_MODE` | `memory` | `memory` or `kubernetes`. |
| `KUBERNETES_NAMESPACE` | — | Required when `KEYSTORE_MODE=kubernetes`. |
| `KUBERNETES_ALLOW_KUBECONFIG` | `false` | Permit falling back to a local kubeconfig outside a cluster. |
| `MODELS_NAMESPACE` | — | Namespace holding the KServe `LLMInferenceService` objects the `/models` page lists. Empty turns the page off and hides the nav link. Only read in `kubernetes` keystore mode. |
| `MODELS_LABEL_SELECTOR` | — | Optional label selector narrowing the catalog to a curated subset, e.g. `tier=public`. Empty lists them all. |
| `API_KEY_PREFIX` | `llm_` | Prefix on generated credentials. |
| `ONBOARDING_MODELS` | — | JSON array of models the setup picker offers, each with its own base path. See below. Empty falls back to a single model built from `DEFAULT_MODEL`. |
| `DEFAULT_MODEL` | — | Single-model fallback prefilled into the setup snippets when `ONBOARDING_MODELS` is unset — a convenience, not the only model available. Clients may set `model` to any name the gateway routes; see `/models` for the live list. |
| `GRAFANA_URL` | — | Absolute http(s) URL (may include a path, e.g. `https://llm.birks.dev/grafana`). When set, a **Metrics** link appears in the header and on the account page. Empty hides it. |
| `ENTRA_AVATARS` | `true` | Microsoft profile photos in the header. See above. |
| `INFERENCE_BASE_URL` | `PUBLIC_BASE_URL` | Set only if `/v1/*` lives on a different hostname than the portal. |

`ONBOARDING_MODELS` is a JSON array. Each entry is `{ "id", "label", "kind", "path" }`:

- `id` (required) — the value sent in the OpenAI `model` field.
- `label` — human name shown on the picker; defaults to `id`.
- `kind` — `"coding"`, `"reasoning"`, or empty. A `reasoning` model's snippets carry a max_tokens reminder.
- `path` — base-path segment the model is served under, before `/v1`, with a leading slash and no trailing one — `""` for the portal origin, `"/muse"` for a model routed under a subpath.

```json
[
  {"id": "qwen3.8-nvfp4",    "label": "Qwen3.8",          "kind": "coding",    "path": ""},
  {"id": "muse-glimmer-30b", "label": "Muse-Glimmer 30B", "kind": "reasoning", "path": "/muse"}
]
```
| `DEV_ASSETS_DIR` | — | Serve templates and CSS from disk with per-request reload. |
| `DEV_FAKE_AUTH` | `false` | Development sign-in bypass. See the guard rails above. |

Branding variables are documented in the next section.

`SESSION_KEY` accepts a comma-separated list. The first key seals new sessions and all of them are
accepted when reading, so rotation is: prepend a new key, deploy, and drop the old one once
existing sessions have expired (10 hours).

`ENTRA_CLIENT_SECRET` and `SESSION_KEY` have no defaults and never will. They are deployment
Secrets managed in the cluster repository, and they are unrelated to the API-key Secrets this
service creates.

## Branding

The defaults are this deployment's own identity — "llm.birks.dev", blue accent, and the e-gineering
mark, which is compiled into the binary and shown unless an operator mounts their own logo. The
knobs below exist so the portal does not look wrong when it is run somewhere else, and so a logo can
be dropped in without a rebuild. In Kubernetes this is one ConfigMap for the strings; no mounted
volume is needed unless you are overriding the built-in logo or favicon.

This is not a white-label product; it is one deployment with a few things left adjustable.

| Variable | Default | Description |
|---|---|---|
| `BRAND_NAME` | `llm.birks.dev` | Company or service name. Appears in the header, page title, and footer. |
| `BRAND_SHORT_NAME` | = `BRAND_NAME` | Shorter form for tight layouts. |
| `BRAND_ORG_NAME` | `E-gineering` | Organisation whose work accounts sign in. Builds the sign-in heading, "Sign in to your … account". Leave empty for a plain "Sign in". |
| `BRAND_TAGLINE` | `Private self-hosted AI endpoint` | One-line description on the landing page and footer. |
| `BRAND_LOGO_FILE` | — | Path to a mounted PNG, JPEG, WebP, or SVG. Overrides the built-in e-gineering mark; without one, that embedded default is rendered. |
| `BRAND_LOGO_ALT` | `e-gineering`, or `BRAND_NAME` when a logo file is mounted | Alt text for the logo. |
| `BRAND_FAVICON_FILE` | — | Path to a mounted icon. |
| `BRAND_ACCENT` | `#3b6fd6` | Accent colour, `#rgb` or `#rrggbb`. |
| `BRAND_ACCENT_DARK` | derived | Explicit dark-mode accent. Derived from `BRAND_ACCENT` when unset. |
| `BRAND_SUPPORT_EMAIL` | — | Shown as a "Get help" link. |
| `BRAND_SUPPORT_URL` | — | Takes precedence over the email if both are set. |

Text fields are capped at 64 characters and reject control characters and pre-escaped HTML.

### How the accent colour works

The portal serves a strict Content-Security-Policy with `style-src 'self'` and no nonce, which
rules out injecting a colour as an inline style. Instead the accent is compiled at startup into a
small generated stylesheet served at a content-addressed URL, defining four custom properties that
the main stylesheet consumes:

```css
--brand-accent
--brand-accent-hover
--brand-accent-fg      /* auto-chosen for contrast against the accent */
--brand-accent-subtle  /* tinted surface */
```

`--brand-accent-fg` is whichever of near-white or near-black scores a higher WCAG contrast ratio
against your accent. If the best available ratio is still below 4.5:1, startup logs a warning
naming the measured ratio but proceeds — your brand colour is your call.

For dark mode, an accent chosen for white backgrounds is usually too dark on a dark surface, so
when `BRAND_ACCENT_DARK` is unset the colour is lightened until it clears 4.5:1. Set
`BRAND_ACCENT_DARK` explicitly to take that decision yourself.

Because the stylesheet URL contains a hash of its contents, changing the accent invalidates caches
automatically.

### Logo requirements

- PNG, JPEG, WebP, or SVG. The format is detected from the file contents, not the extension.
- 512 KiB maximum.
- Served from a content-addressed path with immutable caching, so replacing the file busts caches.

**SVGs are restricted.** An SVG served from our own origin can carry scripts. That is inert inside
an `<img>` tag but would execute on a direct navigation to the asset URL, so SVG responses carry a
sandboxed `default-src 'none'` policy, and startup **rejects** any SVG containing `<script>`,
`on*=` event handlers, `<foreignObject>`, embedded entities, or external `href`/`xlink:href`
references. Export a flattened SVG, or use a PNG.

A broken or missing logo file is a startup error, not a silent fallback — a mounted-but-unreadable
logo is a deployment mistake worth hearing about immediately.

When `BRAND_LOGO_FILE` is unset the portal serves an embedded default, the e-gineering mark
(`internal/brand/egineering.svg`, compiled in with `//go:embed`). It runs through the same
validation, content-addressing, and sandboxed serving as a mounted file, so the app ships branded
with nothing to mount. Setting `BRAND_LOGO_FILE` replaces it.

### What is not brandable

Fonts are the system stack. Layout, dark-mode behaviour, and copy other than the fields above are
fixed.

The **Sign in with Microsoft** button is also fixed. Microsoft's branding guidelines require the
four-square mark and that exact wording, so the mark ships as a built-in asset, `BRAND_ACCENT` does
not recolour it, and `web/templates/partials/ms_button.html` should not be restyled. Everything
around it is yours.

### Example

```bash
BRAND_NAME="Acme AI"
BRAND_TAGLINE="Private self-hosted AI for the team"
BRAND_ACCENT="#0f766e"
BRAND_LOGO_FILE=/etc/brand/logo.svg
BRAND_SUPPORT_EMAIL=help@birks.dev
```

## Keystores

**`memory`** (default) keeps keys in process memory. Keys are lost on restart and are not usable
for real inference. This is what local development and the test suite use.

Memory is the default on purpose. If Kubernetes were the default, running the portal on a laptop
would write real Secrets into whatever cluster the current kubeconfig context happened to point at.
Selecting the real store is always explicit.

**`kubernetes`** persists each credential as a Secret through the API server. In-cluster
configuration is used automatically. Outside a cluster the portal refuses to start unless
`KUBERNETES_ALLOW_KUBECONFIG=true`, rather than guessing which cluster you meant.

The portal never talks to etcd.

## Kubernetes Secret contract

All issued keys are entries in **one aggregate Opaque Secret**. Envoy Gateway's
`SecurityPolicy.apiKeyAuth` references that Secret by name and compares a presented credential
against its values — there is no label selector, so there is no per-key Secret:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: llm-apikeys            # KUBERNETES_SECRET_NAME (default)
  namespace: llm-portal        # KUBERNETES_NAMESPACE
  annotations:
    # one per issued key: ownership tuple + display metadata, as JSON.
    # the gateway never reads these; the account page uses them to list and
    # revoke a user's own keys, since a per-key label is no longer possible.
    llm-portal/key-<id>: '{"tid":"<entra-tenant-id>","oid":"<entra-object-id>","name":"MacBook Claude Code","suffix":"A1b2C3","owner_name":"Alice Example","owner_email":"alice@example.com","created":"2026-08-21T00:00:00Z"}'
type: Opaque
data:
  client-<id>: <base64 of "llm_<credential>">   # bare token, no "Bearer " prefix
  client-test: <base64 of "llm_testkey123">     # bootstrap entry, owned by nobody
```

The Secret is created once (SOPS-managed in the cluster repository) and the portal **upserts
entries into it at runtime** — it never recreates it. Issuing a key `patch`es in one
`client-<id>` data entry plus its ownership annotation; revoking `patch`es both back out (a JSON
merge patch with `null` values). If the Secret is somehow absent, the first issue creates it.

Ownership lives in the per-key `llm-portal/key-<id>` annotation because there is only one object to
hang a label on. Every list/get/revoke checks the caller's Entra tuple against the annotation; a
data entry with no matching annotation — the bootstrap `client-test` entry, for one — is owned by
nobody and never appears on any user's account page.

The `llm-portal` prefix is the `LabelDomain` constant in `internal/keystore/kubernetes`. These
annotations are internal to the portal (the gateway ignores them), so the value only has to stay
stable across the portal's own upgrades. It is deliberately not a hostname.

Neither the data key (client identifier) nor the credential contains the user's email or object
ID. The client identifier may appear in gateway logs, so it is kept opaque and non-identifying.

### Required RBAC

The portal's ServiceAccount needs, in that namespace only:

```yaml
rules:
  - apiGroups: [""]
    resources: ["secrets"]
    verbs: ["get", "list", "watch", "create", "patch", "update"]
```

`patch` (and `create`, to bootstrap a missing Secret) is what lets the portal upsert entries into
the single aggregate Secret. It can be narrowed to `resourceNames: ["llm-apikeys"]` for the
mutating verbs.

The dedicated namespace matters. Kubernetes RBAC cannot restrict Secret access by label, only by
namespace and name, so the namespace is the actual blast-radius boundary.

When the `/models` page is enabled (`MODELS_NAMESPACE` set), the ServiceAccount also needs
read-only access to the model objects in that namespace:

```yaml
rules:
  - apiGroups: ["serving.kserve.io"]
    resources: ["llminferenceservices"]
    verbs: ["get", "list", "watch"]
```

Read-only, and only in the models namespace. The portal never creates, deletes, or edits a model —
it lists them for display. This Role lives in the cluster repository (`dbirks/home-k8s`) alongside
the model objects, not here.

## Security notes

**Credentials are stored in cleartext in a Kubernetes Secret.** This is a deliberate trade-off, not
an oversight. Envoy Gateway's built-in API-key authentication compares the presented credential
against the value in the Secret, so a hash cannot be substituted without giving up built-in auth
and writing a custom auth service.

The compensating controls are the cluster's responsibility:

- etcd encryption at rest for Secrets
- least-privilege RBAC as above
- a dedicated namespace
- protected backups
- no credential logging

The portal behaves as if the credential were unrecoverable regardless: it is shown once and never
retrievable through the UI.

Other properties, each covered by a test:

- Credentials are 256 bits from `crypto/rand`, formatted `llm_<base64url>`.
- A credential never appears in a URL, redirect, cookie, element attribute, or log line. There are
  explicit regression tests asserting this.
- The one-time page sends `Cache-Control: no-store` and `Referrer-Policy: no-referrer`.
- Ownership is checked against the stored object's labels on every read and delete, never against
  form input. A key belonging to someone else returns 404, identically to a missing one.
- Two users with the same object ID in different tenants are different people.
- Sessions are AES-GCM sealed cookies, `HttpOnly`, `SameSite=Lax`, `Secure` and `__Host-` prefixed
  on HTTPS. The 10-hour lifetime is enforced from the sealed payload, not the cookie's `Max-Age`.
- CSRF protection is `net/http.CrossOriginProtection` (Go 1.25+), which rejects non-safe
  cross-origin requests using `Sec-Fetch-Site`, falling back to comparing `Origin` against `Host`.
  There are no per-form tokens. Note that requests carrying *neither* header are treated as
  same-origin or non-browser and allowed; this differs from a synchroniser-token model and is
  relevant only for pre-2023 browsers.
- CSP is first-party only, with no `unsafe-inline` anywhere. `form-action` permits
  `login.microsoftonline.com` for the sign-in redirect.
- Logs carry `request_id`, `tid`, `oid`, operation, and resource name — never tokens, cookies,
  credentials, Secret data, or query strings.
- The `SameSite=Lax` choice is deliberate: `Strict` would drop the session cookie on the
  cross-site top-level navigation back from Microsoft.

## Health endpoints

| Path | Meaning |
|---|---|
| `GET /healthz` | The process is alive. Suitable as a liveness probe. |
| `GET /readyz` | Configuration is loaded and the keystore is usable. In Kubernetes mode this confirms the API server is reachable and the ServiceAccount may list Secrets. Suitable as a readiness probe. |

Both return `ok` and expose no internal detail.

## Container

```bash
docker build -t ai-account:dev .

docker run --rm -p 8080:8080 \
  -e PUBLIC_BASE_URL=http://localhost:8080 \
  -e SESSION_KEY="$(openssl rand -base64 32)" \
  -e DEV_FAKE_AUTH=1 \
  ai-account:dev
```

The image is `distroless/static`, runs as uid 65532, and contains one static binary plus CA
certificates. No shell, no package manager. Templates and stylesheets are embedded in the binary.

Published to `ghcr.io/dbirks/kubernetes-llm-api-key-portal` on pushes to `main` and on tags, for
amd64 and arm64, with SBOM, provenance, and an immutable `sha-<commit>` tag alongside the
convenience tags.

## Development

```bash
go test ./...          # full suite
go test -race ./...    # what CI runs
go vet ./...
```

The onboarding snippets are golden-tested so that an upstream client changing its configuration
format shows up as a reviewable diff rather than as silently wrong instructions:

```bash
go test ./internal/onboarding -update
git diff internal/onboarding/testdata
```

CI fails if the goldens are stale.

### Testing against a real cluster

The Kubernetes store is unit-tested with `client-go`'s fake clientset, which checks the object we
build and our own ownership logic. It cannot tell you whether the API server accepts our merge
patch, or whether your RBAC grants the `patch`/`create` verbs the upsert needs — the fake simulates
none of that.

For those, there is an opt-in integration test:

```bash
kubectl create namespace portal-itest
PORTAL_INTEGRATION_NAMESPACE=portal-itest go test ./internal/keystore/kubernetes -run Integration -v
kubectl delete namespace portal-itest
```

It creates real Secrets through your current kubeconfig context and deletes them on the way out.
Point it at a scratch namespace. Without the environment variable it skips, so `go test ./...` and
CI are unaffected.

What still is not covered anywhere in this repository: whether the gateway actually accepts the
credential. That needs the checklist at the end of this file.

### Layout

```
cmd/ai-account/      wiring, signals, graceful shutdown
internal/config/     environment loading and validation
internal/brand/      white-label name, logo, and generated colour stylesheet
internal/auth/       OIDC flow, sealed sessions, middleware, dev bypass
internal/keystore/   KeyStore interface, credential generation
  memory/            in-process store for development and tests
  kubernetes/        production store
internal/models/     read-only KServe model catalog for the /models page
internal/onboarding/ client setup guides, golden-tested
internal/httpapp/    routing, handlers, middleware, view models
web/                 templates and static assets, embedded via go:embed
```

## Verifying the gateway integration

The credential shape depends on the gateway's contract, which cannot be verified from this
repository. Before pointing colleagues at the portal, confirm against the Envoy Gateway release
actually deployed:

- [ ] The `SecurityPolicy.apiKeyAuth` `credentialRefs` names the `llm-apikeys` Secret, and each
      `data` value there is accepted as a valid key.
- [ ] `Authorization: Bearer <key>` is accepted (OpenAI-compatible SDKs, Claude Code gateway mode).
- [ ] `X-Api-Key: <key>` is accepted where Anthropic-style clients need it.
- [ ] A revoked key stops authenticating after the normal watch propagation delay.
- [ ] `forwardCredential` is `false` unless something downstream genuinely needs it.
- [ ] Only the intended inference routes are publicly exposed — no model management endpoints.
- [ ] `/v1/responses` is served and routed, which the Codex setup snippet depends on.
- [ ] The Envoy AI Gateway routes each `model` name from `/models` to the right KServe backend, and
      a request naming an idle model cold-starts rather than failing.
- [ ] The ServiceAccount can `list` `llminferenceservices` in `MODELS_NAMESPACE` — otherwise
      `/models` returns its friendly 503. (This Role lives in `dbirks/home-k8s`.)

Each client setup snippet should be tested end to end, for each model. They are the part of this
repository most likely to drift.

## License

[Apache License 2.0](LICENSE). Copyright 2026 David Birks.

Apache-2.0 rather than MIT because this repository is employer-adjacent: it includes an explicit
patent grant, and its contribution clause licenses any submitted change under the same terms
without needing a separate CLA.
