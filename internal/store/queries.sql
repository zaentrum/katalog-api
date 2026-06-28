-- sqlc query stubs for katalog-api. The full query set will mirror the
-- read surface of katalog-manager-api once the data model lands.
--
-- Run `sqlc generate` to regenerate the Go code under internal/store/.

-- name: ListItems :many
-- TODO: pagination + projection. Returns the items the cloud_katalog_ro role
-- is allowed to see.
SELECT id
FROM items
WHERE deleted_at IS NULL
ORDER BY id
LIMIT $1 OFFSET $2;

-- name: GetItem :one
-- TODO: return the canonical projection used by the chino-web, tv-web, and
-- musig-web detail pages.
SELECT id
FROM items
WHERE id = $1
  AND deleted_at IS NULL;
