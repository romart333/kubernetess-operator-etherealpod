package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeStore is an in-memory Store double keyed by user then product.
type fakeStore struct {
	items map[string]map[string]int64
	err   error
}

func newFakeStore() *fakeStore {
	return &fakeStore{items: map[string]map[string]int64{}}
}

func (f *fakeStore) AddAmount(_ context.Context, userID, productName string, amount int64) (int64, error) {
	if f.err != nil {
		return 0, f.err
	}
	if f.items[userID] == nil {
		f.items[userID] = map[string]int64{}
	}
	f.items[userID][productName] += amount
	return f.items[userID][productName], nil
}

func (f *fakeStore) SumProduct(_ context.Context, productName string) (int64, bool, error) {
	if f.err != nil {
		return 0, false, f.err
	}
	var sum int64
	found := false
	for _, products := range f.items {
		if amount, ok := products[productName]; ok {
			sum += amount
			found = true
		}
	}
	return sum, found, nil
}

func (f *fakeStore) DeleteProduct(_ context.Context, productName string) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	deleted := false
	for _, products := range f.items {
		if _, ok := products[productName]; ok {
			delete(products, productName)
			deleted = true
		}
	}
	return deleted, nil
}

func newTestHandler(t *testing.T, store Store) http.Handler {
	t.Helper()
	return newHandler(store, slog.New(slog.DiscardHandler), func(int) {
		t.Fatal("exit must not be called outside /crash")
	})
}

func doRequest(t *testing.T, handler http.Handler, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

func decodeJSON(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	return body
}

func TestWriteEndpoint(t *testing.T) {
	t.Run("valid write returns the new user total", func(t *testing.T) {
		store := newFakeStore()
		handler := newTestHandler(t, store)
		_, err := store.AddAmount(context.Background(), "thor", "beer", 2)
		require.NoError(t, err)

		rec := doRequest(t, handler, http.MethodPost, "/write?user_id=thor&product_name=beer&amount=3")

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, map[string]any{
			"user_id": "thor", "product_name": "beer", "amount": float64(5),
		}, decodeJSON(t, rec))
	})

	invalid := []struct {
		name   string
		target string
	}{
		{"missing user_id", "/write?product_name=beer&amount=1"},
		{"missing product_name", "/write?user_id=thor&amount=1"},
		{"missing amount", "/write?user_id=thor&product_name=beer"},
		{"uppercase user_id", "/write?user_id=Thor&product_name=beer&amount=1"},
		{"digits in product_name", "/write?user_id=thor&product_name=beer1&amount=1"},
		{"non-integer amount", "/write?user_id=thor&product_name=beer&amount=one"},
		{"zero amount", "/write?user_id=thor&product_name=beer&amount=0"},
		{"negative amount", "/write?user_id=thor&product_name=beer&amount=-1"},
		{"amount above limit", "/write?user_id=thor&product_name=beer&amount=1000001"},
	}
	for _, tt := range invalid {
		t.Run(tt.name+" is a 400", func(t *testing.T) {
			handler := newTestHandler(t, newFakeStore())

			rec := doRequest(t, handler, http.MethodPost, tt.target)

			assert.Equal(t, http.StatusBadRequest, rec.Code)
			assert.Contains(t, decodeJSON(t, rec), "error")
		})
	}

	t.Run("amount at the upper limit is accepted", func(t *testing.T) {
		handler := newTestHandler(t, newFakeStore())

		rec := doRequest(t, handler, http.MethodPost, "/write?user_id=thor&product_name=beer&amount=1000000")

		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("GET on /write is a 405", func(t *testing.T) {
		handler := newTestHandler(t, newFakeStore())

		rec := doRequest(t, handler, http.MethodGet, "/write?user_id=thor&product_name=beer&amount=1")

		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	})

	t.Run("store failure is a 500", func(t *testing.T) {
		store := newFakeStore()
		store.err = errors.New("disk gone")
		handler := newTestHandler(t, store)

		rec := doRequest(t, handler, http.MethodPost, "/write?user_id=thor&product_name=beer&amount=1")

		assert.Equal(t, http.StatusInternalServerError, rec.Code)
		assert.Contains(t, decodeJSON(t, rec), "error")
	})
}

func TestGetProductAmountEndpoint(t *testing.T) {
	t.Run("sums the product across users", func(t *testing.T) {
		store := newFakeStore()
		ctx := context.Background()
		_, err := store.AddAmount(ctx, "loki", "apple", 1)
		require.NoError(t, err)
		_, err = store.AddAmount(ctx, "thor", "apple", 3)
		require.NoError(t, err)
		handler := newTestHandler(t, store)

		rec := doRequest(t, handler, http.MethodGet, "/get_product_amount?product_name=apple")

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, map[string]any{
			"product_name": "apple", "amount": float64(4),
		}, decodeJSON(t, rec))
	})

	t.Run("unknown product is a 404", func(t *testing.T) {
		handler := newTestHandler(t, newFakeStore())

		rec := doRequest(t, handler, http.MethodGet, "/get_product_amount?product_name=mjolnir")

		assert.Equal(t, http.StatusNotFound, rec.Code)
		assert.Equal(t, map[string]any{"error": "product not found"}, decodeJSON(t, rec))
	})

	t.Run("missing product_name is a 400", func(t *testing.T) {
		handler := newTestHandler(t, newFakeStore())

		rec := doRequest(t, handler, http.MethodGet, "/get_product_amount")

		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("invalid product_name is a 400", func(t *testing.T) {
		handler := newTestHandler(t, newFakeStore())

		rec := doRequest(t, handler, http.MethodGet, "/get_product_amount?product_name=Apple")

		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("POST on /get_product_amount is a 405", func(t *testing.T) {
		handler := newTestHandler(t, newFakeStore())

		rec := doRequest(t, handler, http.MethodPost, "/get_product_amount?product_name=apple")

		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	})
}

func TestDeleteProductEndpoint(t *testing.T) {
	t.Run("deletes an existing product", func(t *testing.T) {
		store := newFakeStore()
		_, err := store.AddAmount(context.Background(), "thor", "beer", 3)
		require.NoError(t, err)
		handler := newTestHandler(t, store)

		rec := doRequest(t, handler, http.MethodDelete, "/delete_product?product_name=beer")

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, map[string]any{"deleted": "beer"}, decodeJSON(t, rec))
	})

	t.Run("absent product is a 404", func(t *testing.T) {
		handler := newTestHandler(t, newFakeStore())

		rec := doRequest(t, handler, http.MethodDelete, "/delete_product?product_name=beer")

		assert.Equal(t, http.StatusNotFound, rec.Code)
	})

	t.Run("invalid product_name is a 400", func(t *testing.T) {
		handler := newTestHandler(t, newFakeStore())

		rec := doRequest(t, handler, http.MethodDelete, "/delete_product?product_name=BEER")

		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("GET on /delete_product is a 405", func(t *testing.T) {
		handler := newTestHandler(t, newFakeStore())

		rec := doRequest(t, handler, http.MethodGet, "/delete_product?product_name=beer")

		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	})
}

func TestHealthz(t *testing.T) {
	handler := newTestHandler(t, newFakeStore())

	rec := doRequest(t, handler, http.MethodGet, "/healthz")

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "ok", rec.Body.String())
}

func TestCrash(t *testing.T) {
	var exitCode int
	handler := newHandler(newFakeStore(), slog.New(slog.DiscardHandler), func(code int) {
		exitCode = code
	})

	rec := doRequest(t, handler, http.MethodPost, "/crash")

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, 2, exitCode)
}
