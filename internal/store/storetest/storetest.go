// Package storetest runs tests against a real PostgreSQL.
//
// A test that calls Open is skipped unless KATALOG_API_TEST_DATABASE_URL names
// a database in which it may create and drop schemas and roles, for example
//
//	KATALOG_API_TEST_DATABASE_URL='postgres://postgres@127.0.0.1:5432/postgres?sslmode=disable' go test ./...
//
// Each test gets a schema of its own, dropped when the test ends. It holds the
// catalog tables this service reads as a catalog older than migration 030 has
// them, column for column as katalog-manager creates them (lowercase, as
// Postgres folded the CAP DDL). Migrate030, Migrate032, Migrate036, Migrate038,
// Migrate039, Migrate040, Migrate045, Migrate046 and Migrate049 bring the
// schema forward, so a test can
// prove a query works on a catalog before and after a migration — the catalog
// is katalog-manager's, and it migrates while this service runs.
package storetest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// EnvURL names the variable that points the tests at a database.
const EnvURL = "KATALOG_API_TEST_DATABASE_URL"

// DB is one test's schema.
type DB struct {
	// Pool's sessions work in the schema (search_path = schema, public; the
	// unaccent extension lives in public).
	Pool   *pgxpool.Pool
	Schema string
	dsn    string
}

// Open returns a fresh schema holding the catalog tables as they are before
// migration 030, or skips t when no test database is configured.
func Open(t testing.TB) *DB {
	t.Helper()
	dsn := os.Getenv(EnvURL)
	if dsn == "" {
		t.Skipf("set %s to run the tests that need PostgreSQL", EnvURL)
	}
	ctx := context.Background()
	db := &DB{Schema: "katalog_api_test_" + randomHex(t, 6), dsn: dsn}

	admin := db.admin(t)
	defer admin.Close()
	// People search folds accents with unaccent, which the catalog database
	// has installed, and the search reads titles by their trigrams too
	// (pg_trgm, katalog-manager's 049); extensions are per database, so they
	// go to public, which every test session has on its search_path.
	for _, ext := range []string{"unaccent", "pg_trgm"} {
		createExtension(t, admin, ext)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+db.Schema); err != nil {
		t.Fatalf("create schema %s: %v", db.Schema, err)
	}
	t.Cleanup(func() {
		admin := db.admin(t)
		defer admin.Close()
		if _, err := admin.Exec(context.Background(), "DROP SCHEMA "+db.Schema+" CASCADE"); err != nil {
			t.Errorf("drop schema %s: %v", db.Schema, err)
		}
	})

	db.Pool = db.connect(t, "")
	db.Exec(t, baseSchema)
	return db
}

// createExtension creates the extension ext when the database has none of
// that name. The test packages run at once, and two sessions that create it
// at the same time can both find it missing: the one that loses is told the
// name is taken, and the extension is there then.
func createExtension(t testing.TB, admin *pgxpool.Pool, ext string) {
	t.Helper()
	_, err := admin.Exec(context.Background(), "CREATE EXTENSION IF NOT EXISTS "+ext)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && (pgErr.Code == "23505" || pgErr.Code == "42710") {
		err = nil
	}
	if err != nil {
		t.Fatalf("create the %s extension: %v", ext, err)
	}
}

// Migrate030 adds what migration 030 adds: a person's details and their
// portraits.
func (db *DB) Migrate030(t testing.TB) {
	t.Helper()
	db.Exec(t, migration030)
}

// Migrate032 adds what migration 032 adds to a credit: the job, the character
// played, the billing order and the number of episodes.
func (db *DB) Migrate032(t testing.TB) {
	t.Helper()
	db.Exec(t, migration032)
}

// Migrate036 adds what migration 036 adds to a title: its certification and
// its country, the minimum age it means, an admin's override, when TMDB was
// read, and the index of the age a title without a parent is held to.
func (db *DB) Migrate036(t testing.TB) {
	t.Helper()
	db.Exec(t, migration036)
}

// Migrate038 adds what migration 038 adds: whether a subtitle is forced.
func (db *DB) Migrate038(t testing.TB) {
	t.Helper()
	db.Exec(t, `ALTER TABLE com_nalet_katalog_subtitleassets
  ADD COLUMN IF NOT EXISTS isforced BOOLEAN NOT NULL DEFAULT false`)
}

// Migrate039 adds what migration 039 adds: a movie's or a series' extras, one
// row per extra, with what a viewer is shown of it and its package.
func (db *DB) Migrate039(t testing.TB) {
	t.Helper()
	db.Exec(t, migration039)
}

// Migrate040 adds what migration 040 adds, the library's tables: an item's
// sources (its originals) and versions (its packages), the source and the
// version a playback asset is of, and an extra's package id and when it was
// recorded. A catalog has 039 before it has 040, and 040 alters 039's table,
// so 039 is applied first (it is idempotent, as 040 is).
func (db *DB) Migrate040(t testing.TB) {
	t.Helper()
	db.Exec(t, migration039)
	db.Exec(t, migration040)
}

// Migrate045 adds what migration 045 adds: on an episode that one file holds
// with others, the holder it is covered by.
func (db *DB) Migrate045(t testing.TB) {
	t.Helper()
	db.Exec(t, migration045)
}

// Migrate046 adds what migration 046 adds: the name the scanner made a series
// under, its other name.
func (db *DB) Migrate046(t testing.TB) {
	t.Helper()
	db.Exec(t, migration046)
}

// Migrate049 applies katalog-manager's migration 049, the catalog search, as
// katalog-manager does: its functions in one statement, then each index on its
// own, CONCURRENTLY (an index of a column the catalog lacks, 046's or 030's,
// left out). The extensions are public's, on every session's search_path.
func (db *DB) Migrate049(t testing.TB) {
	t.Helper()
	parts := strings.Split(migration049, "\n--@index ")
	db.Exec(t, parts[0])
	for _, part := range parts[1:] {
		name, build, _ := strings.Cut(part, "\n")
		var missing bool
		switch name {
		case "idx_items_search_scantitle":
			missing = !db.hasColumn(t, "com_nalet_katalog_items", "scantitle")
		case "idx_people_search_aka":
			missing = !db.hasColumn(t, "com_nalet_katalog_people", "alsoknownas")
		}
		if !missing {
			db.Exec(t, strings.TrimSpace(build))
		}
	}
}

func (db *DB) hasColumn(t testing.TB, table, column string) bool {
	t.Helper()
	var ok bool
	if err := db.Pool.QueryRow(context.Background(), `SELECT EXISTS (SELECT 1 FROM pg_attribute
		WHERE attrelid = to_regclass($1) AND attname = $2 AND NOT attisdropped)`, table, column).Scan(&ok); err != nil {
		t.Fatalf("look up %s.%s: %v", table, column, err)
	}
	return ok
}

// Exec runs one statement (or several, without arguments) in the schema and
// fails t on error.
func (db *DB) Exec(t testing.TB, sql string, args ...any) {
	t.Helper()
	if _, err := db.Pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", firstLine(sql), err)
	}
}

// Role creates a login role that may use the schema but read nothing in it
// yet, and returns its name and a pool whose sessions run as it — the shape
// of the SELECT-only role this service runs under. The test grants it what it
// should read. The role is dropped when the test ends.
func (db *DB) Role(t testing.TB) (string, *pgxpool.Pool) {
	t.Helper()
	name := "katalog_api_ro_" + randomHex(t, 6)
	db.Exec(t, "CREATE ROLE "+name+" LOGIN")
	t.Cleanup(func() {
		admin := db.admin(t)
		defer admin.Close()
		ctx := context.Background()
		if _, err := admin.Exec(ctx, "DROP OWNED BY "+name); err != nil {
			t.Errorf("drop what %s holds: %v", name, err)
		}
		if _, err := admin.Exec(ctx, "DROP ROLE "+name); err != nil {
			t.Errorf("drop role %s: %v", name, err)
		}
	})
	db.Exec(t, "GRANT USAGE ON SCHEMA "+db.Schema+" TO "+name)
	return name, db.connect(t, name)
}

// connect opens a pool on the test database whose sessions work in the
// schema, as user when one is named. It is closed when the test ends, before
// anything registered earlier is cleaned up.
func (db *DB) connect(t testing.TB, user string) *pgxpool.Pool {
	t.Helper()
	dsn := withSession(db.dsn, user, map[string]string{"search_path": db.Schema + ",public"})
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect to %s: %v", EnvURL, err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func (db *DB) admin(t testing.TB) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), db.dsn)
	if err != nil {
		t.Fatalf("connect to %s: %v", EnvURL, err)
	}
	return pool
}

// withSession sets the user and session settings of a URL or key=value dsn.
// pgx passes parameters it does not know on to the server, which applies
// them to every session it opens.
func withSession(dsn, user string, params map[string]string) string {
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		if u, err := url.Parse(dsn); err == nil {
			if user != "" {
				u.User = url.User(user)
			}
			q := u.Query()
			for k, v := range params {
				q.Set(k, v)
			}
			u.RawQuery = q.Encode()
			return u.String()
		}
	}
	if user != "" {
		dsn += " user=" + user
	}
	for k, v := range params {
		dsn += " " + k + "=" + v
	}
	return dsn
}

func randomHex(t testing.TB, n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i] + " …"
	}
	return s
}

// baseSchema is the part of the CAP base schema this service's people,
// similarity, segment, rating, playback and identity queries read. The items'
// identities have no index but their primary key, as in the catalog.
const baseSchema = `
CREATE TABLE com_nalet_katalog_items (
  id VARCHAR(36) NOT NULL PRIMARY KEY,
  createdat TIMESTAMP, createdby VARCHAR(255), modifiedat TIMESTAMP, modifiedby VARCHAR(255),
  type VARCHAR(20) NOT NULL,
  title VARCHAR(255) NOT NULL,
  sorttitle VARCHAR(255), year INTEGER, description TEXT, rating DECIMAL(3, 1), durationms BIGINT,
  parent_id VARCHAR(36), seasonnumber INTEGER, episodenumber INTEGER, tagline VARCHAR(500),
  metadatalocked BOOLEAN NOT NULL DEFAULT false
);
CREATE TABLE com_nalet_katalog_itemexternalids (
  id VARCHAR(36) NOT NULL PRIMARY KEY, item_id VARCHAR(36) NOT NULL,
  source VARCHAR(30) NOT NULL, externalid VARCHAR(120) NOT NULL
);
CREATE TABLE com_nalet_katalog_genres (
  id VARCHAR(36) NOT NULL PRIMARY KEY, name VARCHAR(80) NOT NULL
);
CREATE TABLE com_nalet_katalog_itemgenres (
  id VARCHAR(36) NOT NULL PRIMARY KEY, item_id VARCHAR(36) NOT NULL, genre_id VARCHAR(36) NOT NULL
);
CREATE TABLE com_nalet_katalog_people (
  id VARCHAR(36) NOT NULL PRIMARY KEY, name VARCHAR(255) NOT NULL
);
CREATE TABLE com_nalet_katalog_itempeople (
  id VARCHAR(36) NOT NULL PRIMARY KEY, item_id VARCHAR(36) NOT NULL,
  person_id VARCHAR(36) NOT NULL, role VARCHAR(40) NOT NULL
);
CREATE TABLE com_nalet_katalog_mediasegments (
  id VARCHAR(36) NOT NULL PRIMARY KEY,
  createdat TIMESTAMP, createdby VARCHAR(255), modifiedat TIMESTAMP, modifiedby VARCHAR(255),
  item_id VARCHAR(36) NOT NULL, kind VARCHAR(20) NOT NULL, startms BIGINT NOT NULL, endms BIGINT NOT NULL,
  source VARCHAR(30) NOT NULL, confidence DECIMAL(3, 2), label VARCHAR(120)
);
CREATE TABLE com_nalet_katalog_playbackassets (
  id VARCHAR(36) NOT NULL PRIMARY KEY, item_id VARCHAR(36) NOT NULL,
  path VARCHAR(2048) NOT NULL, codec VARCHAR(40), resolution VARCHAR(40), bitratekbps INTEGER,
  sizebytes BIGINT, hash VARCHAR(160), isprimary BOOLEAN DEFAULT FALSE, kind VARCHAR(20) DEFAULT 'primary',
  audiocodec VARCHAR(40), audiolanguage VARCHAR(10), audiochannels INTEGER, audiobitratekbps INTEGER,
  audiotrackcount INTEGER, subtitletrackcount INTEGER, durationms BIGINT
);
CREATE TABLE com_nalet_katalog_subtitleassets (
  id VARCHAR(36) NOT NULL PRIMARY KEY, item_id VARCHAR(36) NOT NULL,
  path VARCHAR(2048), format VARCHAR(20), lang VARCHAR(10), label VARCHAR(200),
  isdefault BOOLEAN DEFAULT FALSE
);
CREATE TABLE com_nalet_katalog_settings (
  id VARCHAR(36) NOT NULL PRIMARY KEY,
  createdat TIMESTAMP, createdby VARCHAR(255), modifiedat TIMESTAMP, modifiedby VARCHAR(255),
  key VARCHAR(120) NOT NULL, valuetext VARCHAR(2000) NOT NULL DEFAULT '',
  valuetype VARCHAR(20) NOT NULL DEFAULT 'string', description TEXT
);
`

// migration030 is what katalog-manager's 030_people.sql adds that this
// service reads.
const migration030 = `
ALTER TABLE com_nalet_katalog_people
  ADD COLUMN IF NOT EXISTS sortname           VARCHAR(255),
  ADD COLUMN IF NOT EXISTS alsoknownas        JSONB CHECK (jsonb_typeof(alsoknownas) = 'array'),
  ADD COLUMN IF NOT EXISTS birthdate          DATE,
  ADD COLUMN IF NOT EXISTS deathdate          DATE,
  ADD COLUMN IF NOT EXISTS birthplace         TEXT,
  ADD COLUMN IF NOT EXISTS biography          JSONB CHECK (jsonb_typeof(biography) = 'object'),
  ADD COLUMN IF NOT EXISTS tmdbpersonid       TEXT CHECK (tmdbpersonid ~ '^[0-9]+$'),
  ADD COLUMN IF NOT EXISTS imdbid             TEXT CHECK (imdbid ~ '^nm[0-9]+$'),
  ADD COLUMN IF NOT EXISTS knownfordepartment TEXT;
CREATE TABLE IF NOT EXISTS com_nalet_katalog_personartwork (
  id          VARCHAR(36)  PRIMARY KEY,
  person_id   VARCHAR(36)  NOT NULL,
  kind        VARCHAR(20)  NOT NULL DEFAULT 'profile' CHECK (kind IN ('profile')),
  contenttype VARCHAR(80)  NOT NULL,
  bytes       BYTEA        NOT NULL,
  sha256      CHAR(64)     NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
  width       INTEGER,
  height      INTEGER,
  isprimary   BOOLEAN      NOT NULL DEFAULT false,
  sourcepath  VARCHAR(2048),
  fetchedat   TIMESTAMPTZ
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_personartwork_primary
  ON com_nalet_katalog_personartwork (person_id, kind) WHERE isprimary;
`

// migration032 is what migration 032 adds to a credit, as the catalog
// contract defines it: nullable job, character, billing order and episode
// count, a role token, and one credit per (item, person, role).
const migration032 = `
ALTER TABLE com_nalet_katalog_itempeople
  ADD COLUMN IF NOT EXISTS job           VARCHAR(255),
  ADD COLUMN IF NOT EXISTS charactername TEXT,
  ADD COLUMN IF NOT EXISTS ordinal       INTEGER,
  ADD COLUMN IF NOT EXISTS episodecount  INTEGER;
ALTER TABLE com_nalet_katalog_itempeople
  ADD CONSTRAINT itempeople_role_token CHECK (role ~ '^[a-z][a-z0-9-]{0,39}$');
CREATE UNIQUE INDEX IF NOT EXISTS idx_itempeople_credit
  ON com_nalet_katalog_itempeople (item_id, person_id, role);
`

// migration036 is what katalog-manager's 036_item_ratings.sql adds: a title's
// certification as TMDB gives it, its country, the minimum age it means (0 to
// 21), an admin's override, when TMDB was read, and the index of the age a
// title without a parent is held to. An episode carries none of its own.
const migration036 = `
ALTER TABLE com_nalet_katalog_items
  ADD COLUMN IF NOT EXISTS certification            VARCHAR(40),
  ADD COLUMN IF NOT EXISTS certification_country    VARCHAR(2) CHECK (certification_country ~ '^[A-Z]{2}$'),
  ADD COLUMN IF NOT EXISTS min_age                  SMALLINT CHECK (min_age BETWEEN 0 AND 21),
  ADD COLUMN IF NOT EXISTS min_age_override         SMALLINT CHECK (min_age_override BETWEEN 0 AND 21),
  ADD COLUMN IF NOT EXISTS certification_fetched_at TIMESTAMPTZ;
CREATE INDEX IF NOT EXISTS idx_items_rated_age
  ON com_nalet_katalog_items ((COALESCE(min_age_override, min_age)));
`

// migration039 is katalog-manager's 039_item_extras.sql, as the catalog
// contract defines it: one row per extra of a movie or a series (never an
// episode), keyed by the extra's id, with how it was taken in, what a viewer
// is shown of it (its order, whether it is hidden, a label), where its
// packaging stands, its package, and when it was removed. An extra plays when
// it is packaged, not removed, not hidden and its source is not missing.
const migration039 = `
CREATE TABLE IF NOT EXISTS com_nalet_katalog_itemextras (
  id VARCHAR(36) PRIMARY KEY,
  item_id VARCHAR(36) NOT NULL,
  kind VARCHAR(20) NOT NULL CHECK (kind IN ('featurette','behind-the-scenes','making-of','deleted-scene',
       'interview','trailer','teaser','gag-reel','short','other')),
  title VARCHAR(255) NOT NULL CHECK (title <> ''),
  localizedtitles JSONB CHECK (localizedtitles IS NULL OR jsonb_typeof(localizedtitles)='object'),
  language VARCHAR(35), seasonnumber INTEGER CHECK (seasonnumber IS NULL OR seasonnumber >= 0),
  origin JSONB CHECK (origin IS NULL OR jsonb_typeof(origin)='object'),
  sourcepath VARCHAR(2048), sourcesize BIGINT,
  sourceqh1 VARCHAR(71) CHECK (sourceqh1 IS NULL OR sourceqh1 ~ '^sha256:[0-9a-f]{64}$'),
  recordpath VARCHAR(2048),
  registeredby VARCHAR(10) NOT NULL CHECK (registeredby IN ('api','scanner','library')),
  sortorder INTEGER, hidden BOOLEAN NOT NULL DEFAULT false, label VARCHAR(255),
  state VARCHAR(12) NOT NULL DEFAULT 'pending' CHECK (state IN
       ('pending','queued','transcoding','transcoded','packaging','ready','failed','missing')),
  error VARCHAR(500), attempts INTEGER NOT NULL DEFAULT 0, failures INTEGER NOT NULL DEFAULT 0,
  nextretryat TIMESTAMPTZ, dispatchedat TIMESTAMPTZ, heartbeatat TIMESTAMPTZ,
  packagepath VARCHAR(2048), packagedat TIMESTAMPTZ, durationms BIGINT, videocodec VARCHAR(40),
  width INTEGER, height INTEGER, peakbandwidthbps BIGINT, packagesizebytes BIGINT,
  removedat TIMESTAMPTZ, removedby VARCHAR(255), removalreason VARCHAR(500),
  createdat TIMESTAMPTZ NOT NULL DEFAULT now(), createdby VARCHAR(255),
  modifiedat TIMESTAMPTZ NOT NULL DEFAULT now(), modifiedby VARCHAR(255));
CREATE INDEX IF NOT EXISTS idx_itemextras_item ON com_nalet_katalog_itemextras (item_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_itemextras_source ON com_nalet_katalog_itemextras (sourcepath)
  WHERE removedat IS NULL AND sourcepath IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_itemextras_due ON com_nalet_katalog_itemextras (state, nextretryat)
  WHERE removedat IS NULL AND state NOT IN ('ready','failed');
`

// migration040 is katalog-manager's 040_library_v2.sql, verbatim as the library
// contract defines it: when an item was recorded in the library and projected,
// and its hold; a person's projection; an item's sources (its originals, while
// they exist) and versions (its packages: one building, one complete, the
// superseded ones until they are removed); the source and the version a
// playback asset is of; an extra's package id, when it was recorded and when
// its original was deleted; and SELECT on the new tables for the read-only
// role, where there is one.
const migration040 = `
ALTER TABLE com_nalet_katalog_items
  ADD COLUMN IF NOT EXISTS recordedat TIMESTAMPTZ,            -- item.json written
  ADD COLUMN IF NOT EXISTS libraryprojectedat TIMESTAMP,      -- the modifiedat the last metadata.json reflects
  ADD COLUMN IF NOT EXISTS retirehold BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE com_nalet_katalog_people
  ADD COLUMN IF NOT EXISTS libraryprojectedat TIMESTAMPTZ;
CREATE TABLE IF NOT EXISTS com_nalet_katalog_itemsources (
  id VARCHAR(36) PRIMARY KEY,                    -- the sourceId: sources/<id>/
  item_id VARCHAR(36) NOT NULL,
  filename VARCHAR(1024) NOT NULL,               -- source.json file.name
  arrivalpath VARCHAR(2048),                     -- absolute, while the original exists; NULL after retire
  librarypath VARCHAR(2048),                     -- relative to ARRIVALS_ROOT (source.json origin.libraryPath)
  sizebytes BIGINT NOT NULL,
  qh1 VARCHAR(71) CHECK (qh1 IS NULL OR qh1 ~ '^sha256:[0-9a-f]{64}$'),
  state VARCHAR(12) NOT NULL DEFAULT 'present' CHECK (state IN ('present','retiring','deleted','removed')),
  recordedat TIMESTAMPTZ, recorddir VARCHAR(2048),
  sidecars JSONB CHECK (sidecars IS NULL OR jsonb_typeof(sidecars)='array'),  -- [{subtitleAssetId, rendition, path}]
  retireeventid VARCHAR(36), retireeventat TIMESTAMPTZ, trashpath VARCHAR(2048),
  deletedat TIMESTAMPTZ, deletedby VARCHAR(255), lost JSONB, error VARCHAR(500),
  createdat TIMESTAMPTZ NOT NULL DEFAULT now(), modifiedat TIMESTAMPTZ NOT NULL DEFAULT now());
CREATE INDEX IF NOT EXISTS idx_itemsources_item ON com_nalet_katalog_itemsources (item_id);
CREATE INDEX IF NOT EXISTS idx_itemsources_fixity ON com_nalet_katalog_itemsources (sizebytes, qh1);
CREATE UNIQUE INDEX IF NOT EXISTS idx_itemsources_arrival ON com_nalet_katalog_itemsources (arrivalpath) WHERE arrivalpath IS NOT NULL;
CREATE TABLE IF NOT EXISTS com_nalet_katalog_itemversions (
  id VARCHAR(36) PRIMARY KEY,                    -- the versionId: versions/<id>/
  item_id VARCHAR(36) NOT NULL,
  sourceids VARCHAR(36)[] NOT NULL DEFAULT '{}',
  state VARCHAR(12) NOT NULL CHECK (state IN ('building','complete','superseded','removed')),
  packageid VARCHAR(36), dir VARCHAR(2048),
  completedat TIMESTAMPTZ, verifiedat TIMESTAMPTZ,
  verifiedlevel VARCHAR(8) CHECK (verifiedlevel IS NULL OR verifiedlevel IN ('chain','full')),
  supersededby VARCHAR(36), supersededat TIMESTAMPTZ, supersedeeventid VARCHAR(36),
  removedat TIMESTAMPTZ, removeeventid VARCHAR(36),
  createdat TIMESTAMPTZ NOT NULL DEFAULT now(), modifiedat TIMESTAMPTZ NOT NULL DEFAULT now());
CREATE INDEX IF NOT EXISTS idx_itemversions_item ON com_nalet_katalog_itemversions (item_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_itemversions_building ON com_nalet_katalog_itemversions (item_id) WHERE state='building';
CREATE UNIQUE INDEX IF NOT EXISTS idx_itemversions_current  ON com_nalet_katalog_itemversions (item_id) WHERE state='complete';
ALTER TABLE com_nalet_katalog_playbackassets
  ADD COLUMN IF NOT EXISTS sourceid VARCHAR(36), ADD COLUMN IF NOT EXISTS versionid VARCHAR(36);
ALTER TABLE com_nalet_katalog_itemextras
  ADD COLUMN IF NOT EXISTS packageid VARCHAR(36), ADD COLUMN IF NOT EXISTS recordedat TIMESTAMPTZ,
  ADD COLUMN IF NOT EXISTS sourcedeletedat TIMESTAMPTZ;
DO $$ BEGIN IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='cloud_katalog_ro') THEN
  GRANT SELECT ON com_nalet_katalog_itemsources, com_nalet_katalog_itemversions TO cloud_katalog_ro; END IF; END $$;
`

// migration045 is what katalog-manager's migration 045 adds, as the library
// contract defines it: one file that holds several episodes belongs to the
// first of them, its holder, and every other episode it holds (one it covers)
// names the holder's item id; NULL on every other item. The index finds the
// episodes a holder covers. A role granted SELECT on the items may read a
// column added to them, so 045 grants nothing.
const migration045 = `
ALTER TABLE com_nalet_katalog_items
  ADD COLUMN IF NOT EXISTS coveredby VARCHAR(36);
CREATE INDEX IF NOT EXISTS idx_items_coveredby ON com_nalet_katalog_items (coveredby);
`

// migration046 is what katalog-manager's 046_series_scan_title.sql adds: the
// name the scanner made a series under, and its index.
const migration046 = `
ALTER TABLE com_nalet_katalog_items ADD COLUMN IF NOT EXISTS scantitle VARCHAR(500);
CREATE INDEX IF NOT EXISTS idx_items_series_scantitle ON com_nalet_katalog_items (scantitle)
  WHERE type = 'series' AND scantitle IS NOT NULL;
`

// migration049 is katalog-manager's 049_catalog_search.sql, verbatim: the
// functions that normalize a title, a name or a plot for the search, and the
// indexes that hold them, each after its "--@index" line.
const migration049 = `-- 049_catalog_search.sql — what katalog-api's ranked search finds titles and people by (idempotent).
--
-- katalog-api ranks a search by how a title matches it: the title is the
-- query, begins with it, holds every word of it, a series' other name holds
-- them, an episode's title does, the title is close to it (a typo), and last,
-- when nothing else matched, the plot holds them. Each of those reads a
-- title, a name or a plot as it is normalized, so that "widows bay", "widow's
-- bay" and "Widow´s Bay" are one and "leon" finds "Léon":
--
--   com_nalet_katalog_search_text(t)    lower case, accents off (unaccent's
--                                       rules), apostrophes dropped
--                                       ("Widow's" is one word, "widows"),
--                                       every other mark and space one space:
--                                       "Spider-Man: Far From Home" is
--                                       "spider man far from home"
--   com_nalet_katalog_search_joined(t)  the same, the marks inside a word
--                                       (- . · • / & + *) dropped too:
--                                       "spiderman far from home", "S.W.A.T."
--                                       "swat", "WALL·E" "walle"
--   com_nalet_katalog_search_words(t)   the words of both, as a tsvector
--                                       (configuration simple: no stemming,
--                                       no stop words)
--   com_nalet_katalog_search_plot(t)    the words of the first, for a plot
--
-- They are IMMUTABLE so that an index can hold their results. unaccent() is
-- STABLE because its dictionary could change; they name the dictionary, as
-- the catalog's immutable_unaccent does. Every name in them is schema
-- qualified: from PostgreSQL 17 on, an index's expression is computed under
-- a search_path of pg_catalog alone during CREATE INDEX, REINDEX, VACUUM and
-- ANALYZE. Their cost says they are expensive, so that the planner reaches
-- for the indexes rather than computing them row by row.
--
-- Never change what a function returns: an index holds what it returned when
-- the row was written, and a change would leave it disagreeing with the
-- function. A change of normalization makes new functions and new indexes.
--
-- The indexes, built CONCURRENTLY: a build neither blocks the catalog's
-- writes nor rewrites the table. CREATE INDEX CONCURRENTLY cannot run inside
-- a transaction, and a build that is cut short leaves its index invalid, so
-- katalog-manager runs each statement after a line "--@index <name>" on its
-- own, in the background after startup, drops an invalid index of that name
-- and builds it again (store.EnsureCatalogSearch). Everything before the
-- first "--@index" line runs as one statement, before the indexes.
--
--   idx_items_search_words     the words of a title (a GIN of
--                              com_nalet_katalog_search_words(title)): every
--                              word of a query begins a word of it
--   idx_items_search_prefix    a title by its type and its text: it is the
--                              query, it begins with it (a B-tree,
--                              text_pattern_ops, with the id and the title, so
--                              that a scan of it reads no row)
--   idx_items_search_trigrams  the trigrams of a title's text (pg_trgm): a
--                              title close to the query
--   idx_items_search_scantitle the words of the name the scanner made a series
--                              under (046's scantitle), a series' other name
--   idx_items_search_plot      the words of a plot
--   idx_people_search_words, idx_people_search_prefix,
--   idx_people_search_trigrams a person's name, as a title's
--   idx_people_search_aka      the other names a person is known by (030's
--                              alsoknownas)
--   idx_itempeople_person      a person's credits, which a person found is
--                              ranked by (and a filmography is read by)
--
-- Lowercase identifiers, like every com_nalet_katalog_* table. A large
-- catalog (78,000 items, 16,000 people) builds them in seconds.

DO $migration$
DECLARE
  -- The functions go where the catalog's items are, which every reader
  -- finds through its search_path.
  catalog text := (SELECT n.nspname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
                   WHERE c.oid = to_regclass('com_nalet_katalog_items'));
  accents text;
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'unaccent') THEN
    CREATE EXTENSION unaccent;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'pg_trgm') THEN
    CREATE EXTENSION pg_trgm;
  END IF;
  SELECT n.nspname INTO accents FROM pg_extension e JOIN pg_namespace n ON n.oid = e.extnamespace
  WHERE e.extname = 'unaccent';

  EXECUTE format($f$
    CREATE OR REPLACE FUNCTION %1$I.com_nalet_katalog_search_text(t text) RETURNS text
    LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE COST 10000 AS $body$
      SELECT pg_catalog.btrim(pg_catalog.regexp_replace(pg_catalog.regexp_replace(
        pg_catalog.lower(%2$I.unaccent(%3$L::regdictionary, t)),
        '[\U00000027\U00000060\U000000b4\U000002bc\U00002018\U00002019\U00002032\U0000ff07]+', '', 'g'),
        '[[:space:]\U00000021-\U0000002f\U0000003a-\U00000040\U0000005b-\U00000060\U0000007b-\U0000007e\U000000a0-\U000000bf\U000000d7\U000000f7\U00002000-\U0000206f\U00002e00-\U00002e7f\U00003000-\U00003003\U00003008-\U00003011\U00003014-\U0000301f\U0000fe10-\U0000fe1f\U0000fe30-\U0000fe6b\U0000ff01-\U0000ff0f\U0000ff1a-\U0000ff20\U0000ff3b-\U0000ff40\U0000ff5b-\U0000ff65]+',
        ' ', 'g'))
    $body$$f$, catalog, accents, accents || '.unaccent');

  EXECUTE format($f$
    CREATE OR REPLACE FUNCTION %1$I.com_nalet_katalog_search_joined(t text) RETURNS text
    LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE COST 10000 AS $body$
      SELECT pg_catalog.btrim(pg_catalog.regexp_replace(pg_catalog.regexp_replace(
        pg_catalog.lower(%2$I.unaccent(%3$L::regdictionary, t)),
        '[\U00000027\U00000060\U000000b4\U000002bc\U00002018\U00002019\U00002032\U0000ff07\U0000002e\U000000b7\U00002022\U0000002f\U00000026\U0000002b\U0000002a\U0000002d\U00002010-\U00002015\U00002212]+', '', 'g'),
        '[[:space:]\U00000021-\U0000002f\U0000003a-\U00000040\U0000005b-\U00000060\U0000007b-\U0000007e\U000000a0-\U000000bf\U000000d7\U000000f7\U00002000-\U0000206f\U00002e00-\U00002e7f\U00003000-\U00003003\U00003008-\U00003011\U00003014-\U0000301f\U0000fe10-\U0000fe1f\U0000fe30-\U0000fe6b\U0000ff01-\U0000ff0f\U0000ff1a-\U0000ff20\U0000ff3b-\U0000ff40\U0000ff5b-\U0000ff65]+',
        ' ', 'g'))
    $body$$f$, catalog, accents, accents || '.unaccent');

  EXECUTE format($f$
    CREATE OR REPLACE FUNCTION %1$I.com_nalet_katalog_search_words(t text) RETURNS tsvector
    LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE AS $body$
      SELECT pg_catalog.to_tsvector('pg_catalog.simple'::regconfig,
        %1$I.com_nalet_katalog_search_text(t) || ' ' || %1$I.com_nalet_katalog_search_joined(t))
    $body$$f$, catalog);

  EXECUTE format($f$
    CREATE OR REPLACE FUNCTION %1$I.com_nalet_katalog_search_plot(t text) RETURNS tsvector
    LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE AS $body$
      SELECT pg_catalog.to_tsvector('pg_catalog.simple'::regconfig, %1$I.com_nalet_katalog_search_text(t))
    $body$$f$, catalog);
END $migration$;

--@index idx_items_search_words
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_items_search_words ON com_nalet_katalog_items
  USING gin (com_nalet_katalog_search_words(title));
--@index idx_items_search_prefix
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_items_search_prefix ON com_nalet_katalog_items
  (type, com_nalet_katalog_search_text(title) text_pattern_ops) INCLUDE (id, title);
--@index idx_items_search_trigrams
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_items_search_trigrams ON com_nalet_katalog_items
  USING gin (com_nalet_katalog_search_text(title) gin_trgm_ops);
--@index idx_items_search_scantitle
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_items_search_scantitle ON com_nalet_katalog_items
  USING gin (com_nalet_katalog_search_words(scantitle)) WHERE scantitle IS NOT NULL;
--@index idx_items_search_plot
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_items_search_plot ON com_nalet_katalog_items
  USING gin (com_nalet_katalog_search_plot(description));
--@index idx_people_search_words
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_people_search_words ON com_nalet_katalog_people
  USING gin (com_nalet_katalog_search_words(name));
--@index idx_people_search_prefix
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_people_search_prefix ON com_nalet_katalog_people
  (com_nalet_katalog_search_text(name) text_pattern_ops) INCLUDE (id, name);
--@index idx_people_search_trigrams
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_people_search_trigrams ON com_nalet_katalog_people
  USING gin (com_nalet_katalog_search_text(name) gin_trgm_ops);
--@index idx_people_search_aka
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_people_search_aka ON com_nalet_katalog_people
  USING gin (com_nalet_katalog_search_words(alsoknownas::text)) WHERE alsoknownas IS NOT NULL;
--@index idx_itempeople_person
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_itempeople_person ON com_nalet_katalog_itempeople (person_id);
`
