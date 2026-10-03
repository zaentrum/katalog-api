package store

import (
	"context"
	"fmt"
	"sync"

	"github.com/jackc/pgx/v5"
)

// The catalog's tables belong to katalog-manager, which migrates them while
// this service runs: in an upgrade the two services roll out minutes apart, in
// either order. So a column or table that a migration adds is read only once
// the catalog has it. Until then the field it fills is left out of the
// response — a query never names what is not there, which would fail every
// request in that window.
//
// catalogColumns remembers the columns it has found. Migrations only add, so a
// column found stays found and is not looked up again. A column not found is
// not remembered: the next request looks again, so the service starts reading
// it as soon as the migration has run, without a restart.
type catalogColumns struct {
	mu      sync.Mutex
	found   map[string]bool // "table.column"
	lookups int             // catalog lookups made, for the tests
}

// readableColumnsSQL selects which of the named columns of a table this
// session can read. to_regclass resolves the table as the queries do, through
// the search_path, and is NULL when there is no such table. has_column_privilege
// leaves out a column the role may not SELECT — every column of a table created
// after the read-only role was granted its tables, for one.
const readableColumnsSQL = `
	SELECT a.attname::text
	FROM pg_catalog.pg_attribute a
	WHERE a.attrelid = to_regclass($1)
	  AND a.attname = ANY ($2::text[])
	  AND a.attnum > 0 AND NOT a.attisdropped
	  AND has_column_privilege(a.attrelid, a.attnum, 'SELECT')`

// columns reports which of cols the catalog's table has and this role may
// read.
func (s *Store) columns(ctx context.Context, table string, cols ...string) (map[string]bool, error) {
	has := make(map[string]bool, len(cols))
	var missing []string
	s.schema.mu.Lock()
	for _, c := range cols {
		if s.schema.found[table+"."+c] {
			has[c] = true
		} else {
			missing = append(missing, c)
		}
	}
	s.schema.mu.Unlock()
	if len(missing) == 0 {
		return has, nil
	}

	rows, err := s.Pool.Query(ctx, readableColumnsSQL, table, missing)
	if err != nil {
		return nil, fmt.Errorf("look up the columns of %s: %w", table, err)
	}
	found, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("look up the columns of %s: %w", table, err)
	}

	s.schema.mu.Lock()
	defer s.schema.mu.Unlock()
	s.schema.lookups++
	if s.schema.found == nil {
		s.schema.found = map[string]bool{}
	}
	for _, c := range found {
		s.schema.found[table+"."+c] = true
		has[c] = true
	}
	return has, nil
}

// all reports whether every one of cols is in has.
func all(has map[string]bool, cols ...string) bool {
	for _, c := range cols {
		if !has[c] {
			return false
		}
	}
	return true
}
