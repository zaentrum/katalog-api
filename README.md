# katalog-api

Read-only catalog REST API for the zaentrum platform. Wraps the catalog
Postgres tables under the `cloud_katalog_ro` role and serves product apps
(`chino-web`, `tv-web`, `musig-web`) and edge consumers via OpenAPI 3.1.

## Status

Serves browse, a ranked search, item detail, people, what the library holds by identity
and the stream services' asset and playback lookups over the catalog tables;
`/healthz` and `/metrics` alongside.
The surface is described in [`api/openapi.yaml`](api/openapi.yaml).

## Design

- Catalog read/write split: this service is the **read** half; all writes go
  through `katalog-manager-api`.
- `katalog-api` only ever runs SELECTs under the `cloud_katalog_ro` Postgres
  role, so it is stateless and HPA-scalable.

## Search

`GET /api/v1/search?q=` is one search of the catalog: its titles, films,
series and episodes ranked together by how they match the query, best first,
and the people whose names match it, ranked apart:
`{"items": [...], "people": [...]}`. `types=` names the types of title to
look at (`movie,series,episode`, the default), `limit=` how many titles
(30, at most 100), `people=` how many people (12, at most 50; 0 none).

A title, a name and a plot are read normalized, as katalog-manager's
migration 049 indexes them: lower case, accents off, apostrophes dropped,
every other mark a space, and with the marks inside a word dropped too. So
"widows bay", "widow's bay" and "Widow´s Bay" are one, "leon" finds "Léon"
and "spiderman" "Spider-Man".

| Tier | `match` | The title, best first |
| --- | --- | --- |
| 1 | `exact` | is the query |
| 2 | `prefix` | begins with it, word by word: each word of the query begins the word of the title in its place ("wido", "widow b" → "Widow's Bay") |
| 3 | `words` | holds every word of it, each the beginning of a word, in any order ("widow" → "Black Widow") |
| 4 | `alternative` | ... a series' other name: the one the library files it under (046's `scantitle`) |
| 5 | as its title | an episode whose title matches as in 1–3, after every film and series; it names its `series` |
| 6 | `similar` | is close to it, a typo ("nevrland" → "Finding Neverland"): every word of the query begins a word of the title or is close to one (pg_trgm's strict word similarity, 0.5; words of four characters and more); only while tiers 1–5 fill no page; films and series before episodes |
| 7 | `plot` | its plot holds every word: only when no title matched in tiers 1–5 |

Within a tier: how close the title is to the query (trigram similarity; a
typo by the word similarity it was found by), then the rating, then the
title. A query shorter than three characters reads the beginnings of titles
alone (tiers 1–2). A search ranks at most a few times the limit of each kind
of candidate (the shortest titles first), so a query that matches thousands
of titles ranks only the best of them.

An episode names its series, `{"id", "title", "matched"}`: `matched` says
the series matched the query by its own title in the same search, so a client
can show the two as one card. People are ranked by the same tiers of their
name (`alternative`: another name they are known by, 030's `alsoknownas`; no
plot), within a tier by the titles that credit them, then how close the name
is, then the name; a person credited on no title is not found.

`GET /api/v1/movies?q=`, `/series?q=` and `/episodes?q=` without a filter or
an order of their own are ranked the same way within their type (the clients
before `/search` ask for films and series apart). With a filter or another
order, `q` matches a title, a series' other name or a plot, as normalized.

Until katalog-manager's 049 is in place (katalog-manager builds its indexes
in the background after it starts), a search matches as before 049: by the
catalog's `search_vector` column where there is one, else by the title, and
people as `GET /api/v1/people` finds them. The store looks at the catalog
again every 15 seconds until it is.

Measured on a generated catalog of 78,000 items and 16,000 people
(PostgreSQL 17 on one core of a development machine, from the query to the page): a search of
films and series answers in 1–3 ms, a one- to three-letter one in 4–13 ms,
the same with episodes in up to 25 ms; the people in 1–12 ms.

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
nothing yet, a capped viewer is served nothing. The one route behind the
bearer the cap does not filter is the holdings, which are the library's: a
capped request is refused them (see [Holdings by
identity](#holdings-by-identity)).

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

## Identities

Every item says the identities its title has outside the catalog, as
enrichment found them (the catalog's base table
`com_nalet_katalog_itemexternalids`): `externalIds`, for each source the
catalog keeps an id of it in, keyed as stored, that id. The sources in use are
`tmdb`, `tvdb` and `imdb` for a film or a series, and `tmdb-episode` for an
episode.

```json
{"id": "f001aeff-9c18-4183-b51b-51403af2515e", "type": "movie", "title": "A Film", "year": 2024,
 "externalIds": {"imdb": "tt0004711", "tmdb": "4711"}}
```

- On every read of an item: by id, the lists, a series' episodes, more like a
  title and a person's filmography. Omitted when the catalog keeps none, so
  such an item is sent as before.
- A row whose source or id is blank is none. Of two ids of one source the
  catalog keeps for a title (nothing stops it), the same one is sent every
  time.
- A role that may not read the table is served its items without them, until
  it may (`GRANT SELECT ON com_nalet_katalog_itemexternalids TO <role>`).

## Holdings by identity

A service that thinks in those identities rather than in catalog ids, such as
an addon, asks what the library holds by them (the platform's
[ADR-0012](https://github.com/zaentrum/zaentrum/blob/main/docs/adr/0012-catalog-identity-for-addons.md)).
The holdings say what the library holds, and never what anyone wants.

`GET /api/v1/holdings?type=series&tmdb=97546` (or `tvdb=`, `imdb=`) answers
the series that carries the identity, season by season:

```json
{"seriesId": "3f6b2c1e-8d4a-4e7f-9b0c-5a1d2e3f4a5b", "title": "A Show", "year": 2020,
 "externalIds": {"imdb": "tt9208876", "tmdb": "97546", "tvdb": "362392"},
 "seasons": [
   {"season": 0, "episodes": [
     {"episode": 1, "itemId": "5e6f7a8b-9c0d-4e1f-8a2b-3c4d5e6f7a8b", "hasFile": true, "playable": true, "coveredBy": null}]},
   {"season": 1, "episodes": [
     {"episode": 1, "itemId": "7d6c5b4a-3e2f-4a1b-8c0d-9e8f7a6b5c4d", "hasFile": true, "playable": false, "coveredBy": null},
     {"episode": 9, "itemId": "a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d", "hasFile": true, "playable": true, "coveredBy": null},
     {"episode": 10, "itemId": "0c9d8e7f-6a5b-4c3d-9e1f-2a3b4c5d6e7f", "hasFile": true, "playable": true,
      "coveredBy": "a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d"}]}]}
```

- The seasons come in their order, the specials (0) among them, and each
  episode the catalog holds of the series in its season, in episode order. An
  episode without a season or an episode number has no place among them and
  is left out.
- `hasFile`: the catalog records a file for the episode, the file it was taken
  in from (as `/items/{id}/asset` answers it) or its package. An original
  retired after packaging is no file, but its package is.
- `playable`: the episode is packaged, as `/packaged-ids` says.
- A covered episode (see [Episodes that share a file](#episodes-that-share-a-file))
  names its holder in `coveredBy`, and its `hasFile` and `playable` are the
  holder's; `coveredBy` is `null` on every other episode.

`GET /api/v1/holdings?type=movie&tmdb=4711` (or `imdb=`) answers the movie:

```json
{"itemId": "f001aeff-9c18-4183-b51b-51403af2515e", "title": "A Film", "year": 2024,
 "externalIds": {"imdb": "tt0004711", "tmdb": "4711"}, "hasFile": true, "playable": true}
```

- One identity is enough, and one no title carries does not keep the others
  from finding it. Identities that name different titles are 400.
- A tmdb id names a title with its type: TMDB numbers films and shows apart.
- 404 when no title of the type carries any of the identities given.
- `year` is `null` when the catalog does not know it; `externalIds` are all
  the title's identities.
- A title the catalog holds twice (two series that carry one identity, as a
  show the scanner made twice) is answered as one: a series as the first of
  them made, with the episodes of each; a movie as the one that plays, else
  one with a file, else the first made.

`GET /api/v1/holdings?type=series` (or `type=movie`) pages every title of the
type, so a service can follow the whole library without copying it:

```json
{"items": [
   {"itemId": "3f6b2c1e-8d4a-4e7f-9b0c-5a1d2e3f4a5b", "title": "A Show", "year": 2020,
    "externalIds": {"imdb": "tt9208876", "tmdb": "97546", "tvdb": "362392"}, "heldEpisodes": 4}],
 "page": 1, "pageSize": 200, "total": 2}
```

- In id order, which renaming a title does not change. `page` from 1,
  `pageSize` 200 unless asked, at most 1000. `total` is how many titles of the
  type there are; page 1 is there when there are none, and a later page past
  the last is 400.
- A title the catalog keeps no identity of is listed with `externalIds: {}`.
- `heldEpisodes`, on a series only: how many of its episodes have a file, as
  its holdings count them.

A request the holdings cannot answer is 400 with `{"error": "..."}` saying
why: no `type` or another one; an identity the type is not looked up by
(`tvdb` for a movie); one empty or malformed (a tmdb or tvdb id is a number,
an imdb id `tt` and a number), or given twice with two values; identities that
name different titles; `page` or `pageSize` no whole number in range, a page
past the last, or either with an identity; and a parameter the holdings do not
take, so that a misspelt identity is not read as a request for the listing.
404 says which identities nothing carries. While the role may not read the
identities the holdings are 503, never an answer that nothing is held.

Who reads them is who reads the catalog: the route is behind the bearer like
every other read, and the verifier admits any token of the issuer for one of
`KATALOG_API_OIDC_AUDIENCE`'s audiences, a signed-in person's and an addon's
service account's (the realm role `zaentrum-addon`) alike. katalog-api checks
no role. An addon's client needs that audience on its tokens, as the bundled
realm's clients have `chino`.

The holdings are the library's, not a viewer's: no rating cap applies to them,
nor anything else that leaves a title out of a viewer's lists. So they are
never served to a request capped at an age: one whose bearer carries
`max_rating`, or that passes the `max_rating` parameter, is 403, as a capped
viewer is served nothing above the cap.

The identities have no index but their primary key, as katalog-manager creates
the table. So a lookup by identity reads the table once (a hash join of the
identities asked), and so does each page of the listing and each read of items
for the identities of all of them, in one query by their ids. An index on
`(source, externalid)` would serve the lookups, and one on `(item_id)` the
reads. A series' episodes are found by `parent_id`, which has no index either;
their files by `idx_playbackassets_item` (042), their holders by 045's index.

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
tables before and after the migrations it reads: 030, 032, 036, 038, 039, 040,
045, 046 and 049). CI runs them against PostgreSQL 16. The search's golden
queries (`internal/store/search_test.go`) rank a fixture catalog as a viewer
should see it.
A throwaway server:

```bash
initdb -D /tmp/pg-katalog-api -U postgres --auth=trust
pg_ctl -D /tmp/pg-katalog-api -o "-p 55434 -c listen_addresses=127.0.0.1" -l /tmp/pg-katalog-api.log start
KATALOG_API_TEST_DATABASE_URL='postgres://postgres@127.0.0.1:55434/postgres?sslmode=disable' go test ./...
pg_ctl -D /tmp/pg-katalog-api stop
```

It needs the `unaccent` and `pg_trgm` extensions (PostgreSQL contrib), as the
catalog does.

## Build the container

```bash
docker build -t zaentrum/katalog-api .
```

Build and push the image to your own registry and update the image reference
in the `k8s/` manifests for your environment.

## License

[MPL-2.0](LICENSE).
