# katalog-api

Read-only catalog REST API for the zaentrum platform. Wraps the catalog
Postgres tables under the `cloud_katalog_ro` role and serves product apps
(`chino-web`, `tv-web`, `musig-web`) and edge consumers via OpenAPI 3.1.

## Status

Serves browse, search, item detail, people and the stream services' asset
lookups over the catalog tables; `/healthz` and `/metrics` alongside. The
surface is described in [`api/openapi.yaml`](api/openapi.yaml).

## Design

- Catalog read/write split: this service is the **read** half; all writes go
  through `katalog-manager-api`.
- `katalog-api` only ever runs SELECTs under the `cloud_katalog_ro` Postgres
  role, so it is stateless and HPA-scalable.

## Credits and people

- `GET /api/v1/items/{id}?include=people` lists a title's credits (`cast`):
  role by role in the order actor, creator, director, writer, producer,
  composer, cinematographer, editor, then any other role token
  alphabetically; within a role by billing order (unbilled last), then name.
  At most 20 actors and 10 people of every other role. Each entry is
  `person_id`, `name`, `role` and, when the catalog knows them, `job`,
  `character`, `order` (billing, 0 first) and `episode_count`.
- `GET /api/v1/people?q=` searches names; each result carries `credits` (the
  number of titles) and `has_profile`.
- `GET /api/v1/people/{id}` is the person, what the catalog knows about them
  (`sort_name`, `also_known_as`, `birth_date`, `death_date`, `birthplace`,
  `known_for_department`, `tmdb_person_id`, `imdb_id`), one `biography` with
  its `biography_lang`, `has_profile`, and their filmography with their
  `roles` on each title. The biography is in the language `?lang=` names,
  else the first of `Accept-Language`'s the catalog has, else English, else
  any.
- The portrait itself is served by katalog-manager at
  `/api/artwork/person/{id}/profile`; `has_profile` says whether there is one.

## Catalog schema

The tables belong to katalog-manager, which migrates them while this service
runs, so the two can roll out in either order. A column or table a migration
adds is read once the catalog has it and the role may SELECT it; until then
the field it fills is omitted from the response. A column found is
remembered; one missing is looked for again on the next request, so a
migration is picked up without a restart.

## Local development

```bash
go run ./cmd/server
curl localhost:8080/healthz
curl localhost:8080/api/v1/items
```

Set:

| Env | Default |
| --- | --- |
| `KATALOG_API_ADDR` | `:8080` |
| `KATALOG_API_PG_URL` | empty -> no DB |
| `KATALOG_API_OIDC_ISSUER` | your OIDC issuer's discovery URL |
| `KATALOG_API_OIDC_AUDIENCE` | `katalog` |

## Tests

```bash
go test ./...
```

The store and handler tests that need PostgreSQL are skipped unless
`KATALOG_API_TEST_DATABASE_URL` names a database in which they may create
and drop schemas and roles (each test makes its own schema, with the catalog
tables before and after the migrations it reads). A throwaway server:

```bash
initdb -D /tmp/pg-katalog-api -U postgres --auth=trust
pg_ctl -D /tmp/pg-katalog-api -o "-p 55434 -c listen_addresses=127.0.0.1" -l /tmp/pg-katalog-api.log start
KATALOG_API_TEST_DATABASE_URL='postgres://postgres@127.0.0.1:55434/postgres?sslmode=disable' go test ./...
pg_ctl -D /tmp/pg-katalog-api stop
```

It needs the `unaccent` extension (PostgreSQL contrib), as the catalog does.

## Build the container

```bash
docker build -t zaentrum/katalog-api .
```

Build and push the image to your own registry and update the image reference
in the `k8s/` manifests for your environment.

## License

[MPL-2.0](LICENSE).
