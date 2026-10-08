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
// Migrate039, Migrate040 and Migrate045 bring the schema forward, so a test can
// prove a query works on a catalog before and after a migration — the catalog
// is katalog-manager's, and it migrates while this service runs.
package storetest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"strings"
	"testing"

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
	// has installed; extensions are per database, so it goes to public, which
	// every test session has on its search_path.
	if _, err := admin.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS unaccent"); err != nil {
		t.Fatalf("create the unaccent extension: %v", err)
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
// similarity, segment, rating and playback queries read.
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
