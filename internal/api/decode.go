package api

import (
	"encoding/json"
	"fmt"
)

// DataInto decodes a single-resource envelope's data into T.
func DataInto[T any](env *Envelope) (T, error) {
	var v T
	if len(env.Data) == 0 {
		return v, fmt.Errorf("empty response data")
	}
	if err := json.Unmarshal(env.Data, &v); err != nil {
		return v, fmt.Errorf("could not decode response: %w", err)
	}
	return v, nil
}

// ListInto decodes a list envelope's data into []T and returns its pagination
// (nil when the response carries no meta).
func ListInto[T any](env *Envelope) ([]T, *Pagination, error) {
	items := []T{} // non-nil so an empty list renders as [] (stable --json), not null
	if len(env.Data) > 0 {
		if err := json.Unmarshal(env.Data, &items); err != nil {
			return nil, nil, fmt.Errorf("could not decode response: %w", err)
		}
	}
	var pg *Pagination
	if env.Meta != nil {
		pg = &env.Meta.Pagination
	}
	return items, pg, nil
}
