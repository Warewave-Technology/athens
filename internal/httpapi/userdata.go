package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/Warewave-Technology/athens/internal/httpx"
	"github.com/Warewave-Technology/athens/internal/reqctx"
	"github.com/Warewave-Technology/athens/internal/store"
)

const maxUIStateBytes = 16 << 10

func mapStoreErr(err error) error {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return httpx.ErrNotFound
	case errors.Is(err, store.ErrConflict):
		return httpx.NewError(http.StatusConflict, "conflict", "an item with this name already exists")
	}
	return err
}

func (s *Server) getUIState(w http.ResponseWriter, r *http.Request) error {
	key := r.PathValue("key")
	if len(key) > 200 {
		return httpx.BadRequest("key too long")
	}
	v, err := s.Store.GetUIState(r.Context(), reqctx.UserFrom(r.Context()).ID, key)
	if err != nil {
		return mapStoreErr(err)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]json.RawMessage{"value": json.RawMessage(v)})
	return nil
}

func (s *Server) putUIState(w http.ResponseWriter, r *http.Request) error {
	key := r.PathValue("key")
	if len(key) > 200 {
		return httpx.BadRequest("key too long")
	}
	var body struct {
		Value json.RawMessage `json:"value"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		return err
	}
	if len(body.Value) == 0 || len(body.Value) > maxUIStateBytes {
		return httpx.NewError(http.StatusRequestEntityTooLarge, "too_large", "value must be at most 16 KiB of JSON")
	}
	if err := s.Store.PutUIState(r.Context(), reqctx.UserFrom(r.Context()).ID, key, string(body.Value)); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
