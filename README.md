# katalog-api

Read-only catalog REST API for the zaentrum platform. Wraps the catalog
Postgres tables under the `cloud_katalog_ro` role and serves product apps
(`chino-web`, `tv-web`, `musig-web`) and edge consumers via OpenAPI 3.1.

## Status

**Scaffold.** The chi router, pgx pool, go-oidc verifier, and Prometheus
`/metrics` wiring are in place; the item handlers and SQL queries are stubs
pending the data model. The HTTP server compiles and serves `/healthz` +
`/metrics` so the service can be deployed as a placeholder.

## Design

- Catalog read/write split: this service is the **read** half; all writes go
  through `katalog-manager-api`.
- `katalog-api` only ever runs SELECTs under the `cloud_katalog_ro` Postgres
  role, so it is stateless and HPA-scalable.

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

## Build the container

```bash
docker build -t zaentrum/katalog-api .
```

Build and push the image to your own registry and update the image reference
in the `k8s/` manifests for your environment.

## License

[MPL-2.0](LICENSE).
