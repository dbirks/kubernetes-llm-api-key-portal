# ai.birks.dev Account Portal — Coding Agent Implementation Brief

**Status:** Initial implementation specification  
**Primary audience:** Coding agent implementing the Go portal repository  
**Date:** 2026-08-09  
**Public hostname:** `https://ai.birks.dev`  
**Repository scope:** Go account/onboarding portal only  
**Cluster/IaC repository:** `dbirks/home-k8s` (Kubernetes manifests, Helm releases, kgateway, vLLM Production Stack, Cloudflare Tunnel, RBAC bindings, etc.)

---

## 1. Mission

Build a small, polished, server-rendered Go web application for `ai.birks.dev` that lets trusted users:

1. Sign in with a Microsoft work account from the owner's Microsoft Entra tenant.
2. See the API keys they have created.
3. Create a new long-lived API key.
4. See the full value of a newly created API key exactly once in the UI.
5. Revoke an existing API key.
6. Get simple copy/paste instructions for using the API with coding agents, initially:
   - Claude Code
   - Pi
   - OpenCode
   - Codex
   - Crush
7. Understand, at a glance, that the endpoint is a self-hosted vLLM service and that the same credential is intended to work across the supported clients.

This app is intentionally **not** an inference proxy and **not** a general identity database.

Human identity belongs to Microsoft Entra ID. API-key persistence belongs to Kubernetes Secrets. API-key enforcement belongs to the Kubernetes gateway layer. Inference belongs to vLLM Production Stack.

The portal is the narrow bridge between an authenticated human and the Kubernetes API.

---

## 2. Product philosophy

Prefer a small, boring, understandable system.

The first version should favor:

- Go standard-library HTTP primitives.
- `html/template` for server-rendered HTML.
- Ordinary HTML forms.
- Plain CSS.
- A very small amount of first-party vanilla JavaScript for copy buttons and lightweight progressive enhancement.
- No React.
- No Node build chain.
- No SPA.
- No client-side auth.
- No application database.
- No Redis.
- No custom Kubernetes CRD.
- No direct etcd access.
- No API-key validation in this service's request path.
- No dependency on Grafana or observability for v1.

The service should remain pleasant to understand by reading the source tree.

HTMX and/or `templ` may be adopted later if a concrete UX or maintainability problem justifies them. Do not introduce them in the first implementation merely because they are available.

---

## 3. System context

The intended broader system is approximately:

```text
                         Microsoft Entra ID
                               |
                         OIDC sign-in
                               |
                               v
                      +------------------+
                      |  ai-account Go   |
                      |  portal          |
                      +--------+---------+
                               |
                        Kubernetes API
                               |
                               v
                    Kubernetes API-key Secrets
                               |
                               | selected/watched by
                               v
Internet -> Cloudflare Tunnel -> kgateway -> vLLM Production Stack -> vLLM engines
```

The public hostname is intended to be:

```text
https://ai.birks.dev
```

Likely routing:

```text
/                       -> Go portal
/login                  -> Go portal
/auth/callback          -> Go portal
/account                -> Go portal
/keys/...               -> Go portal

/v1/models              -> kgateway -> vLLM Production Stack
/v1/chat/completions    -> kgateway -> vLLM Production Stack
/v1/responses           -> kgateway -> vLLM Production Stack
/v1/messages            -> kgateway -> vLLM Production Stack
```

The exact Gateway/HTTPRoute/TrafficPolicy resources do **not** belong in this repository. They belong in `dbirks/home-k8s`.

The portal should nevertheless have a clearly documented integration contract so that the home-k8s repository can configure kgateway correctly.

---

## 4. Non-goals for v1

Do not implement these in the first release unless required to make the core flows work:

- User registration.
- Password storage.
- Password reset.
- Email verification.
- Microsoft Graph integration.
- Per-user billing.
- Per-user token quotas.
- Model-level entitlements.
- Multiple roles.
- Admin UI.
- Grafana embedding.
- Usage dashboards.
- Request/log viewing.
- API-key expiration.
- API-key hashing at rest.
- Multiple identity providers.
- Multiple Microsoft Entra tenants.
- Key rename/edit.
- Key recovery/"show existing key".
- Automated client installation.
- Direct management of vLLM or models.
- Direct management of kgateway policies.
- Direct management of Cloudflare.
- A custom API-key auth middleware for inference requests.

Keep future extension points reasonable, but do not build speculative subsystems.

---

## 5. Identity model

### v1 identity policy

Version 1 is a **single-tenant Microsoft Entra ID web application**.

Any successfully authenticated user from the configured tenant is allowed to use the portal. Do not require explicit Enterprise Application user assignment in application logic.

Do not authorize based on email domain.

Use the stable identity tuple:

```text
tenant ID (`tid`) + object ID (`oid`)
```

as the user's canonical identity.

Treat these as display-only and potentially mutable:

```text
name
preferred_username
email
```

Do not make email the database key, Secret owner key, or authorization key.

### Future direction

A future release may convert the Entra application registration to multi-tenant and allow selected additional Microsoft organizations.

Design configuration and identity types so `TenantID` is always explicit rather than assuming one global tenant forever.

A reasonable future policy would be:

```text
multi-tenant app registration
+
"organizations" Microsoft authority
+
explicit allowlist of accepted tenant IDs
```

Do not implement this in v1, but avoid code that would make it unnecessarily difficult.

### OIDC implementation

Use the Microsoft identity platform's OpenID Connect authorization-code flow for a confidential server-side web application.

Do **not** hand-roll:

- token signature verification,
- OIDC discovery,
- JWKS refresh,
- nonce verification,
- authorization-code exchange.

Use a maintained Go OIDC/OAuth2 library and provider discovery.

The application only needs authentication. It should not request broad Microsoft Graph permissions.

Expected scopes:

```text
openid
profile
email
```

If `email` is absent, that is not an authentication failure.

The public callback should be derived from configuration, e.g.:

```text
https://ai.birks.dev/auth/callback
```

Local development may use an additional redirect URI such as:

```text
http://localhost:8080/auth/callback
```

registered in the Entra app.

### OIDC security requirements

Validate at minimum:

- OAuth `state`.
- OIDC `nonce`.
- ID-token signature through OIDC discovery/JWKS.
- token audience/client ID.
- issuer.
- configured tenant policy.
- token expiration.

Never log raw ID tokens, access tokens, authorization codes, client secrets, or session cookies.

---

## 6. Session model

Prefer stateless or nearly stateless sessions.

A signed and encrypted HTTP-only cookie is appropriate for the small set of claims needed by this portal.

Suggested session payload:

```go
type SessionUser struct {
    TenantID string
    ObjectID string
    Name     string
    Email    string // optional/display only
}
```

The session must not contain API keys.

Cookie requirements:

- `Secure`
- `HttpOnly`
- `SameSite=Lax`
- narrow path where practical
- sensible expiration, e.g. one workday
- cryptographically strong signing/encryption keys
- rotation-friendly key configuration if straightforward

The service will sit behind Cloudflare Tunnel and a Kubernetes gateway. Do not derive critical security behavior from untrusted forwarded headers. Use a configured `PUBLIC_BASE_URL` as the canonical external origin.

Implement logout by clearing the local application session. Entra global logout is optional for v1.

---

## 7. API-key persistence contract

API keys are persisted as Kubernetes `Secret` resources through the Kubernetes API server.

The Go application must **never connect directly to etcd**.

### Secret namespace

The production namespace will be supplied by cluster configuration, likely a dedicated namespace such as:

```text
llm-access
```

The dedicated namespace is an important security boundary because standard Kubernetes RBAC cannot grant Secret access based only on arbitrary labels.

The portal's ServiceAccount should ultimately receive only the Secret permissions it needs in this namespace.

This RBAC is configured in `dbirks/home-k8s`, not this repository.

### One Secret per API key

Use one Secret per generated credential.

Example shape:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: llm-key-<random-id>
  namespace: llm-access
  labels:
    ai.birks.dev/api-key: "true"
    ai.birks.dev/owner-oid: "<entra-object-id>"
    ai.birks.dev/owner-tid: "<entra-tenant-id>"
  annotations:
    ai.birks.dev/display-name: "MacBook Claude Code"
    ai.birks.dev/key-suffix: "A1b2C3"
    ai.birks.dev/owner-display-name: "Alice Example"
    ai.birks.dev/owner-email: "alice@example.com"
    ai.birks.dev/managed-by: "ai-account"
type: extauth.solo.io/apikey
immutable: true
stringData:
  client-<opaque-client-id>: "llm_<cryptographically-random-secret>"
```

Exact metadata prefix/label values should be centralized as constants and configurable only if there is a real deployment need.

### Important kgateway behavior

The gateway is expected to use kgateway's built-in `TrafficPolicy.spec.apiKeyAuth`.

kgateway can source API keys from Kubernetes Secrets selected by label. In each Secret data entry:

- the **data key** is an arbitrary client identifier;
- the **data value** is the credential.

The portal must therefore store the actual generated credential in the Secret. It cannot store only a hash if kgateway built-in API-key auth is being used.

This is an explicit architectural tradeoff.

The cluster must protect these Secrets using:

- etcd Secret encryption at rest;
- least-privilege RBAC;
- a dedicated namespace/blast radius;
- protected backups;
- no credential logging.

The portal UI must still behave as if the credential is unrecoverable: **show it once after creation and never provide a "show key" feature.**

### Immutable Secrets

Set:

```yaml
immutable: true
```

for API-key Secrets.

Keys are not edited in place.

Lifecycle is:

```text
create -> use -> revoke/delete
```

Rotation is:

```text
create replacement -> verify/use replacement -> revoke old key
```

### API-key format

Generate credentials with `crypto/rand`.

Use at least 256 bits of random secret material.

A reasonable human-recognizable format is:

```text
llm_<base64url-without-padding>
```

Example only:

```text
llm_Cy6EwX...random...
```

Do not use UUID alone as the secret.

Do not use `math/rand`.

The exact prefix should be a constant or setting so it can later change without changing the storage interface.

Store only a short suffix (for example the final 6 characters) in Secret metadata for UI identification.

### Secret name and client identifier

Both should be opaque and should not include the person's email address.

Example:

```text
Secret name:
llm-key-01jabc...

kgateway client identifier:
client-u7f12-k9c3
```

The client identifier may later appear in gateway logs. Keep it non-secret and privacy-conscious.

Do not assume client identifiers should become Prometheus labels.

---

## 8. Key ownership and authorization

Every operation on a key must be authorized against the authenticated Entra identity.

For list operations, query using owner labels where possible:

```text
ai.birks.dev/owner-tid=<tid>
ai.birks.dev/owner-oid=<oid>
```

For revoke operations:

1. Parse/validate the requested Secret name.
2. Read the Secret.
3. Compare its owner tenant/object labels with the current authenticated session.
4. Delete only if both match.
5. Otherwise return 404 or 403 without leaking another user's key metadata.

Do not trust a hidden HTML form field containing owner identity.

Do not allow the browser to specify arbitrary Kubernetes namespace, Secret type, labels, or data keys.

The user controls only a friendly key name.

---

## 9. Kubernetes access abstraction

Do not tightly couple HTTP handlers to client-go calls.

Define a small storage interface, e.g.:

```go
type KeyStore interface {
    ListKeys(ctx context.Context, owner OwnerID) ([]KeyMetadata, error)
    CreateKey(ctx context.Context, owner Owner, displayName string) (CreatedKey, error)
    RevokeKey(ctx context.Context, owner OwnerID, keyID string) error
    Ready(ctx context.Context) error
}
```

Provide at least:

```text
internal/keystore/memory
internal/keystore/kubernetes
```

The memory implementation is for:

- local UI development,
- unit tests,
- handler tests,
- safe demo runs.

The Kubernetes implementation is for production.

Default local development should **not accidentally write Secrets to the current kubeconfig context**.

A developer must explicitly select Kubernetes mode.

Suggested setting:

```text
KEYSTORE_MODE=memory|kubernetes
```

In Kubernetes mode:

- prefer in-cluster configuration;
- optionally support a kubeconfig only for deliberate development/testing;
- require an explicit namespace;
- fail fast if required configuration is missing.

---

## 10. User experience

The site should feel intentionally small and polished, not like a Kubernetes admin panel.

### Visual direction

Aim for:

- calm,
- technical,
- minimal,
- highly legible,
- generous whitespace,
- strong code-block presentation,
- excellent dark-mode behavior using `prefers-color-scheme`,
- no dashboard visual clutter,
- no giant marketing hero,
- no gradients required,
- no excessive cards inside cards.

Use system fonts.

Do not load Google Fonts or other third-party frontend resources.

All CSS and JS should be served from the application's own origin, preferably embedded in the Go binary.

### Responsive behavior

The site must work comfortably on:

- desktop,
- laptop,
- phone.

The likely primary use is desktop, but key creation/copying should still be usable on mobile.

### Accessibility

Require:

- semantic HTML,
- keyboard navigation,
- visible focus states,
- proper labels,
- buttons that are buttons,
- sufficient color contrast,
- status/error text not conveyed by color alone,
- confirmation language for destructive actions,
- `aria-live` or equivalent for copy/status feedback where useful.

---

## 11. Page-by-page UX

### 11.1 Landing page (`GET /`)

Unauthenticated state:

```text
ai.birks.dev

Private self-hosted AI endpoint

Use your Microsoft work account to create an API key and
connect Claude Code, Pi, OpenCode, Codex, or Crush.

[ Sign in with Microsoft ]
```

Optionally include a small footer explaining that inference is self-hosted and access is intended for invited/trusted coworkers.

Do not expose infrastructure detail such as home IPs, Kubernetes namespaces, model storage paths, or internal services.

If already authenticated, redirect to `/account`.

### 11.2 Account page (`GET /account`)

Header:

```text
ai.birks.dev

Alice Example
alice@example.com
[Sign out]
```

Main content should have two strong sections.

#### API keys

Example:

```text
API keys                                   [Create API key]

MacBook / Claude Code
Created Aug 9, 2026
Ends in ...A1b2C3                           [Revoke]

Workstation
Created Aug 9, 2026
Ends in ...D4e5F6                           [Revoke]
```

Do not show the full key.

If no keys exist:

```text
You do not have an API key yet.
Create one to connect a coding agent.
[Create API key]
```

#### Connect a coding agent

Present a lightweight picker/tabs/dropdown in this order:

1. Claude Code
2. Pi
3. OpenCode
4. Codex
5. Crush

Each panel should include:

- one-sentence human explanation;
- required config file/environment variables;
- a copyable snippet using `YOUR_API_KEY` or a named env var;
- a "Copy setup" button;
- optionally a second "Copy instructions for my agent" button.

The "copy instructions for my agent" text should be written as a short natural-language prompt that the user can paste into another coding agent, e.g.:

```text
Configure Claude Code on this machine to use my OpenAI/Anthropic-compatible
self-hosted endpoint at https://ai.birks.dev. Use the API key I provide through
an environment variable rather than committing it to a repository. Use model
<configured default model>. Preserve my existing unrelated configuration.
```

Do not make the wink/joke part required, but the tone may be lightly playful.

### 11.3 Create-key flow

A small form:

```text
Create API key

Name this key
[ MacBook Claude Code                  ]

Use a name that helps you remember where the key is stored.

[Cancel] [Create key]
```

Validation:

- required;
- trim whitespace;
- reasonable max length (e.g. 64 characters);
- reject control characters;
- escape output using `html/template`.

### 11.4 One-time key page

After successful creation, return a dedicated response showing the credential once.

Example:

```text
API key created

MacBook Claude Code

llm_........................................
[Copy key]

Save this key now. It will not be shown here again.
If you lose it, create a replacement and revoke this key.

Quick start
[Claude Code] [Pi] [OpenCode] [Codex] [Crush]

<snippet pre-filled or parameterized with the newly created key>

[Copy setup]

[Done]
```

Security headers on this response are especially important:

```http
Cache-Control: no-store
Pragma: no-cache
Referrer-Policy: no-referrer
```

Do not place the credential in:

- URL query strings,
- URL fragments,
- redirects,
- analytics events,
- logs,
- HTML element IDs,
- DOM data attributes unrelated to the visible secret,
- third-party scripts.

The page may contain the key in its rendered HTML because showing it is the purpose of the page. It must not load third-party assets.

### 11.5 Revoke flow

Use an ordinary POST form.

A destructive operation should have a lightweight confirmation step.

Example:

```text
Revoke "MacBook Claude Code"?

Anything using this key will stop authenticating.

[Cancel] [Revoke key]
```

After success, redirect to `/account` with a short flash-style success message.

Do not implement restore.

---

## 12. Why POST forms reload pages — and what to do

A full page reload is completely acceptable for v1.

Use the Post/Redirect/Get pattern for normal forms where possible.

For the one-time API-key response, returning the creation result directly from the POST is acceptable because redirecting would require temporarily persisting the cleartext key somewhere else.

The site should work fully without JavaScript.

A small first-party JS file may add:

- clipboard copy buttons,
- agent setup tabs/picker,
- nonessential visual feedback.

Do not require HTMX in v1.

Later, HTMX could enhance operations such as revocation without a full document navigation, while retaining server-rendered HTML endpoints.

---

## 13. CSRF and browser security

All state-changing browser requests require CSRF protection:

```text
POST /keys
POST /keys/{id}/revoke
POST /logout
```

Use a maintained CSRF mechanism or a correct synchronizer/double-submit pattern.

Set a strict Content Security Policy appropriate for a first-party server-rendered site, for example conceptually:

```text
default-src 'self';
script-src 'self';
style-src 'self';
img-src 'self' data:;
connect-src 'self';
frame-ancestors 'none';
base-uri 'self';
form-action 'self' https://login.microsoftonline.com;
```

Adjust to the actual OIDC/form behavior and test it rather than copying blindly.

Also consider:

```text
X-Content-Type-Options: nosniff
Referrer-Policy: no-referrer
Permissions-Policy: ...
```

Do not add deprecated headers merely for checklist completeness.

Avoid inline JavaScript so CSP can remain simple.

---

## 14. Logging requirements

Use structured logs.

Useful fields:

```text
request_id
method
route
status
duration
entra_tid
entra_oid
operation
key_resource_name
```

Do **not** log:

```text
Authorization
X-Api-Key
Cookie
Set-Cookie
OIDC authorization code
access token
ID token
Entra client secret
generated API key
Kubernetes Secret data
request bodies containing secrets
```

Avoid logging full raw query strings.

Use the friendly key name only when appropriate; it may contain user-provided text.

Do not log email by default if `tid` + `oid` is sufficient.

---

## 15. Health endpoints

Provide unauthenticated internal health endpoints suitable for Kubernetes probes:

```text
GET /healthz
GET /readyz
```

`/healthz`:

- confirms the process is alive.

`/readyz`:

- confirms required application configuration is loaded;
- confirms the configured KeyStore is usable;
- should not mutate data;
- should fail when Kubernetes access is unavailable in production mode.

Do not expose secrets or dependency details in the response.

Simple bodies are enough:

```text
ok
```

---

## 16. Suggested Go structure

Keep the package graph small.

One possible layout:

```text
.
├── cmd/
│   └── ai-account/
│       └── main.go
├── internal/
│   ├── auth/
│   │   ├── oidc.go
│   │   ├── session.go
│   │   └── middleware.go
│   ├── config/
│   │   └── config.go
│   ├── httpapp/
│   │   ├── handlers.go
│   │   ├── routes.go
│   │   ├── middleware.go
│   │   └── viewmodels.go
│   ├── keystore/
│   │   ├── keystore.go
│   │   ├── memory/
│   │   └── kubernetes/
│   └── onboarding/
│       ├── clients.go
│       └── snippets.go
├── web/
│   ├── templates/
│   └── static/
├── Dockerfile
├── go.mod
├── go.sum
├── README.md
└── .github/
    └── workflows/
```

Embedding templates/static assets with `//go:embed` is encouraged so the runtime container contains one application binary plus CA certificates.

Do not turn every tiny concept into its own package.

---

## 17. Configuration

Prefer environment variables for deployment-time configuration.

Suggested initial set:

```text
PORT=8080
PUBLIC_BASE_URL=https://ai.birks.dev

ENTRA_TENANT_ID=<uuid>
ENTRA_CLIENT_ID=<uuid>
ENTRA_CLIENT_SECRET=<secret>

SESSION_KEY=<strong-random-secret>

KEYSTORE_MODE=memory|kubernetes
KUBERNETES_NAMESPACE=llm-access

API_KEY_PREFIX=llm_
DEFAULT_MODEL=<served-model-name>
```

Optional/future:

```text
ALLOWED_ENTRA_TENANTS=...
AVAILABLE_MODELS=...
LOG_LEVEL=...
```

Fail fast on invalid production configuration.

Never provide a production default for:

```text
ENTRA_CLIENT_SECRET
SESSION_KEY
```

The Entra client secret and session key are deployment Secrets managed in `dbirks/home-k8s`, not API-key Secrets managed by the portal.

---

## 18. Client onboarding contract

The portal should model client setup instructions as data, not as a giant conditional embedded in templates.

For example:

```go
type ClientGuide struct {
    ID          string
    Name        string
    Description string
    Files       []ConfigFile
    Commands    []CodeBlock
    AgentPrompt string
    Notes       []string
}
```

Keep each integration independently testable.

The snippets below are **implementation guidance as of August 2026**. Before shipping the page, verify each against the latest upstream documentation. Client configuration changes more quickly than the portal architecture.

### 18.1 Claude Code

vLLM implements the Anthropic Messages API and documents direct Claude Code integration.

The public endpoint should expose:

```text
POST https://ai.birks.dev/v1/messages
```

Preferred setup should use a gateway bearer token:

```bash
export ANTHROPIC_BASE_URL="https://ai.birks.dev"
export ANTHROPIC_AUTH_TOKEN="$BIRKS_AI_API_KEY"

export ANTHROPIC_DEFAULT_OPUS_MODEL="<DEFAULT_MODEL>"
export ANTHROPIC_DEFAULT_SONNET_MODEL="<DEFAULT_MODEL>"
export ANTHROPIC_DEFAULT_HAIKU_MODEL="<DEFAULT_MODEL>"

claude
```

Depending on current Claude Code/vLLM behavior, setting `ANTHROPIC_API_KEY` as well may be required or useful. Verify this during implementation.

Anthropic documents `ANTHROPIC_AUTH_TOKEN` for gateway bearer authentication. vLLM's current Claude Code integration documents `ANTHROPIC_BASE_URL`, `ANTHROPIC_AUTH_TOKEN`, and the default Opus/Sonnet/Haiku model mappings.

### 18.2 Pi

Pi supports custom providers in:

```text
~/.pi/agent/models.json
```

It supports OpenAI-compatible APIs and can automatically emit:

```http
Authorization: Bearer <apiKey>
```

when `authHeader` is enabled.

A likely setup shape:

```json
{
  "providers": {
    "birks-ai": {
      "baseUrl": "https://ai.birks.dev/v1",
      "api": "openai-completions",
      "apiKey": "BIRKS_AI_API_KEY",
      "authHeader": true,
      "models": [
        { "id": "<DEFAULT_MODEL>" }
      ]
    }
  }
}
```

Verify whether the target model works best through Pi's `openai-completions`, Responses-compatible mode, or Anthropic Messages mode before finalizing the production snippet.

Do not put the literal API key in committed project files.

### 18.3 OpenCode

Current OpenCode supports custom OpenAI-compatible providers and bearer authentication when a provider API key is available.

A likely current configuration uses a custom provider with:

```text
baseURL = https://ai.birks.dev/v1
API key from an environment variable
explicit model map
```

The exact OpenCode configuration schema has changed across releases. Use the current official `opencode.ai` documentation at implementation time and snapshot-test the generated example.

The UX should favor an environment variable such as:

```bash
export BIRKS_AI_API_KEY="..."
```

rather than embedding the key in `opencode.json`.

### 18.4 Codex

Current Codex supports custom providers in:

```text
~/.codex/config.toml
```

vLLM officially documents Codex through its OpenAI Responses API.

Expected setup:

```toml
model = "<DEFAULT_MODEL>"
model_provider = "birks-ai"

[model_providers.birks-ai]
name = "Birks AI"
env_key = "BIRKS_AI_API_KEY"
base_url = "https://ai.birks.dev/v1"
wire_api = "responses"
```

Then:

```bash
export BIRKS_AI_API_KEY="..."
codex
```

As of current Codex configuration documentation, custom provider `wire_api` is Responses-only.

### 18.5 Crush

Crush supports custom `openai-compat` and `anthropic` providers.

Prefer an OpenAI-compatible configuration unless testing shows the Anthropic route is materially better for the chosen model.

Likely shape:

```json
{
  "$schema": "https://charm.land/crush.json",
  "providers": {
    "birks-ai": {
      "type": "openai-compat",
      "base_url": "https://ai.birks.dev/v1",
      "api_key": "$BIRKS_AI_API_KEY",
      "models": [
        {
          "id": "<DEFAULT_MODEL>",
          "name": "<DEFAULT_MODEL>"
        }
      ]
    }
  }
}
```

Crush's OpenAI-compatible provider sends bearer authentication.

---

## 19. Gateway compatibility assumptions

The portal repository does not deploy kgateway, but its generated credential shape depends on kgateway's contract.

The home-k8s configuration is expected to configure kgateway API-key authentication roughly around:

- `TrafficPolicy.spec.apiKeyAuth`
- a `secretSelector` matching `ai.birks.dev/api-key=true`
- key sources that cover the client wire formats
- a `clientIdHeader` for future request attribution
- default `forwardCredential: false` unless downstream auth requires otherwise

Important integration test:

Verify a single generated Secret/API key works with:

| Client/wire style | Expected credential wire format |
|---|---|
| OpenAI-compatible SDKs | `Authorization: Bearer <key>` |
| Claude Code gateway mode | `Authorization: Bearer <key>` |
| Anthropic-compatible clients | `X-Api-Key: <key>` where needed |
| curl | both configured forms |

Do not assume this compatibility merely from documentation. Test it against the exact kgateway release deployed in `home-k8s`.

The portal itself should not transform inference requests.

---

## 20. Public route assumptions

The gateway/IaC repository should publicly expose only intended inference routes.

The portal may document these expected routes:

```text
GET  /v1/models
POST /v1/chat/completions
POST /v1/responses
POST /v1/messages
```

Potential later routes such as embeddings can be added intentionally.

Do not teach users to call vLLM management endpoints.

Do not expose management paths such as sleep/wake/engine-control routes through the public API.

---

## 21. Cloudflare/public-origin assumptions

The likely deployment path is:

```text
Internet
  -> Cloudflare edge
  -> Cloudflare Tunnel
  -> cloudflared in Kubernetes
  -> kgateway
  -> portal or inference route
```

The application should assume the canonical public URL is configured as:

```text
https://ai.birks.dev
```

It does not need to:

- obtain a Let's Encrypt certificate;
- call Cloudflare APIs;
- manage DNS;
- terminate public TLS itself.

Those are deployment concerns in `dbirks/home-k8s`/Cloudflare.

Never make the application depend on the user's residential IP.

---

## 22. Local development

The repository must be enjoyable to run locally.

Desired flow:

```bash
go run ./cmd/ai-account
```

with:

```text
KEYSTORE_MODE=memory
PUBLIC_BASE_URL=http://localhost:8080
```

There are two acceptable authentication development modes:

### Preferred integration mode

Use real Entra OIDC with a registered localhost callback:

```text
http://localhost:8080/auth/callback
```

This exercises the real login flow.

### Unit-test/fake mode

Provide auth interfaces that handlers can test using a fake authenticated user.

If an explicit development-login bypass is added for manual UI work, it must:

- be disabled by default;
- be impossible to enable accidentally in production;
- display a conspicuous development-only banner;
- never be presented as a production feature.

Do not require a Kubernetes cluster for routine frontend development.

---

## 23. Error handling

User-facing errors should be short and actionable.

Examples:

```text
We couldn't sign you in.
Try again, or contact the service owner if the problem continues.
```

```text
We couldn't create your API key.
Nothing was changed. Try again.
```

```text
That API key no longer exists.
```

Do not show:

- Go stack traces,
- Kubernetes API objects,
- Entra token bodies,
- internal Service names,
- raw OIDC errors containing sensitive values.

Server logs can contain the safe operational error with request ID.

Add a request ID to error pages so debugging can correlate a browser error with logs.

---

## 24. Tests

### Unit tests

Cover:

- API-key randomness/format.
- label/annotation construction.
- owner-label filtering.
- key-name validation.
- display-name validation.
- suffix generation.
- session encode/decode.
- auth middleware.
- CSRF enforcement.
- unauthorized access redirects.
- another user cannot revoke a key.
- another tenant with same `oid` cannot own the same identity.
- HTML escaping.
- onboarding snippet generation.

### Handler tests

Use `httptest` with memory KeyStore and fake auth/session.

Cover:

```text
GET /
GET /account
POST /keys
POST /keys/{id}/revoke
GET /healthz
GET /readyz
```

Check important response headers, especially on the one-time key page.

### Kubernetes adapter tests

Test the generated `Secret` object shape.

A fake Kubernetes client is sufficient for most repository-level tests, while remembering that fake clients do not validate real RBAC behavior.

### Security regression tests

Explicitly assert that generated API-key values do not appear in:

- normal post-create redirects,
- key listing HTML,
- structured logs produced by tested handlers,
- error responses.

### Client guide tests

Use snapshot/golden tests for generated setup snippets.

This makes future upstream syntax updates easy to review in pull requests.

---

## 25. Build and container image

Produce a small, non-root OCI image suitable for GHCR.

Requirements:

- multi-stage Go build;
- runtime contains CA certificates for Entra HTTPS;
- run as non-root;
- listen on configurable port (default 8080);
- no shell required at runtime;
- static/templates embedded if practical;
- graceful HTTP server shutdown on SIGTERM;
- reasonable server read/header/write/idle timeouts;
- no credentials baked into image layers.

Publishing target will be GHCR.

The exact repository/image name may be decided when the repo is created, but design CI so the image can be consumed from:

```text
ghcr.io/dbirks/<repo>:<tag>
```

---

## 26. GitHub Actions

At minimum:

### Pull requests

Run:

```text
go test ./...
go vet ./...
```

Also consider:

```text
govulncheck ./...
staticcheck ./...
```

Use current stable action versions and pin appropriately.

### Main/tags

Build and publish the OCI image to GHCR.

Prefer immutable SHA tags in addition to any convenience tag.

Optional but desirable:

- amd64 + arm64 image;
- SBOM/provenance;
- Dependabot/Renovate for Go modules and Actions.

Do not make an overly elaborate release system for the first version.

---

## 27. README requirements

The repo README should explain:

1. What the portal does.
2. What it intentionally does not do.
3. Architecture/context.
4. Local run instructions.
5. Entra app-registration requirements.
6. Required environment variables.
7. Memory vs Kubernetes KeyStore.
8. Kubernetes Secret contract.
9. Security considerations.
10. Container build/run.
11. Link/note that deployment manifests belong in `dbirks/home-k8s`.

The README should not duplicate every detail from this brief, but it should be enough for a future maintainer to run the service.

---

## 28. Security checklist

Before declaring v1 done:

- [ ] Entra OIDC uses authorization-code flow and validates ID tokens.
- [ ] Tenant ID and object ID are the canonical user identity.
- [ ] Email is not an authorization identifier.
- [ ] OAuth state and OIDC nonce are validated.
- [ ] Session cookie is Secure, HttpOnly, SameSite.
- [ ] CSRF is enforced on mutations.
- [ ] API keys use at least 256 bits from `crypto/rand`.
- [ ] API key is never placed in URLs.
- [ ] API key is shown once after create and not shown on listings.
- [ ] One-time key response has `Cache-Control: no-store`.
- [ ] No third-party page assets.
- [ ] No secret-bearing request/response logging.
- [ ] User cannot delete another user's key.
- [ ] Kubernetes adapter creates immutable Secrets.
- [ ] Service has no update/patch API for credential Secrets.
- [ ] Production KeyStore uses Kubernetes API, not etcd directly.
- [ ] Local dev defaults to memory store.
- [ ] Production fails fast if auth/session secrets are missing.
- [ ] Health endpoints reveal no secret/internal detail.
- [ ] Container runs non-root.
- [ ] Dependency/security scans are clean.
- [ ] Exact kgateway bearer/X-Api-Key behavior is integration-tested in home-k8s.
- [ ] Cluster uses encryption at rest for Kubernetes Secrets (deployment responsibility).
- [ ] Portal ServiceAccount receives least-privilege Secret RBAC (deployment responsibility).

---

## 29. Acceptance criteria for the first useful release

A first release is successful when this flow works end-to-end:

1. A coworker opens `https://ai.birks.dev`.
2. They click **Sign in with Microsoft**.
3. They authenticate using an account in the configured Company A Entra tenant.
4. The portal displays their account page.
5. They click **Create API key**.
6. They name it `MacBook Claude Code`.
7. The Go service creates an immutable Kubernetes Secret matching the kgateway API-key selector contract.
8. The portal shows the generated key once.
9. They select **Claude Code** and copy the setup snippet.
10. Claude Code sends an authenticated request through `ai.birks.dev` and reaches vLLM.
11. Returning to `/account` later shows the key name, created time, and suffix but not the cleartext key.
12. They revoke the key.
13. The corresponding Kubernetes Secret is deleted.
14. The old credential stops authenticating through kgateway after normal propagation/watch delay.
15. The user cannot access or revoke keys belonging to a different Entra `tid`/`oid`.

The same credential should subsequently be proven with Pi, OpenCode, Codex, and Crush.

---

## 30. Recommended implementation order

### Milestone 1 — skeleton/UI

- Go HTTP server.
- embedded templates/static assets.
- landing page.
- account page with fake user.
- memory KeyStore.
- create/revoke flows.
- one-time key UX.
- client-guide UI.
- health endpoints.

### Milestone 2 — Entra

- OIDC provider discovery.
- login/callback.
- secure session.
- logout.
- CSRF/security headers.
- real tenant/object identity.

### Milestone 3 — Kubernetes

- client-go KeyStore.
- exact immutable Secret contract.
- owner filtering/authorization.
- explicit production configuration.
- unit/integration tests.

### Milestone 4 — onboarding correctness

- verify Claude Code configuration against current Anthropic + vLLM docs.
- verify Pi against current Pi docs.
- verify OpenCode against current official OpenCode docs.
- verify Codex against current OpenAI + vLLM docs.
- verify Crush against current Charmbracelet docs/source.
- golden-test snippets.

### Milestone 5 — delivery

- Dockerfile.
- GHCR workflow.
- README.
- production hardening review.

Actual `home-k8s` work can then wire the image into Entra secrets, RBAC, kgateway, Cloudflare Tunnel, and vLLM Production Stack.

---

## 31. Implementation guidance: things to keep boring

A coding agent should actively resist these temptations in v1:

- Don't add React.
- Don't add an ORM.
- Don't create a SQL database.
- Don't create a `User` table.
- Don't mirror Entra users.
- Don't write an OAuth server.
- Don't write an API gateway.
- Don't create a Kubernetes operator.
- Don't create a CRD.
- Don't implement API-key validation for inference traffic.
- Don't proxy `/v1/*` through this Go service.
- Don't introduce a message queue.
- Don't add a JS framework only to make a tab switch.
- Don't persist cleartext keys outside Kubernetes Secrets.
- Don't make email the identity.
- Don't make the portal an admin plane for vLLM.

The ideal reaction from a maintainer opening this repository should be:

> "Oh, this is just a small Go website that signs people in and creates/revokes the right Secrets."

---

## 32. Source notes for the implementer

These are the most relevant current upstream references to re-check while implementing.

### Microsoft identity

- Microsoft OAuth 2.0 authorization-code flow:  
  https://learn.microsoft.com/en-us/entra/identity-platform/v2-oauth2-auth-code-flow
- Microsoft OpenID Connect:  
  https://learn.microsoft.com/en-us/entra/identity-platform/v2-protocols-oidc
- Converting an Entra app to multi-tenant later:  
  https://learn.microsoft.com/en-us/entra/identity-platform/howto-convert-app-to-be-multi-tenant

### Kubernetes Secrets

- Secret security practices / encryption at rest / least privilege:  
  https://kubernetes.io/docs/concepts/security/secrets-good-practices/

### kgateway

- Current kgateway docs (2.4.x at time of this brief):  
  https://kgateway.dev/docs/envoy/latest/
- API-key authentication:  
  https://kgateway.dev/docs/envoy/latest/security/extauth/apikey/
- API reference for `APIKeyAuth`:  
  https://kgateway.dev/docs/envoy/latest/reference/api/

### vLLM

- Claude Code integration:  
  https://docs.vllm.ai/en/stable/serving/integrations/claude_code/
- Codex integration:  
  https://docs.vllm.ai/en/stable/serving/integrations/codex/
- vLLM server/compatible APIs:  
  https://docs.vllm.ai/en/stable/

### Claude Code

- Anthropic LLM gateway configuration (`ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_BASE_URL`):  
  https://docs.anthropic.com/en/docs/claude-code/llm-gateway

### Codex

- Current Codex config reference:  
  https://developers.openai.com/codex/config-reference/

### Pi

- Custom models/providers documentation:  
  https://github.com/badlogic/pi-mono/blob/main/packages/coding-agent/docs/models.md

### OpenCode

- Current provider documentation:  
  https://opencode.ai/v2/docs/providers

### Crush

- Repository/configuration documentation:  
  https://github.com/charmbracelet/crush
- Current provider config/schema/source should be verified before shipping generated snippets.

---

## 33. Final instruction to the coding agent

Implement the smallest secure version of this product that satisfies the acceptance criteria.

When a choice exists between a clever abstraction and a straightforward Go implementation, prefer the straightforward implementation.

Keep identity, credential persistence, credential enforcement, and inference as separate responsibilities:

```text
Entra ID             -> who is the human?
Go portal            -> may this human create/revoke their credentials?
Kubernetes Secrets   -> where are credentials stored?
kgateway              -> is this API request carrying a valid credential?
vLLM Production Stack -> where should the inference request go?
```

That separation is the core of the design.
