# scim-outline-adapter

A SCIM 2.0 adapter for [Outline](https://www.getoutline.com/), e.g. for use with
[authentik](https://goauthentik.io/)'s SCIM provider. It exposes a SCIM 2.0
(RFC 7643/7644) API and translates every request into calls against Outline's
REST API, so an identity provider can provision users and groups — including
Outline team roles derived from group membership — into an Outline instance.

There is **no local state**: the SCIM resource `id` is the Outline UUID for both
users and groups. Every lookup is resolved against Outline directly.

## How it works

- **Users** are provisioned via Outline invites. The invited account is claimed
  later by the person via OIDC email match; SCIM only creates/updates/suspends it.
- **Groups** map 1:1 to Outline groups. `externalId` is stored on Outline groups
  and round-trips through SCIM.
- **Team roles** (`admin` / `member` / `viewer`) are *derived* from group
  membership using the role map below. A user's role is recomputed after any
  membership change that affects them, and applied only when it actually differs.
  Users not touched by a request are never modified.

## Configuration (environment variables)

| Variable | Required | Default | Description |
|---|---|---|---|
| `LISTEN_ADDR` | no | `:8080` | Address the HTTP server binds to. |
| `SCIM_TOKEN` | **yes** | — | Static bearer token the SCIM client must present. Compared in constant time. |
| `OUTLINE_URL` | **yes** | — | Outline instance base URL, e.g. `https://wiki.example.com`. |
| `OUTLINE_TOKEN` | **yes** | — | Outline API token with admin rights. |
| `ROLE_MAP_ADMIN` | no | — | Comma-separated SCIM group displayNames whose members become Outline `admin`. |
| `ROLE_MAP_MEMBER` | no | — | Comma-separated group displayNames whose members become Outline `member`. |
| `ROLE_MAP_VIEWER` | no | — | Comma-separated group displayNames mapped to `viewer` (the default anyway). |
| `HARD_DELETE_USERS` | no | `false` | If `false`, `DELETE /Users/{id}` and `active=false` suspend the user. If `true`, `DELETE` permanently deletes. |
| `SUPPRESS_INVITE_EMAILS` | no | `true` | Ask Outline not to send invite mails for SCIM-provisioned users (accounts are claimed via SSO email match anyway). Set to `false` to restore Outline's default invite mail. |

### Role mapping

For each user affected by a membership change, the adapter recomputes the role:

1. member of any `ROLE_MAP_ADMIN` group → `admin`
2. else member of any `ROLE_MAP_MEMBER` group → `member`
3. else → `viewer` (the default; `ROLE_MAP_VIEWER` is accepted for documentation/parity)

Admin membership wins over member, which wins over the viewer default.
Matching is case-insensitive.

Example configuration (organisation-specific group names are only an example):

```sh
ROLE_MAP_ADMIN="Admins"
ROLE_MAP_MEMBER="Editors,Staff"
ROLE_MAP_VIEWER="Viewers"
```

## SCIM endpoints

All endpoints are served under `/scim/v2` and require
`Authorization: Bearer <SCIM_TOKEN>`. Responses use `application/scim+json`;
requests are accepted with either `application/scim+json` or `application/json`.

- `GET /ServiceProviderConfig` — advertises `patch` and `filter` as supported;
  `bulk`, `sort`, `etag`, `changePassword` as not supported.
- `Users`
  - `POST /Users` — create (idempotent: adopts an existing Outline account with
    the same email and returns it with `200`).
  - `GET /Users` — list; supports `filter` for `userName eq`, `emails.value eq`,
    and `externalId eq` (equality only), plus `startIndex`/`count` pagination.
  - `GET /Users/{id}`, `PUT /Users/{id}`, `PATCH /Users/{id}` (at least `active`
    and simple attribute replaces), `DELETE /Users/{id}`.
- `Groups`
  - `POST /Groups` — create (idempotent: adopts an existing group by displayName).
  - `GET /Groups` — list; supports `filter` for `displayName eq` / `externalId eq`.
  - `GET /Groups/{id}`, `PUT /Groups/{id}` (rename + full member reconcile),
    `PATCH /Groups/{id}` (`add`/`remove`/`replace` on `members`), `DELETE /Groups/{id}`.

Member `value`s are SCIM user ids, i.e. Outline user UUIDs.

## authentik provider settings

Configure an authentik **SCIM provider** with:

- **URL**: `https://<host>/scim/v2`
- **Token**: the value of `SCIM_TOKEN`

Compatibility notes:

- The adapter matches users on `id` (the Outline UUID). Outline does **not**
  store an `externalId` for users, so `externalId` is omitted from user
  responses; authentik tolerates this. Group `externalId` **is** stored and
  round-tripped.
- `userName` equals the Outline email and is treated as immutable via PATCH.
- authentik's usual sequence (POST create, PUT full-update, PATCH membership
  deltas, `active=false` to deactivate) is supported.

## Container image

Every push to `main` publishes `ghcr.io/acul021/scim-outline-adapter:latest`;
version tags (`vX.Y.Z`) additionally publish `:X.Y.Z` and `:X.Y`
(linux/amd64 + linux/arm64, built `FROM scratch`, runs as non-root):

```sh
docker run --rm -p 8080:8080 \
  -e SCIM_TOKEN=... -e OUTLINE_URL=https://wiki.example.com -e OUTLINE_TOKEN=... \
  ghcr.io/acul021/scim-outline-adapter:latest
```

The Dockerfile is self-contained (multi-stage: build in `golang:alpine`, final
image `FROM scratch` with only CA certificates, tzdata, and the static binary),
so `docker build .` works without any pre-build step.

## Development

```sh
go build ./...
go vet ./...
go test ./...
```

Run locally:

```sh
SCIM_TOKEN=devtoken \
OUTLINE_URL=https://wiki.example.com \
OUTLINE_TOKEN=ol_api_xxx \
ROLE_MAP_ADMIN="Admins" \
ROLE_MAP_MEMBER="Editors,Staff" \
ROLE_MAP_VIEWER="Viewers" \
go run ./application
```

## License

MIT — see [LICENSE](LICENSE).
