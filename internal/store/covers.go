package store

import "context"

// One file may hold two episodes or more (a double-length finale). It is never
// split: it is packaged once and plays for every episode it holds. The file
// belongs to its holder, the first episode it holds, as do its source, its
// versions and its package; every other episode it holds, one it covers, keeps
// an item of its own (its title, its numbers) and no file, and names the
// holder (katalog-manager's migration 045, com_nalet_katalog_items.coveredby).
// So whatever a covered episode plays is its holder's: each playback lookup of
// it answers the holder's, and it plays exactly when its holder does.
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

// holderOf is the SQL of the item whose file plays for the item id, an SQL
// expression (a parameter): the holder of a covered episode, else the item
// itself; id itself on a catalog that covers nothing (covering).
func holderOf(covering bool, id string) string {
	if !covering {
		return id
	}
	return `COALESCE((SELECT NULLIF(h.coveredby, '') FROM com_nalet_katalog_items h WHERE h.id = ` + id + `), ` + id + `)`
}
