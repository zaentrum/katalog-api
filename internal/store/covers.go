package store

import (
	"context"
	"strconv"
)

// One file may hold two episodes or more (a double-length finale). It is never
// split: it is packaged once and plays for every episode it holds. The file
// belongs to its holder, the first episode it holds, as do its source, its
// versions and its package; every other episode it holds, one it covers, keeps
// an item of its own (its title, its numbers) and no file, and names the
// holder (katalog-manager's migration 045, com_nalet_katalog_items.coveredby).
// So whatever a covered episode plays is its holder's: each playback lookup of
// it answers the holder's, and it plays exactly when its holder does.
//
// Whoever plays one episode of a file sees every episode it holds, so a viewer
// with a rating cap is held to the strictest rating among them: an episode of
// the file is served to the viewer only when each of them is, the holder and
// every episode it covers (fileCap). Each keeps its own rating as it is sent
// (min_age), and a viewer without a cap is served as before.
//
// On a catalog without 045, or with a role that may not read its column, no
// episode is covered and every item is read as before; the column is read as
// soon as it is there, without a restart (schema.go).

// coveredColumn is the column migration 045 adds to an item: the holder of the
// file a covered episode is held in, NULL on every other item.
const coveredColumn = "coveredby"

// covering reports whether the catalog says which episodes a file covers: it
// has migration 045's column, and this role may read it.
func (s *Store) covering(ctx context.Context) (bool, error) {
	has, err := s.columns(ctx, "com_nalet_katalog_items", coveredColumn)
	return has[coveredColumn], err
}

// coverSelect is what every item query reads of the file the item i shares,
// after its rating (scanItem): the holder it is covered by, the episodes it
// covers in episode order, and the highest of their numbers, the last episode
// its file holds. NULLs on a catalog that covers nothing (covering). The index
// 045 adds on the column finds the episodes a holder covers.
func coverSelect(covering bool) string {
	if !covering {
		return `, NULL::text, NULL::text[], NULL::int`
	}
	return `, NULLIF(i.coveredby, '')::text,
		ARRAY(SELECT cov.id::text FROM com_nalet_katalog_items cov WHERE cov.coveredby = i.id
		      ORDER BY cov.seasonnumber NULLS LAST, cov.episodenumber NULLS LAST, cov.id),
		(SELECT max(cov.episodenumber) FROM com_nalet_katalog_items cov WHERE cov.coveredby = i.id)::int`
}

// fileCap is the condition, beside the one that holds an item to its own
// rating (capFilter), that keeps an item i of whose file no episode is out of
// a viewer's cap: capAge is the cap (SQL, its parameter), and an episode out
// of it is one rated above it (as ageOf says), or one nothing rates unless
// show says unrated titles are served. An item that shares no file holds only
// itself, and is kept.
//
// The files that hold an episode out of the cap are found once a query, not
// once an item: the holders of the covered episodes out of it, found through
// 045's index on the column, and the holders out of it themselves, by their
// ids; an item is looked up among them by its file, its holder's id or its
// own. Neither yields NULL, so NOT IN keeps every other item.
func fileCap(capAge string, show bool) string {
	out := `NOT COALESCE(` + ageOf("f", "fp") + ` <= ` + capAge + `, ` + strconv.FormatBool(show) + `)`
	return `COALESCE(NULLIF(i.coveredby, ''), i.id) NOT IN (
		SELECT f.coveredby
		FROM com_nalet_katalog_items f LEFT JOIN com_nalet_katalog_items fp ON fp.id = f.parent_id
		WHERE f.coveredby IS NOT NULL AND f.coveredby <> '' AND ` + out + `
		UNION ALL
		SELECT f.id
		FROM com_nalet_katalog_items f LEFT JOIN com_nalet_katalog_items fp ON fp.id = f.parent_id
		WHERE f.id IN (SELECT c.coveredby FROM com_nalet_katalog_items c WHERE c.coveredby IS NOT NULL)
		  AND ` + out + `)`
}

// holderOf is the SQL of the item whose file plays for the item id, an SQL
// expression (a parameter): the holder of a covered episode, else the item
// itself; id itself on a catalog that covers nothing (covering).
func holderOf(covering bool, id string) string {
	if !covering {
		return id
	}
	return `COALESCE((SELECT NULLIF(h.coveredby, '') FROM com_nalet_katalog_items h WHERE h.id = ` + id + `), ` + id + `)`
}
