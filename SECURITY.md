# Security

## Reporting a vulnerability

Please report security issues privately through GitHub's
[private vulnerability reporting](https://github.com/dbirks/kubernetes-llm-api-key-portal/security/advisories/new)
rather than opening a public issue.

Include what you did, what happened, and what you expected. A proof of concept helps but is not
required.

This is a personal project, not a commercial product, so there is no formal response-time
commitment and no bug bounty.

## Scope

This repository is the account portal only. Issues in the surrounding deployment — kgateway
policies, Cloudflare Tunnel, vLLM, cluster RBAC — belong to the cluster repository, not here.

## Design decisions that look like bugs

Two properties are deliberate, and are documented at length in the README. Reports about them are
welcome as discussion, but they are known trade-offs rather than oversights.

**API-key credentials are stored in cleartext in Kubernetes Secrets.** kgateway's built-in API-key
authentication compares a presented credential against the value in the Secret, so a hash cannot be
substituted without abandoning built-in auth and running a custom auth service. The compensating
controls are etcd encryption at rest, least-privilege RBAC, a dedicated namespace, and no
credential logging. The portal still treats the credential as unrecoverable: it is shown once and
never retrievable afterwards.

**CSRF protection allows requests carrying neither `Sec-Fetch-Site` nor `Origin`.** This is the
documented behaviour of `net/http.CrossOriginProtection`, which treats such requests as same-origin
or non-browser. Every browser has sent `Sec-Fetch-Site` since 2023, and this application has no
non-browser API surface, so the residual exposure is limited to pre-2023 browsers.

## What is already covered by tests

Regression tests assert that a generated credential never appears in a redirect, cookie, HTML
attribute, key listing, or log line; that a user cannot read or revoke another user's key; that two
users sharing an object ID in different Entra tenants are treated as different people; and that
cross-origin state changes are rejected. If you find a way around any of these, that is a bug worth
reporting.
