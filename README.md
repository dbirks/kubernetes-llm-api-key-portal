# API key portal

A small server-rendered Go web application that lets trusted colleagues sign in with a Microsoft
work account, create an API key for a self-hosted LLM endpoint, and connect their coding agent.

## Read this first: it is opinionated

This is not a general-purpose product. It is built for one specific deployment — a self-hosted
vLLM endpoint at `ai.birks.dev`, running on a home Kubernetes cluster, used by colleagues at
[E-gineering](https://www.e-gineering.com) who sign in with their work Microsoft accounts.

Those opinions are baked in rather than abstracted away:

- **Microsoft Entra ID is the only way to sign in.** There is no local account, no password, no
  second identity provider, and no plan for one.
- **One Entra tenant.** Anyone in the configured tenant may sign in. The code keeps tenant ID
  explicit throughout so a future multi-tenant allowlist stays possible, but v1 accepts exactly one.
- **kgateway enforces the keys.** The Secret shape, the `extauth.solo.io/apikey` type, and the
  `ai.birks.dev/*` label prefix exist because that is what kgateway's built-in API-key auth
  selects on. `ai.birks.dev` is a compile-time constant in
  `internal/keystore/kubernetes`, not a setting — it is one half of a contract with the
  `TrafficPolicy` in the cluster repository, and the two must change together.
- **vLLM is the backend.** The five client setup guides assume the Anthropic Messages API,
  OpenAI-compatible endpoints, and the Responses API are all served from the same origin.
- **Kubernetes Secrets are the database.** There is no SQL, no ORM, and no user table.

If you want to run this somewhere else, it will work — the branding, hostname, tenant, namespace,
and model are all configuration — but expect to change the label constant and re-check the gateway
contract. Treat it as a starting point, not a product.

```
Microsoft Entra ID   ->  who is this person?
This portal          ->  may they create and revoke their own credentials?
Kubernetes Secrets   ->  where are the credentials stored?
kgateway             ->  is this API request carrying a valid credential?
vLLM                 ->  where does the inference request go?
```

That separation is the whole design. Each layer does one job and knows nothing about the others'
internals.

## What it does

- Signs a user in against a single Microsoft Entra tenant using OpenID Connect.
- Lists the API keys that user owns.
- Creates a new key, shown exactly once.
- Revokes a key.
- Generates copy-paste setup instructions for Claude Code, Pi, OpenCode, Codex, and Crush.

## What it deliberately does not do

No user database. No password handling. No Microsoft Graph calls. No inference proxying — `/v1/*`
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
                           v
                   API-key Secrets
                           | selected by label
                           v
Internet -> Cloudflare Tunnel -> kgateway -> vLLM
```

Deployment manifests — Gateway, HTTPRoute, TrafficPolicy, RBAC, Cloudflare Tunnel, vLLM — live in
the cluster repository (`dbirks/home-k8s`), not here. This repository produces one container image
and documents the contract that image expects.

## Quick start

No cluster and no Entra tenant required:

```bash
export PUBLIC_BASE_URL=http://localhost:8080
export SESSION_KEY=$(openssl rand -base64 32)
export KEYSTORE_MODE=memory
export DEV_FAKE_AUTH=1
export DEFAULT_MODEL=Qwen3-Coder-30B

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
5. Under **API permissions**, the default delegated `User.Read` is enough. The portal requests only
   `openid`, `profile`, and `email`, and never calls Microsoft Graph.

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

### Optional

| Variable | Default | Description |
|---|---|---|
| `PORT` | `8080` | Listen port. |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error`. |
| `KEYSTORE_MODE` | `memory` | `memory` or `kubernetes`. |
| `KUBERNETES_NAMESPACE` | — | Required when `KEYSTORE_MODE=kubernetes`. |
| `KUBERNETES_ALLOW_KUBECONFIG` | `false` | Permit falling back to a local kubeconfig outside a cluster. |
| `API_KEY_PREFIX` | `llm_` | Prefix on generated credentials. |
| `DEFAULT_MODEL` | — | Served model name used in the setup snippets. |
| `INFERENCE_BASE_URL` | `PUBLIC_BASE_URL` | Set only if `/v1/*` lives on a different hostname than the portal. |
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

The defaults are this deployment's own identity — "Birks AI", indigo accent, no logo. The knobs
below exist so the portal does not look wrong when it is run somewhere else, and so a logo can be
dropped in without a rebuild. In Kubernetes this is one ConfigMap for the strings and one mounted
volume for the image files.

This is not a white-label product; it is one deployment with a few things left adjustable.

| Variable | Default | Description |
|---|---|---|
| `BRAND_NAME` | `Birks AI` | Company or service name. Appears in the header, page title, and footer. |
| `BRAND_SHORT_NAME` | = `BRAND_NAME` | Shorter form for tight layouts. |
| `BRAND_TAGLINE` | `Private self-hosted AI endpoint` | One-line description on the landing page and footer. |
| `BRAND_LOGO_FILE` | — | Path to a mounted PNG, JPEG, WebP, or SVG. Without one, a text wordmark is rendered. |
| `BRAND_LOGO_ALT` | = `BRAND_NAME` | Alt text for the logo. |
| `BRAND_FAVICON_FILE` | — | Path to a mounted icon. |
| `BRAND_ACCENT` | `#4f46e5` | Accent colour, `#rgb` or `#rrggbb`. |
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

### What is not brandable

Fonts are the system stack. Layout, dark-mode behaviour, and copy other than the fields above are
fixed.

The **Sign in with Microsoft** button is also fixed. Microsoft's branding guidelines require the
four-square mark and that exact wording, so the mark ships as a built-in asset, `BRAND_ACCENT` does
not recolour it, and `web/templates/partials/ms_button.html` should not be restyled. Everything
around it is yours.

### Example

```bash
BRAND_NAME="Birks AI"
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

One Secret per credential:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: llm-key-<opaque-id>
  namespace: llm-access
  labels:
    ai.birks.dev/api-key: "true"          # the gateway's selector
    ai.birks.dev/owner-tid: "<entra-tenant-id>"
    ai.birks.dev/owner-oid: "<entra-object-id>"
  annotations:
    ai.birks.dev/display-name: "MacBook Claude Code"
    ai.birks.dev/key-suffix: "A1b2C3"
    ai.birks.dev/owner-display-name: "Alice Example"
    ai.birks.dev/owner-email: "alice@example.com"
    ai.birks.dev/managed-by: "ai-account"
type: extauth.solo.io/apikey
immutable: true
stringData:
  client-<opaque-id>: "llm_<credential>"
```

The `ai.birks.dev` prefix is the `LabelDomain` constant in `internal/keystore/kubernetes`. It must
match the gateway's `secretSelector`. Changing it is a code change on purpose: as an environment
variable it looked like a per-deployment preference, and a mismatch silently stops every key from
authenticating with no error anywhere.

Secrets are immutable: keys are created and deleted, never edited. Rotation is create replacement →
verify → revoke old.

Neither the Secret name nor the client identifier contains the user's email or object ID. The
client identifier may appear in gateway logs, so it is kept opaque and non-identifying.

### Required RBAC

The portal's ServiceAccount needs, in that namespace only:

```yaml
rules:
  - apiGroups: [""]
    resources: ["secrets"]
    verbs: ["get", "list", "create", "delete"]
```

There is deliberately no `update` or `patch`: the service has no code path that modifies an
existing credential Secret, and withholding the verb makes that structural.

The dedicated namespace matters. Kubernetes RBAC cannot restrict Secret access by label, only by
namespace and name, so the namespace is the actual blast-radius boundary.

## Security notes

**Credentials are stored in cleartext in Kubernetes Secrets.** This is a deliberate trade-off, not
an oversight. kgateway's built-in API-key authentication compares the presented credential against
the value in the Secret, so a hash cannot be substituted without giving up built-in auth and
writing a custom auth service.

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
build and our own ownership logic. It cannot tell you whether the API server accepts that object,
whether your RBAC is right, or whether `immutable: true` is genuinely enforced — the fake simulates
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

What still is not covered anywhere in this repository: whether kgateway actually accepts the
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
internal/onboarding/ client setup guides, golden-tested
internal/httpapp/    routing, handlers, middleware, view models
web/                 templates and static assets, embedded via go:embed
```

## Verifying the gateway integration

The credential shape depends on kgateway's contract, which cannot be verified from this repository.
Before pointing colleagues at the portal, confirm against the kgateway release actually deployed:

- [ ] The `TrafficPolicy` `secretSelector` matches `ai.birks.dev/api-key=true`.
- [ ] `Authorization: Bearer <key>` is accepted (OpenAI-compatible SDKs, Claude Code gateway mode).
- [ ] `X-Api-Key: <key>` is accepted where Anthropic-style clients need it.
- [ ] A revoked key stops authenticating after the normal watch propagation delay.
- [ ] `forwardCredential` is `false` unless something downstream genuinely needs it.
- [ ] Only the intended inference routes are publicly exposed — no vLLM management endpoints.
- [ ] `/v1/responses` is served and routed, which the Codex setup snippet depends on.

Each of the five client setup snippets should be tested end to end. They are the part of this
repository most likely to drift.
