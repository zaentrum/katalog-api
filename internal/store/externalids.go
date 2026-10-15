package store

import (
	"context"
	"fmt"
)

// A title carries the identities it has outside the catalog: its id in a
// reference database, one source at a time, as enrichment found them (the
// catalog's base table com_nalet_katalog_itemexternalids). The sources in use
// are tmdb, tvdb and imdb for a film or a series, and tmdb-episode for an
// episode. TMDB numbers films and shows apart, so a tmdb id names a title
// together with the item's type.

// externalIDsTable is where the catalog keeps the items' identities, a row per
// item and source.
const externalIDsTable = "com_nalet_katalog_itemexternalids"

// externalIDColumns are the columns of externalIDsTable the identities are
// read from.
var externalIDColumns = []string{"id", "item_id", "source", "externalid"}

// identified reports whether this role may read the items' identities.
func (s *Store) identified(ctx context.Context) (bool, error) {
	has, err := s.columns(ctx, externalIDsTable, externalIDColumns...)
	return all(has, externalIDColumns...), err
}

// externalIDsOf returns the identities of the items ids, by item: each source
// the catalog keeps an id of the item in, keyed as it is stored, and that id.
// An item it keeps none of has no entry; a row whose source or id is blank is
// none. Should it keep two ids of one source for an item (nothing stops it),
// the same one is answered every time: the row's whose id sorts last.
func (s *Store) externalIDsOf(ctx context.Context, ids []string) (map[string]map[string]string, error) {
	out := map[string]map[string]string{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT item_id, source, externalid
		FROM `+externalIDsTable+`
		WHERE item_id = ANY($1::text[]) AND btrim(source) <> '' AND btrim(externalid) <> ''
		ORDER BY item_id, id`, ids)
	if err != nil {
		return nil, fmt.Errorf("identities of %d items: %w", len(ids), err)
	}
	defer rows.Close()
	for rows.Next() {
		var item, source, id string
		if err := rows.Scan(&item, &source, &id); err != nil {
			return nil, err
		}
		if out[item] == nil {
			out[item] = map[string]string{}
		}
		out[item][source] = id
	}
	return out, rows.Err()
}

// withExternalIDs sets the ExternalIDs of each of items to its identities
// (externalIDsOf), in one query for all of them. On a catalog whose identities
// this role may not read, no item says any rather than failing; they are read
// as soon as it may, without a restart.
func (s *Store) withExternalIDs(ctx context.Context, items []Item) error {
	if len(items) == 0 {
		return nil
	}
	if ok, err := s.identified(ctx); err != nil || !ok {
		return err
	}
	ids := make([]string, len(items))
	for i := range items {
		ids[i] = items[i].ID
	}
	byItem, err := s.externalIDsOf(ctx, ids)
	if err != nil {
		return err
	}
	for i := range items {
		items[i].ExternalIDs = byItem[items[i].ID]
	}
	return nil
}
