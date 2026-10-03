package store

import (
	"context"
	"errors"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store wraps the pgx pool used by the read-only catalog API. The pool is
// intentionally configured to connect with the `cloud_katalog_ro` Postgres
// role (set in the connection string) so the read path can never accidentally
// mutate rows owned by katalog-manager-api.
type Store struct {
	Pool *pgxpool.Pool

	// schema is what the store has learnt of the catalog's columns: which of
	// the ones a migration adds it may read yet (schema.go).
	schema catalogColumns
}

// ErrNotFound is returned when an item or asset does not exist.
var ErrNotFound = errors.New("not found")

// ErrNoPool is returned when handlers are called against a Store whose pool
// has not been initialised (scaffold mode).
var ErrNoPool = errors.New("no pg pool configured")

// New opens a pgx pool against the given URL. If url is empty (scaffold mode)
// New returns a Store with a nil pool — handlers must check before use.
func New(ctx context.Context, url string) (*Store, error) {
	if url == "" {
		slog.Warn("KATALOG_API_PG_URL not set; running with no DB pool (scaffold mode)")
		return &Store{}, nil
	}
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, err
	}
	return &Store{Pool: pool}, nil
}

// Close releases the underlying pool, if any.
func (s *Store) Close() {
	if s == nil || s.Pool == nil {
		return
	}
	s.Pool.Close()
}

// Asset is a single playback asset row from com_nalet_katalog_playbackassets.
type Asset struct {
	Path      string `json:"path"`
	IsPrimary bool   `json:"isPrimary"`
}

// SubtitleAssetInfo is the on-disk record for one sidecar subtitle.
// Returned by SubtitleAsset for the stream services so they can open
// the file from the packages PVC without a Postgres round-trip per
// segment.
type SubtitleAssetInfo struct {
	ItemID string `json:"itemId"`
	Path   string `json:"path"`
	Format string `json:"format,omitempty"`
	Lang   string `json:"lang,omitempty"`
}

// SubtitleAsset returns the disk path + format for one subtitle id.
// chino-stream / tv-stream / musig-stream call this with a stream
// token (same as Asset) to resolve a Subtitle.id from the item-detail
// response into a file on the packages PVC. Returns ErrNotFound when
// the id doesn't exist.
func (s *Store) SubtitleAsset(ctx context.Context, subID string) (SubtitleAssetInfo, error) {
	if s == nil || s.Pool == nil {
		return SubtitleAssetInfo{}, ErrNoPool
	}
	var a SubtitleAssetInfo
	err := s.Pool.QueryRow(ctx,
		`SELECT item_id, path, COALESCE(format,''), COALESCE(lang,'')
		 FROM com_nalet_katalog_subtitleassets
		 WHERE id = $1`, subID).Scan(&a.ItemID, &a.Path, &a.Format, &a.Lang)
	if errors.Is(err, pgx.ErrNoRows) {
		return SubtitleAssetInfo{}, ErrNotFound
	}
	return a, err
}

// PrimaryAsset returns the primary playback asset for an item. If no row has
// isprimary=true it falls back to the first asset by path. Returns
// ErrNotFound when the item has no assets.
func (s *Store) PrimaryAsset(ctx context.Context, itemID string) (Asset, error) {
	if s == nil || s.Pool == nil {
		return Asset{}, ErrNoPool
	}
	var a Asset
	err := s.Pool.QueryRow(ctx,
		`SELECT path, isprimary FROM com_nalet_katalog_playbackassets
		 WHERE item_id = $1 AND isprimary = true LIMIT 1`, itemID).Scan(&a.Path, &a.IsPrimary)
	if err == nil {
		return a, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Asset{}, err
	}
	err = s.Pool.QueryRow(ctx,
		`SELECT path, isprimary FROM com_nalet_katalog_playbackassets
		 WHERE item_id = $1 ORDER BY path LIMIT 1`, itemID).Scan(&a.Path, &a.IsPrimary)
	if errors.Is(err, pgx.ErrNoRows) {
		return Asset{}, ErrNotFound
	}
	return a, err
}
