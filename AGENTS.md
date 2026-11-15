# katalog-api

katalog-api is the read side of the catalog: a Go REST API over the catalog's
PostgreSQL tables, which katalog-manager owns and migrates. It only runs
SELECTs. chino-api calls it with the viewer's bearer for browsing, search, a
title's detail, people and what a capped viewer may see; chino-stream for where
a title's package and original are (`/api/v1/items/{id}/playback`, the asset
routes); an addon's service account for what the library holds by identity
(`/api/v1/holdings`). It is reached only inside the platform. The surface is
`api/openapi.yaml`.
How a change is proposed and what a pull request has to pass is in the
[guide to contributing](https://github.com/zaentrum/.github/blob/main/CONTRIBUTING.md);
what may be built is in [building from the specification](https://github.com/zaentrum/zaentrum/blob/main/AGENTS.md).

## Build

```sh
go build ./...
docker build -t katalog-api .
```

It needs Go 1.23 or newer (`go.mod`; its `toolchain` line names go1.24.1).
The image builds the binary in `golang:1.24-alpine` and runs it on
`gcr.io/distroless/static-debian12:nonroot` as a non-root user, on port 8080.
`--build-arg BASE=<registry>/library/` pulls the build image from a mirror; the
runtime image is the same whatever `BASE` says.

## Test

```sh
go vet ./...
KATALOG_API_TEST_DATABASE_URL='postgres://postgres@127.0.0.1:5432/postgres?sslmode=disable' go test ./...
```

The workflow `.github/workflows/build.yml` runs `go build ./...`, `go vet
./...` and these tests on every pull request and on main, against a
`postgres:16` service that trusts every connection. The store and handler
tests need a PostgreSQL with the `unaccent` and `pg_trgm` extensions
(contrib) in which they may create and drop schemas and roles: each makes a
schema of its own holding the catalog's tables before and after the
migrations it reads, and some sign in as read-only roles they create without
a password. Without `KATALOG_API_TEST_DATABASE_URL` they are skipped, 97 of
the 107 tests. The README has the commands for a throwaway server.

## Run it

```sh
KATALOG_API_PG_URL='postgres://postgres@127.0.0.1:5432/katalog?sslmode=disable' \
KATALOG_API_OIDC_ISSUER=https://sso.example.org/realms/zaentrum go run ./cmd/server
curl -fsS http://localhost:8080/api/v1/packaged-ids
```

It reads `KATALOG_API_ADDR` (`:8080`), `KATALOG_API_PG_URL` (empty: it runs
with no database), `KATALOG_API_OIDC_ISSUER` and `KATALOG_API_OIDC_AUDIENCE`
(`katalog`, or a comma-separated list). The database is a catalog: the base
schema of the platform's chart with katalog-manager's migrations, which
katalog-manager applies when it starts on it (katalog-manager's `AGENTS.md`
makes one). The catalog's routes (lists, search, items, people, holdings) take
a bearer of the issuer for one of the audiences, and nothing turns that off;
while the issuer cannot be reached they answer 503. The playback and asset
routes, `/api/v1/packaged-ids` and `/api/v1/visible` take no bearer. The
reference demo runs katalog-api inside its cluster with no route out
([how it is deployed](https://github.com/zaentrum/zaentrum/blob/main/docs/reference-demo.md)),
so a local one reads a local catalog.

## What the tests leave unchecked

- The real schema: the tests build the catalog from a copy of
  katalog-manager's base schema and migrations in `internal/store/storetest`.
  A change to katalog-manager's migrations that the copy does not have is not
  seen.
- A real identity provider: tokens come from a test issuer
  (`internal/auth/authtest`).
- `api/openapi.yaml`: no test compares it with the routes.
- The process's start (`cmd/server`, `internal/config`) has no test.
