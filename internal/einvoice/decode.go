package einvoice

import (
	"encoding/json"
	"fmt"
)

// DataInto decodes a single-resource response's "data" into T (detail).
func DataInto[T any](r *Response) (T, error) {
	var v T
	if len(r.Data) == 0 {
		return v, fmt.Errorf("empty response data")
	}
	if err := json.Unmarshal(r.Data, &v); err != nil {
		return v, fmt.Errorf("could not decode E-Invoice response: %w", err)
	}
	return v, nil
}

// ListInto decodes a list response's "data" (which nests "paging" + "items")
// into []T and returns the paging block (nil when the response carries none).
func ListInto[T any](r *Response) ([]T, *Paging, error) {
	var wrap struct {
		Paging *Paging `json:"paging"`
		Items  []T     `json:"items"`
	}
	if len(r.Data) > 0 {
		if err := json.Unmarshal(r.Data, &wrap); err != nil {
			return nil, nil, fmt.Errorf("could not decode E-Invoice response: %w", err)
		}
	}
	if wrap.Items == nil {
		wrap.Items = []T{} // non-nil so an empty list renders as [] (stable --json), not null
	}
	return wrap.Items, wrap.Paging, nil
}
