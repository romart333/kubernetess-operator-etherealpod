package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"

	_ "modernc.org/sqlite"
)

// schema is idempotent so every startup can apply it unconditionally.
const schema = `
CREATE TABLE IF NOT EXISTS items (
  user_id      TEXT NOT NULL,
  product_name TEXT NOT NULL,
  amount       INTEGER NOT NULL CHECK (amount > 0),
  PRIMARY KEY (user_id, product_name)
);`

// SQLiteStore persists grocery items in a single SQLite database file.
// Durability comes from WAL journaling with synchronous=FULL (every commit
// reaches disk before returning), and single-writer safety from limiting the
// pool to one connection — no extra locking is needed.
type SQLiteStore struct {
	db *sql.DB
}

// NewSQLiteStore opens (creating if needed) the database at path and applies
// the schema.
func NewSQLiteStore(ctx context.Context, path string) (*SQLiteStore, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)",
		url.PathEscape(path))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database %q: %w", path, err)
	}
	db.SetMaxOpenConns(1)

	if _, err := db.ExecContext(ctx, schema); err != nil {
		return nil, errors.Join(
			fmt.Errorf("apply schema to %q: %w", path, err),
			db.Close(),
		)
	}
	return &SQLiteStore{db: db}, nil
}

// Close releases the underlying database handle.
func (s *SQLiteStore) Close() error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("close database: %w", err)
	}
	return nil
}

// AddAmount adds amount to the user's stock of the product (upsert-increment)
// and returns the user's new total for that product.
func (s *SQLiteStore) AddAmount(ctx context.Context, userID, productName string, amount int64) (int64, error) {
	const query = `
INSERT INTO items (user_id, product_name, amount) VALUES (?, ?, ?)
ON CONFLICT (user_id, product_name) DO UPDATE SET amount = amount + excluded.amount
RETURNING amount;`

	var total int64
	if err := s.db.QueryRowContext(ctx, query, userID, productName, amount).Scan(&total); err != nil {
		return 0, fmt.Errorf("add %d of %q for user %q: %w", amount, productName, userID, err)
	}
	return total, nil
}

// SumProduct returns the product's total amount summed across all users;
// found reports whether any user has the product at all.
func (s *SQLiteStore) SumProduct(ctx context.Context, productName string) (sum int64, found bool, err error) {
	const query = `SELECT COALESCE(SUM(amount), 0), COUNT(*) FROM items WHERE product_name = ?;`

	var rows int64
	if err := s.db.QueryRowContext(ctx, query, productName).Scan(&sum, &rows); err != nil {
		return 0, false, fmt.Errorf("sum product %q: %w", productName, err)
	}
	return sum, rows > 0, nil
}

// DeleteProduct removes the product for all users; deleted reports whether
// any row existed.
func (s *SQLiteStore) DeleteProduct(ctx context.Context, productName string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM items WHERE product_name = ?;`, productName)
	if err != nil {
		return false, fmt.Errorf("delete product %q: %w", productName, err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("delete product %q: rows affected: %w", productName, err)
	}
	return rows > 0, nil
}
