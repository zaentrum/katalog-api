package store

import (
	"context"
	"errors"
	"fmt"
	"path"
	"time"

	"github.com/jackc/pgx/v5"
)

// The stream services find what they play through the catalog: katalog-manager
// owns every path of the library, and records them as it packages (the
// library's tables, its migration 040), so a stream service never computes a
// path, nor walks the storage to learn what is packaged.
//
// An item's package is one of its versions: the one complete now, until a
// newer one completes and supersedes it. A superseded version stays on the
// storage for a grace before it is removed, so a playback session started on
// it ends on it. A version's folder holds its record, package.json.
//
// Before 040, and for an item packaged before the library, the package is the
// folder of the item's packaged asset (kind packaged), the package store's,
// whose record is its manifest.json; it has no versions.

// The record a package's folder holds: a version's package.json, else the
// package store's manifest.json.
const (
	recordPackage  = "package.json"
	recordManifest = "manifest.json"
)

// Playback is where an item's package and its original are, as the stream
// services read them. Package is the package that plays: the item's complete
// version, else its packaged asset's folder; nil when it has none, as a series
// never has. Previous are its superseded versions that are not removed, the
// one superseded last first: a session started on one of them is served from
// it until it ends. Original is the file the item was taken in from, while
// there is one; nil once it was retired, and for an item without one.
type Playback struct {
	ItemID   string       `json:"itemId"`
	Type     string       `json:"type"`
	Package  *PackageRef  `json:"package"`
	Previous []PackageRef `json:"previous"`
	Original *Original    `json:"original"`
}

// PackageRef is one package's folder (Dir) and the record in it the stream
// services read it by (Record: package.json or manifest.json). VersionID is
// the version it is, nil for a package from before the library. CompletedAt is
// when the complete version was completed; omitted for a previous version and
// for a package from before the library.
type PackageRef struct {
	VersionID   *string    `json:"versionId"`
	Dir         string     `json:"dir"`
	Record      string     `json:"record"`
	CompletedAt *time.Time `json:"completedAt,omitempty"`
}

// Original is the file an item was taken in from (its primary asset) and the
// source it is (040's sourceid), nil when the catalog does not say.
type Original struct {
	Path     string  `json:"path"`
	SourceID *string `json:"sourceId"`
}

// assetColumns040 are the columns migration 040 adds to a playback asset: the
// source and the version it is of.
var assetColumns040 = []string{"sourceid", "versionid"}

// versionColumns are the columns of com_nalet_katalog_itemversions, the table
// migration 040 creates, that an item's versions are read from.
var versionColumns = []string{"id", "item_id", "state", "dir", "completedat", "supersededat", "removedat"}

// Playback returns where the item itemID's package and original are, or
// ErrNotFound when the catalog holds no such item. It applies no rating cap:
// the stream services ask it for what a stream token already authorizes.
//
// On a catalog without migration 040, or with a role that may not read the
// versions yet (a table created after the read-only role was granted its
// tables), the package is the packaged asset's folder and there are no
// previous versions; the versions are read as soon as the table is there and
// the role may read it, without a restart. A packaged asset of a version is
// its package.json, so its folder plays the same until then.
func (s *Store) Playback(ctx context.Context, itemID string) (Playback, error) {
	if s == nil || s.Pool == nil {
		return Playback{}, ErrNoPool
	}
	has, err := s.columns(ctx, "com_nalet_katalog_playbackassets", assetColumns040...)
	if err != nil {
		return Playback{}, err
	}
	col := func(name string) string {
		if has[name] {
			return "a." + name
		}
		return "NULL::text"
	}
	p := Playback{Previous: []PackageRef{}}
	var origPath, origSource, pkgPath, pkgVersion *string
	// The original is the item's primary asset, marked primary and of kind
	// primary (a row without a kind is one); its packaged asset is the one
	// katalog-manager writes when it records the item's package (it keeps
	// one).
	err = s.Pool.QueryRow(ctx, `
		SELECT i.id, i.type, o.path, o.sourceid, k.path, k.versionid
		FROM com_nalet_katalog_items i
		LEFT JOIN LATERAL (
			SELECT a.path, `+col("sourceid")+` AS sourceid
			FROM com_nalet_katalog_playbackassets a
			WHERE a.item_id = i.id AND a.isprimary AND COALESCE(a.kind, 'primary') = 'primary'
			ORDER BY a.path LIMIT 1
		) o ON true
		LEFT JOIN LATERAL (
			SELECT a.path, `+col("versionid")+` AS versionid
			FROM com_nalet_katalog_playbackassets a
			WHERE a.item_id = i.id AND a.kind = 'packaged'
			ORDER BY a.path LIMIT 1
		) k ON true
		WHERE i.id = $1`, itemID).Scan(&p.ItemID, &p.Type, &origPath, &origSource, &pkgPath, &pkgVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return Playback{}, ErrNotFound
	}
	if err != nil {
		return Playback{}, fmt.Errorf("playback of %s: %w", itemID, err)
	}
	if origPath != nil {
		p.Original = &Original{Path: *origPath, SourceID: origSource}
	}
	if p.Type == "series" {
		return p, nil // a series is played by its episodes
	}
	if err := s.versionsOf(ctx, &p); err != nil {
		return Playback{}, err
	}
	if p.Package == nil && pkgPath != nil {
		p.Package = &PackageRef{VersionID: pkgVersion, Dir: path.Dir(*pkgPath), Record: recordOf(*pkgPath)}
	}
	return p, nil
}

// versionsOf sets p's package to the item's complete version and its previous
// ones to its superseded versions that are not removed, the one superseded
// last first (by the catalog's clock, which orders them as they were current;
// a version's completedat is its packager's), when the catalog has versions
// and this role may read them. A version without a folder is none to play.
func (s *Store) versionsOf(ctx context.Context, p *Playback) error {
	has, err := s.columns(ctx, "com_nalet_katalog_itemversions", versionColumns...)
	if err != nil {
		return err
	}
	if !all(has, versionColumns...) {
		return nil
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT id, dir, state = 'complete', completedat
		FROM com_nalet_katalog_itemversions
		WHERE item_id = $1 AND dir IS NOT NULL
		  AND (state = 'complete' OR (state = 'superseded' AND removedat IS NULL))
		ORDER BY state = 'complete' DESC, supersededat DESC NULLS LAST, completedat DESC NULLS LAST, id`,
		p.ItemID)
	if err != nil {
		return fmt.Errorf("versions of %s: %w", p.ItemID, err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id, dir   string
			complete  bool
			completed *time.Time
		)
		if err := rows.Scan(&id, &dir, &complete, &completed); err != nil {
			return err
		}
		ref := PackageRef{VersionID: &id, Dir: dir, Record: recordPackage}
		switch {
		case !complete:
			p.Previous = append(p.Previous, ref)
		case p.Package == nil: // an item has one complete version
			if completed != nil {
				at := completed.UTC()
				ref.CompletedAt = &at
			}
			p.Package = &ref
		}
	}
	return rows.Err()
}

// recordOf is the record of the package whose packaged asset is at p: a
// version's package.json, else the package store's manifest.json.
func recordOf(p string) string {
	if path.Base(p) == recordPackage {
		return recordPackage
	}
	return recordManifest
}

// ExtraPlayback is where an extra's package is: its folder (Dir) and the
// record in it (Record: package.json for an extra packaged into the library,
// manifest.json for one in the package store), the title it belongs to
// (ItemID), and when it was packaged.
type ExtraPlayback struct {
	ExtraID    string    `json:"extraId"`
	ItemID     string    `json:"itemId"`
	Dir        string    `json:"dir"`
	Record     string    `json:"record"`
	PackagedAt time.Time `json:"packagedAt"`
}

// extraPackageColumns are the columns of com_nalet_katalog_itemextras
// (migration 039) an extra's package is read from; extraPackageColumns040 adds
// 040's package id, which an extra packaged into the library has.
var (
	extraPackageColumns    = []string{"id", "item_id", "packagepath", "packagedat", "removedat"}
	extraPackageColumns040 = []string{"id", "item_id", "packagepath", "packagedat", "removedat", "packageid"}
)

// ExtraPlayback returns where the extra extraID's package is. It is
// ErrNotFound unless the extra is packaged and not removed (a hidden one, and
// one whose original went missing, keep their package), as on a catalog
// without migration 039 or with a role that may not read the extras.
func (s *Store) ExtraPlayback(ctx context.Context, extraID string) (ExtraPlayback, error) {
	if s == nil || s.Pool == nil {
		return ExtraPlayback{}, ErrNoPool
	}
	has, err := s.columns(ctx, "com_nalet_katalog_itemextras", extraPackageColumns040...)
	if err != nil {
		return ExtraPlayback{}, err
	}
	if !all(has, extraPackageColumns...) {
		return ExtraPlayback{}, ErrNotFound
	}
	packageID := "NULL::text"
	if has["packageid"] {
		packageID = "packageid"
	}
	var (
		x   ExtraPlayback
		pid *string
	)
	err = s.Pool.QueryRow(ctx, `
		SELECT id, item_id, packagepath, packagedat, `+packageID+`
		FROM com_nalet_katalog_itemextras
		WHERE id = $1 AND packagedat IS NOT NULL AND removedat IS NULL AND packagepath IS NOT NULL`,
		extraID).Scan(&x.ExtraID, &x.ItemID, &x.Dir, &x.PackagedAt, &pid)
	if errors.Is(err, pgx.ErrNoRows) {
		return ExtraPlayback{}, ErrNotFound
	}
	if err != nil {
		return ExtraPlayback{}, fmt.Errorf("playback of extra %s: %w", extraID, err)
	}
	x.PackagedAt = x.PackagedAt.UTC()
	x.Record = recordManifest
	if pid != nil {
		x.Record = recordPackage
	}
	return x, nil
}
