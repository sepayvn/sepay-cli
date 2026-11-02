package bankhub

import (
	"encoding/json"
	"fmt"
)

// DataInto decodes a single-resource Bank Hub response's "data" into T (detail).
func DataInto[T any](r *Response) (T, error) {
	var v T
	if len(r.Data) == 0 {
		return v, fmt.Errorf("empty response data")
	}
	if err := json.Unmarshal(r.Data, &v); err != nil {
		return v, fmt.Errorf("could not decode Bank Hub response: %w", err)
	}
	return v, nil
}

// ListInto decodes a Bank Hub list response's "data" into []T and returns its
// meta (nil when the response carries none).
func ListInto[T any](r *Response) ([]T, *Meta, error) {
	items := []T{} // non-nil so an empty list renders as [] (stable --json), not null
	if len(r.Data) > 0 {
		if err := json.Unmarshal(r.Data, &items); err != nil {
			return nil, nil, fmt.Errorf("could not decode Bank Hub response: %w", err)
		}
	}
	return items, r.Meta, nil
}
