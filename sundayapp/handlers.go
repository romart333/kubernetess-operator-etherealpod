package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
)

// Store is the persistence contract the HTTP layer consumes; the SQLite
// implementation lives in store.go and tests substitute a fake.
type Store interface {
	// AddAmount adds amount to the user's stock of the product and returns
	// the user's new total for that product.
	AddAmount(ctx context.Context, userID, productName string, amount int64) (int64, error)
	// SumProduct returns the product's total across all users and whether
	// any user has it.
	SumProduct(ctx context.Context, productName string) (sum int64, found bool, err error)
	// DeleteProduct removes the product for all users and reports whether
	// any row existed.
	DeleteProduct(ctx context.Context, productName string) (deleted bool, err error)
}

// Validation rules for query parameters.
var nameRe = regexp.MustCompile(`^[a-z]+$`)

const (
	amountMin = 1
	amountMax = 1_000_000
)

type handler struct {
	store Store
	log   *slog.Logger
	exit  func(code int)
}

// newHandler wires the HTTP routes. exit is what /crash calls after
// responding (os.Exit in production, a spy in tests).
func newHandler(store Store, log *slog.Logger, exit func(code int)) http.Handler {
	h := &handler{store: store, log: log, exit: exit}

	// Method-qualified patterns make the mux answer 405 for wrong methods.
	mux := http.NewServeMux()
	mux.HandleFunc("POST /write", h.write)
	mux.HandleFunc("GET /get_product_amount", h.getProductAmount)
	mux.HandleFunc("DELETE /delete_product", h.deleteProduct)
	mux.HandleFunc("GET /healthz", h.healthz)
	mux.HandleFunc("POST /crash", h.crash)
	return mux
}

func (h *handler) write(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.validName(w, r, "user_id")
	if !ok {
		return
	}
	productName, ok := h.validName(w, r, "product_name")
	if !ok {
		return
	}
	amount, err := strconv.ParseInt(r.URL.Query().Get("amount"), 10, 64)
	if err != nil || amount < amountMin || amount > amountMax {
		h.writeError(w, http.StatusBadRequest,
			fmt.Sprintf("amount must be an integer in [%d, %d]", amountMin, amountMax))
		return
	}

	total, err := h.store.AddAmount(r.Context(), userID, productName, amount)
	if err != nil {
		h.storeError(w, r, "write", err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{
		"user_id": userID, "product_name": productName, "amount": total,
	})
}

func (h *handler) getProductAmount(w http.ResponseWriter, r *http.Request) {
	productName, ok := h.validName(w, r, "product_name")
	if !ok {
		return
	}

	sum, found, err := h.store.SumProduct(r.Context(), productName)
	if err != nil {
		h.storeError(w, r, "get_product_amount", err)
		return
	}
	if !found {
		h.writeError(w, http.StatusNotFound, "product not found")
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{
		"product_name": productName, "amount": sum,
	})
}

func (h *handler) deleteProduct(w http.ResponseWriter, r *http.Request) {
	productName, ok := h.validName(w, r, "product_name")
	if !ok {
		return
	}

	deleted, err := h.store.DeleteProduct(r.Context(), productName)
	if err != nil {
		h.storeError(w, r, "delete_product", err)
		return
	}
	if !deleted {
		h.writeError(w, http.StatusNotFound, "product not found")
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"deleted": productName})
}

func (h *handler) healthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write([]byte("ok")); err != nil {
		h.log.Error("write healthz response", "error", err)
	}
}

// crash is the demo helper: respond, then terminate the process abnormally
// so the kubelet restarts the container and RESTARTS goes up by one.
func (h *handler) crash(w http.ResponseWriter, r *http.Request) {
	h.log.InfoContext(r.Context(), "crash requested, exiting")
	h.writeJSON(w, http.StatusOK, map[string]any{"crashing": true})
	// Flush so the demo curl sees the response before the process dies.
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	h.exit(2)
}

// validName extracts and validates a ^[a-z]+$ query parameter, answering 400
// itself when invalid.
func (h *handler) validName(w http.ResponseWriter, r *http.Request, param string) (string, bool) {
	value := r.URL.Query().Get(param)
	if !nameRe.MatchString(value) {
		h.writeError(w, http.StatusBadRequest,
			fmt.Sprintf("%s must be non-empty lowercase letters (a-z)", param))
		return "", false
	}
	return value, true
}

func (h *handler) storeError(w http.ResponseWriter, r *http.Request, op string, err error) {
	h.log.ErrorContext(r.Context(), "store operation failed", "op", op, "error", err)
	h.writeError(w, http.StatusInternalServerError, "internal error")
}

func (h *handler) writeError(w http.ResponseWriter, status int, msg string) {
	h.writeJSON(w, status, map[string]any{"error": msg})
}

func (h *handler) writeJSON(w http.ResponseWriter, status int, body map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		h.log.Error("encode response", "error", err)
	}
}
