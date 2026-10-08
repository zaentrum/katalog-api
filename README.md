# katalog-api

Read-only catalog REST API for the zaentrum platform. Wraps the catalog
Postgres tables under the `cloud_katalog_ro` role and serves product apps
(`chino-web`, `tv-web`, `musig-web`) and edge consumers via OpenAPI 3.1.

## Status

Serves browse, search, item detail, people and the stream services' asset and
playback lookups over the catalog tables; `/healthz` and `/metrics` alongside.
The surface is described in [`api/openapi.yaml`](api/openapi.yaml).

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

## Extras

`GET /api/v1/items/{id}?include=extras` lists a movie's or a series' extras
that play: bonus material that is a file of its own (a trailer, a teaser, a
featurette, a deleted scene, …), which katalog-manager packages for streaming
apart from the title (its migration 039, `com_nalet_katalog_itemextras`).

- An extra plays once it is packaged, until it is removed, unless it is
  hidden (the scanner hides one whose file went missing). While it is packaged anew, the package it had
  plays on.
- They come in the order a viewer sees them: by the place they are given
  (those without one last; nothing sets one yet), then as they were taken in,
  then by id.
- Each is `id`, `kind` and `title` (its label when one is set, which nothing
  does yet, else the title it was taken in with) and, when known, `language`
  (as taken in: BCP 47 or ISO 639-2, `en`, `eng`; `zxx` none) and
  `duration_ms`. A series' extra that belongs to a season names it in
  `season_number` (0 is the specials).
- `trailers` (`include=trailers`) stays the item's links to online videos;
  extras are apart from them.
- An item's extras come with the item: a viewer whose rating cap leaves the
  item out gets the 404 of an id there is not.

```json
"extras": [
  {"id": "1b5c2a8e-6f0d-4c3e-9a51-2d7f0c4b8e01", "kind": "trailer", "title": "Trailer", "language": "en", "duration_ms": 33000},
  {"id": "1b5c2a8e-6f0d-4c3e-9a51-2d7f0c4b8e04", "kind": "featurette", "title": "Specials", "duration_ms": 61000, "season_number": 0}
]
```

On a catalog without migration 039 an item has no extras. A read-only role
granted the catalog's tables before 039 created the extras table may not read
it until someone grants it (`GRANT SELECT ON com_nalet_katalog_itemextras TO
<role>`); until then an item has no extras either, rather than failing.

## Ratings and capped viewers

A kid's account is capped at an age. Every route behind the bearer serves a
viewer at the stricter of the bearer's `max_rating` claim (a whole number of
years) and the `max_rating` parameter a BFF passes (chino-api passes its
viewer's on every catalog request); a cap that is no whole number of years is
the strictest, 0, and a request with neither is served as before. A capped
viewer is served the titles rated at most the cap: a title's rating is
katalog-manager's (its migration 036), an admin's `min_age_override`, else for
an episode its series' rating, else the minimum age the title's certification
means. A title nothing rates is served to the capped only while the catalog's
setting `ratings.unrated_for_capped` says `show` (it hides by default; read
from the settings at most every 30 seconds, and hidden when the role may not
read them). Whatever the cap leaves out is as if the catalog did not hold it:
the lists leave it out of the page and the total, a person's filmography and
search leave it out (and find no one credited in none of what is left), and
`/items/{id}`, `/items/{id}/segments`, `/items/{id}/similar` and
`/series/{id}/episodes` answer it 404, as an id there is not. The cap is one
condition in each query, over the title's rating columns and its parent's
(joined by primary key); a list of films or series reads their own rating,
which katalog-manager's `idx_items_rated_age` indexes. On a catalog that rates
nothing yet, a capped viewer is served nothing.

Every item says what it is rated: `min_age` (omitted when nothing rates it),
and `certification` with `certification_country`, the certification the age
comes from ("FSK 12" is `12` in `DE`), an episode's its series'.

`GET /api/v1/visible?ids=a,b&max_rating=12` answers which of the ids a viewer
at that cap may be served, `{"ids": [...]}` in their order. It is for the
requests a stream token authorizes, which carry the cap and no bearer, and like
the asset routes it takes no bearer: the Service is in-cluster only.

## Playback lookups

The stream services find what they play through the catalog: katalog-manager
records every path of the library as it packages, so a stream service neither
computes a path nor walks the storage to learn what is packaged. These routes
are internal: like the asset routes they take no bearer (the Service is
in-cluster only, with no route out) and apply no rating cap, as a stream token
already authorizes the request.

`GET /api/v1/items/{id}/playback` is where an item's package and its original
are:

```json
{"itemId": "f001aeff-9c18-4183-b51b-51403af2515e", "type": "movie",
 "package": {"versionId": "9a2e4f60-1b2c-4d3e-8f4a-5b6c7d8e9f00",
             "dir": "/var/lib/katalog/movies/f0/f001aeff-9c18-4183-b51b-51403af2515e/versions/9a2e4f60-1b2c-4d3e-8f4a-5b6c7d8e9f00",
             "record": "package.json", "completedAt": "2026-10-06T09:00:00Z"},
 "previous": [{"versionId": "77c1d2e3-f405-4a6b-9c8d-0e1f2a3b4c5d",
               "dir": "/var/lib/katalog/movies/f0/f001aeff-9c18-4183-b51b-51403af2515e/versions/77c1d2e3-f405-4a6b-9c8d-0e1f2a3b4c5d",
               "record": "package.json"}],
 "original": {"path": "/var/lib/katalog/.work/incoming/Sintel (2010).mkv", "sourceId": "0b6c6a52-8d1e-4f3a-9b2c-1d4e5f6a7b8c"}}
```

- `package` is the package that plays: the item's complete version
  (katalog-manager's migration 040, `com_nalet_katalog_itemversions`), its
  folder, and the record in it to read the package by. `null` when the item
  has no package; a series has none.
- `previous` are the item's superseded versions that are not removed yet, the
  one superseded last first. A superseded version stays on the storage for a
  grace before it is removed, so a playback session started on it may finish
  on it.
- `original` is the file the item was taken in from (its primary asset), with
  the source it is (`sourceId`, `null` when the catalog does not say); `null`
  once it was retired after packaging, or when there is none.
- `coveredBy` is sent for an episode held in the file of another, its holder,
  whose package, previous versions and original these are (see [Episodes that
  share a file](#episodes-that-share-a-file)); omitted for every other item.
- 404 for an id of no item.

Before 040, and for an item packaged before the library, the package is the
folder of the item's packaged asset, read by its `manifest.json`, with no
version, no `completedAt` and nothing previous:

```json
{"itemId": "f001aeff-9c18-4183-b51b-51403af2515e", "type": "movie",
 "package": {"versionId": null, "dir": "/var/lib/katalog/packages/movies/f0/f001aeff-9c18-4183-b51b-51403af2515e",
             "record": "manifest.json"},
 "previous": [],
 "original": {"path": "/var/lib/katalog/media/Sintel (2010).mkv", "sourceId": null}}
```

`GET /api/v1/extras/{extraId}/playback` is where an extra's package is: its
folder, the record to read it by (`package.json` for an extra packaged into its
title's folder in the library, `manifest.json` for one in the package store),
the title it belongs to and when it was packaged. 404 unless the extra is
packaged and not removed; a hidden one, or one whose original went missing,
still plays by its id.

```json
{"extraId": "2b7c9e10-3d4f-4a5b-8c6d-7e8f9a0b1c2d", "itemId": "ea886f9b-0d06-4f0f-babb-d2a1162f9b01",
 "dir": "/var/lib/katalog/movies/ea/ea886f9b-0d06-4f0f-babb-d2a1162f9b01/extras/2b7c9e10-3d4f-4a5b-8c6d-7e8f9a0b1c2d",
 "record": "package.json", "packagedAt": "2026-10-06T11:00:00Z"}
```

`GET /api/v1/packaged-ids` answers `{"ids": [...]}`: the movies and episodes
that have a packaged asset, in id order, before the library and in it alike.

`GET /api/v1/items/{id}/asset` answers the item's file: its primary asset, else
the first of its files by path. It never answers the item's package, nor an
original retired after packaging (whose file is gone): an item that has
nothing else is 404.

A read-only role granted the catalog's tables before 040 created the versions'
may not read them until it is granted them (`GRANT SELECT ON
com_nalet_katalog_itemversions TO <role>`; 040 grants `cloud_katalog_ro`).
Until then an item is answered from its packaged asset, which in the library
is its version's `package.json`, so the same folder plays, with nothing
previous.

## Episodes that share a file

One file may hold two episodes or more (a double-length finale). It is never
split: it is packaged once, and plays for every episode it holds. The file
belongs to its holder, the first episode it holds, as do its source, its
versions and its package. Every other episode it holds, one it covers, keeps
an item of its own (its title, its numbers) and no file, and names the holder
(katalog-manager's migration 045, `com_nalet_katalog_items.coveredby`).

- Every read of an item says which file it shares: the item by id, the lists,
  a series' episodes and a person's filmography. A covered episode sends
  `coveredBy`, its holder's id; the holder sends `covers`, the ids of the
  other episodes its file holds, in episode order, and `episodeEnd`, the
  number of the last of them. Each is omitted on every other item, which is
  sent as before.

```json
{"items": [
  {"id": "a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d", "type": "episode", "title": "Finale, Part One",
   "season_number": 1, "episode_number": 9, "parent_id": "3f6b2c1e-8d4a-4e7f-9b0c-5a1d2e3f4a5b",
   "covers": ["0c9d8e7f-6a5b-4c3d-9e1f-2a3b4c5d6e7f"], "episodeEnd": 10},
  {"id": "0c9d8e7f-6a5b-4c3d-9e1f-2a3b4c5d6e7f", "type": "episode", "title": "Finale, Part Two",
   "season_number": 1, "episode_number": 10, "parent_id": "3f6b2c1e-8d4a-4e7f-9b0c-5a1d2e3f4a5b",
   "coveredBy": "a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d"}
], "total": 2}
```

- A covered episode plays its holder's file, and is playable exactly when the
  holder is: `/items/{id}/playback` answers it the holder's package, previous
  versions and original, and names the holder in `coveredBy`;
  `/items/{id}/asset` answers the holder's file; `/packaged-ids` lists it when
  the holder has a package. Whatever a covered episode holds itself is not
  read.
- Its subtitles (`include=subtitles`) and its segments (`include=segments`,
  `/items/{id}/segments`) are those of the file it plays, the holder's. The
  rest of it (its title, its numbers, its credits, its genres) is its own.
- The episodes a holder covers are found by 045's index on the column.

On a catalog without 045, or with a role that may not read the column, no
episode is covered and every answer is as before.

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
tables before and after the migrations it reads: 030, 032, 036, 038, 039, 040
and 045).
A throwaway server:

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
