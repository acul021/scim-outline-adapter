# scim-outline-adapter

A SCIM 2.0 adapter for [Outline](https://www.getoutline.com/), e.g. for use with
[authentik](https://goauthentik.io/)'s SCIM provider. It exposes a SCIM 2.0
(RFC 7643/7644) API and translates every request into calls against Outline's
REST API, so an identity provider can provision users and groups — including
Outline team roles derived from group membership — into an Outline instance.

The SCIM resource `id` is the Outline UUID for both users and groups, and every
lookup is resolved against Outline directly. By default the adapter keeps no
local state. The one exception is the optional user `externalId` mapping (see
[User externalId mapping](#user-externalid-mapping)), which SCIM clients like
Pocket ID need.

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
| `USER_EXTERNAL_ID_FILE` | no | — | Path to a JSON file for the user `externalId` mapping. Unset disables the mapping. Required for Pocket ID, see below. |
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

### User externalId mapping

Outline has no field for a user `externalId`. Without the mapping the adapter
drops it on write and omits it on read. authentik does not care, because it
remembers the returned `id`.

Pocket ID does care. It keeps no record of remote ids and matches users only by
`externalId`. It sends `DELETE` for every user in `GET /Users` whose
`externalId` it does not know, and it skips groups whose members it cannot
resolve that way. Without the mapping, a Pocket ID sync suspends every Outline
user and creates no groups.

Set `USER_EXTERNAL_ID_FILE` to turn the mapping on. The adapter then stores
Outline id → `externalId` in that file:

- `POST /Users` (including adoption by email), `PUT` and `PATCH` store the
  `externalId` the client sends. A request without `externalId` leaves an
  existing entry alone.
- Responses include the stored `externalId`, and `GET /Users?filter=externalId
  eq "..."` resolves through the mapping.
- Hard delete (`HARD_DELETE_USERS=true`) removes the entry. Suspending keeps it,
  so a reactivated user is found again.
- Users without an entry, e.g. created before the mapping was enabled, have no
  `externalId`. Their Outline id stays the only key. They get an entry once a
  client creates them with an `externalId`, which adopts the account by email.

Missing parent directories are created at startup. The file must be on
persistent storage. If it is lost, the next Pocket ID sync
sees no known users and suspends all of them.

Things to know about Pocket ID:

- Pocket ID treats its own user list as complete. Outline users that do not
  exist in Pocket ID, or are not allowed for the OIDC client, get suspended.
  That includes the account that owns `OUTLINE_TOKEN`, unless it exists in
  Pocket ID too. Outline refuses to suspend the token owner (403).
- Outline groups without a matching `externalId` get deleted, including groups
  created by hand.
- On the first sync Pocket ID lists existing users before adopting them, so
  those users are suspended once and reactivated on the next sync.

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
  store an `externalId` for users, so without `USER_EXTERNAL_ID_FILE` it is
  omitted from user responses; authentik tolerates this. Group `externalId`
  **is** stored in Outline and round-tripped.
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

With the user `externalId` mapping, mount a volume for the file. The image ships
`/data` owned by the non-root user (UID 10001), and a named volume mounted there
inherits that ownership. A bind mount needs `chown 10001:10001` on the host:

```sh
docker run --rm -p 8080:8080 \
  -e SCIM_TOKEN=... -e OUTLINE_URL=https://wiki.example.com -e OUTLINE_TOKEN=... \
  -e USER_EXTERNAL_ID_FILE=/data/user-external-ids.json \
  -v scim-outline-data:/data \
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
