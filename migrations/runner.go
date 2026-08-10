package migrations

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed *.up.sql
var files embed.FS

type migration struct {
	version int64
	name    string
	sql     string
}

func Apply(ctx context.Context, pool *pgxpool.Pool) error {
	migrations, err := load()
	if err != nil {
		return err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin migrations transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext('knowflow-migrations'))"); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	if _, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
        version BIGINT PRIMARY KEY,
        name TEXT NOT NULL,
        applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
    )`); err != nil {
		return fmt.Errorf("create schema migrations table: %w", err)
	}
	for _, migration := range migrations {
		var applied bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)", migration.version).Scan(&applied); err != nil {
			return fmt.Errorf("check migration %s: %w", migration.name, err)
		}
		if applied {
			continue
		}
		if _, err := tx.Exec(ctx, migration.sql); err != nil {
			return fmt.Errorf("apply migration %s: %w", migration.name, err)
		}
		if _, err := tx.Exec(ctx, "INSERT INTO schema_migrations (version, name) VALUES ($1, $2)", migration.version, migration.name); err != nil {
			return fmt.Errorf("record migration %s: %w", migration.name, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit migrations: %w", err)
	}
	return nil
}

func load() ([]migration, error) {
	names, err := fs.Glob(files, "*.up.sql")
	if err != nil {
		return nil, fmt.Errorf("list migrations: %w", err)
	}
	sort.Strings(names)
	migrations := make([]migration, 0, len(names))
	var previous int64
	for _, name := range names {
		prefix, _, ok := strings.Cut(filepath.Base(name), "_")
		if !ok {
			return nil, fmt.Errorf("invalid migration filename %q", name)
		}
		version, err := strconv.ParseInt(prefix, 10, 64)
		if err != nil || version <= previous {
			return nil, fmt.Errorf("migration versions must be positive and strictly increasing: %q", name)
		}
		contents, err := files.ReadFile(name)
		if err != nil {
			return nil, fmt.Errorf("read migration %q: %w", name, err)
		}
		migrations = append(migrations, migration{version: version, name: name, sql: string(contents)})
		previous = version
	}
	if len(migrations) == 0 {
		return nil, fmt.Errorf("no migrations embedded")
	}
	return migrations, nil
}
