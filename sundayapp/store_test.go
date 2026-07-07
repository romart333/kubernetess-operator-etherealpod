package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestStore opens a store on a real SQLite file in a temp dir; modernc is
// pure Go, so this stays a unit test with no external infrastructure.
func newTestStore(t *testing.T) *SQLiteStore {
	t.Helper()
	store, err := NewSQLiteStore(context.Background(), filepath.Join(t.TempDir(), "sunday.db"))
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, store.Close()) })
	return store
}

func TestNewSQLiteStoreSchemaIsIdempotent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sunday.db")

	first, err := NewSQLiteStore(ctx, path)
	require.NoError(t, err)
	require.NoError(t, first.Close())

	second, err := NewSQLiteStore(ctx, path)
	require.NoError(t, err)
	assert.NoError(t, second.Close())
}

func TestAddAmount(t *testing.T) {
	ctx := context.Background()

	t.Run("first write returns the written amount", func(t *testing.T) {
		store := newTestStore(t)

		total, err := store.AddAmount(ctx, "loki", "apple", 1)

		require.NoError(t, err)
		assert.Equal(t, int64(1), total)
	})

	t.Run("duplicate user and product increments and returns the new user total", func(t *testing.T) {
		store := newTestStore(t)
		_, err := store.AddAmount(ctx, "thor", "beer", 2)
		require.NoError(t, err)

		total, err := store.AddAmount(ctx, "thor", "beer", 3)

		require.NoError(t, err)
		assert.Equal(t, int64(5), total)
	})

	t.Run("totals are tracked per user, not globally", func(t *testing.T) {
		store := newTestStore(t)
		_, err := store.AddAmount(ctx, "loki", "apple", 4)
		require.NoError(t, err)

		total, err := store.AddAmount(ctx, "thor", "apple", 2)

		require.NoError(t, err)
		assert.Equal(t, int64(2), total)
	})
}

func TestSumProduct(t *testing.T) {
	ctx := context.Background()

	t.Run("sums the product across all users", func(t *testing.T) {
		store := newTestStore(t)
		_, err := store.AddAmount(ctx, "loki", "apple", 1)
		require.NoError(t, err)
		_, err = store.AddAmount(ctx, "thor", "apple", 2)
		require.NoError(t, err)
		_, err = store.AddAmount(ctx, "thor", "beer", 5)
		require.NoError(t, err)

		sum, found, err := store.SumProduct(ctx, "apple")

		require.NoError(t, err)
		assert.True(t, found)
		assert.Equal(t, int64(3), sum)
	})

	t.Run("absent product reports not found", func(t *testing.T) {
		store := newTestStore(t)

		_, found, err := store.SumProduct(ctx, "mjolnir")

		require.NoError(t, err)
		assert.False(t, found)
	})
}

func TestDeleteProduct(t *testing.T) {
	ctx := context.Background()

	t.Run("deletes the product for all users", func(t *testing.T) {
		store := newTestStore(t)
		_, err := store.AddAmount(ctx, "loki", "beer", 1)
		require.NoError(t, err)
		_, err = store.AddAmount(ctx, "thor", "beer", 3)
		require.NoError(t, err)

		deleted, err := store.DeleteProduct(ctx, "beer")

		require.NoError(t, err)
		assert.True(t, deleted)
		_, found, err := store.SumProduct(ctx, "beer")
		require.NoError(t, err)
		assert.False(t, found)
	})

	t.Run("absent product reports nothing deleted", func(t *testing.T) {
		store := newTestStore(t)

		deleted, err := store.DeleteProduct(ctx, "mjolnir")

		require.NoError(t, err)
		assert.False(t, deleted)
	})

	t.Run("other products survive the delete", func(t *testing.T) {
		store := newTestStore(t)
		_, err := store.AddAmount(ctx, "loki", "apple", 4)
		require.NoError(t, err)
		_, err = store.AddAmount(ctx, "loki", "beer", 1)
		require.NoError(t, err)

		_, err = store.DeleteProduct(ctx, "beer")
		require.NoError(t, err)

		sum, found, err := store.SumProduct(ctx, "apple")
		require.NoError(t, err)
		assert.True(t, found)
		assert.Equal(t, int64(4), sum)
	})
}

func TestDataSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sunday.db")

	store, err := NewSQLiteStore(ctx, path)
	require.NoError(t, err)
	_, err = store.AddAmount(ctx, "loki", "apple", 3)
	require.NoError(t, err)
	require.NoError(t, store.Close())

	reopened, err := NewSQLiteStore(ctx, path)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, reopened.Close()) })

	sum, found, err := reopened.SumProduct(ctx, "apple")
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, int64(3), sum)
}
