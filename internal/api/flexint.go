package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// FlexInt is an int that also decodes from a JSON string; some v2 endpoints send numbers as strings.
type FlexInt int

// UnmarshalJSON accepts a JSON number, a quoted number, or null (0).
func (n *FlexInt) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		*n = 0
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		s = string(bytes.TrimSpace([]byte(s)))
		if s == "" {
			*n = 0
			return nil
		}
		if v, ok := parseBoolWord(s); ok {
			*n = FlexInt(v)
			return nil
		}
		v, err := parseWholeNumber(s)
		if err != nil {
			return err
		}
		*n = FlexInt(v)
		return nil
	}
	if v, ok := parseBoolWord(string(b)); ok {
		*n = FlexInt(v)
		return nil
	}
	v, err := parseWholeNumber(string(b))
	if err != nil {
		return err
	}
	*n = FlexInt(v)
	return nil
}

// parseBoolWord maps true/false to 1/0; endpoints disagree on the wire form.
func parseBoolWord(s string) (int, bool) {
	switch s {
	case "true":
		return 1, true
	case "false":
		return 0, true
	}
	return 0, false
}

// parseWholeNumber accepts "100000.00" but rejects a real fraction: VND has no minor unit.
func parseWholeNumber(s string) (int, error) {
	v, err := strconv.Atoi(s)
	if err == nil {
		return v, nil
	}
	// A whole number out of range: float64 rounding could slip it past the bounds check.
	if errors.Is(err, strconv.ErrRange) {
		return 0, fmt.Errorf("value out of range for an integer: %q", s)
	}
	// Settle decimals on their digits: float64 loses precision past 2^53.
	if intPart, frac, plain := splitDecimal(s); plain {
		if strings.Trim(frac, "0") != "" {
			return 0, fmt.Errorf("expected a whole number, got %q", s)
		}
		v, err := strconv.Atoi(intPart)
		if err != nil {
			return 0, fmt.Errorf("value out of range for an integer: %q", s)
		}
		return v, nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid integer %q", s)
	}
	// Inf passes math.Trunc. >= because float64(math.MaxInt64) rounds up to 2^63.
	if math.IsInf(f, 0) || math.IsNaN(f) || f >= math.MaxInt64 || f < math.MinInt64 {
		return 0, fmt.Errorf("value out of range for an integer: %q", s)
	}
	if f != math.Trunc(f) {
		return 0, fmt.Errorf("expected a whole number, got %q", s)
	}
	return int(f), nil
}

// splitDecimal returns plain=false for any shape but a plain decimal.
func splitDecimal(s string) (intPart, frac string, plain bool) {
	dot := strings.IndexByte(s, '.')
	if dot < 0 {
		return "", "", false
	}
	digits := func(v string) bool {
		for i := 0; i < len(v); i++ {
			if v[i] < '0' || v[i] > '9' {
				return false
			}
		}
		return true
	}
	intPart, frac = s[:dot], s[dot+1:]
	head := intPart
	if head != "" && (head[0] == '+' || head[0] == '-') {
		head = head[1:]
	}
	if head == "" || !digits(head) || !digits(frac) {
		return "", "", false
	}
	return intPart, frac, true
}

// FlexNum is a float that also decodes from a JSON string. Money uses FlexInt.
type FlexNum float64

// UnmarshalJSON accepts a JSON number, a quoted number, or null (0).
func (n *FlexNum) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		*n = 0
		return nil
	}
	s := string(b)
	if b[0] == '"' {
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		s = strings.TrimSpace(s)
		if s == "" {
			*n = 0
			return nil
		}
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return fmt.Errorf("invalid number %q", s)
	}
	// A quoted "NaN"/"Inf" would re-encode as invalid JSON.
	if math.IsInf(f, 0) || math.IsNaN(f) {
		return fmt.Errorf("invalid number %q", s)
	}
	*n = FlexNum(f)
	return nil
}

// String renders the value without trailing zeros.
func (n FlexNum) String() string {
	return strconv.FormatFloat(float64(n), 'f', -1, 64)
}

// FlexBool is a bool that also decodes from 1/0 or a quoted bool or number.
type FlexBool bool

// UnmarshalJSON accepts a bool, a number, either quoted, or null (false).
func (b *FlexBool) UnmarshalJSON(raw []byte) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		*b = false
		return nil
	}
	s := string(raw)
	if raw[0] == '"' {
		if err := json.Unmarshal(raw, &s); err != nil {
			return err
		}
		s = strings.TrimSpace(s)
		if s == "" {
			*b = false
			return nil
		}
	}
	if v, ok := parseBoolWord(s); ok {
		*b = v == 1
		return nil
	}
	v, err := parseWholeNumber(s)
	if err != nil {
		return fmt.Errorf("invalid boolean %q", s)
	}
	*b = v != 0
	return nil
}
