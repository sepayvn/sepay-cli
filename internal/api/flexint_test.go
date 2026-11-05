package api

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFlexInt_RejectsNonFiniteAndOutOfRange(t *testing.T) {
	for _, bad := range []string{`"Inf"`, `"-Inf"`, `"NaN"`, `"1e30"`, `"-1e30"`} {
		var n FlexInt
		require.Error(t, json.Unmarshal([]byte(bad), &n), "%s must be rejected", bad)
	}
	for in, want := range map[string]int{`"100000.00"`: 100000, `100000`: 100000, `"0"`: 0} {
		var n FlexInt
		require.NoError(t, json.Unmarshal([]byte(in), &n))
		assert.Equal(t, want, int(n))
	}
}

func TestFlexInt_AcceptsTheBooleanSpelling(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int
	}{
		{`1`, 1},
		{`"1"`, 1},
		{`true`, 1},
		{`"true"`, 1},
		{`0`, 0},
		{`"0"`, 0},
		{`false`, 0},
		{`"false"`, 0},
		{`null`, 0},
	} {
		t.Run(tc.in, func(t *testing.T) {
			var n FlexInt
			require.NoError(t, json.Unmarshal([]byte(tc.in), &n))
			assert.Equal(t, tc.want, int(n))
		})
	}
}

func TestFlexBool_AcceptsEveryWireForm(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{`true`, true},
		{`"true"`, true},
		{`1`, true},
		{`"1"`, true},
		{`false`, false},
		{`"false"`, false},
		{`0`, false},
		{`"0"`, false},
		{`null`, false},
		{`""`, false},
	} {
		t.Run(tc.in, func(t *testing.T) {
			var b FlexBool
			require.NoError(t, json.Unmarshal([]byte(tc.in), &b))
			assert.Equal(t, tc.want, bool(b))
		})
	}
}

func TestFlexBool_MarshalsBackAsABool(t *testing.T) {
	out, err := json.Marshal(struct {
		Active FlexBool `json:"active"`
	}{Active: true})
	require.NoError(t, err)
	assert.JSONEq(t, `{"active":true}`, string(out))
}

func TestFlexBool_RejectsNonsense(t *testing.T) {
	var b FlexBool
	assert.Error(t, json.Unmarshal([]byte(`"yes"`), &b))
}

func TestFlexInt_RejectsTheValueThatRoundsToTwoToThe63(t *testing.T) {
	for _, bad := range []string{
		`"9223372036854775808"`,  // 2^63 exactly
		`"9223372036854775809"`,  // rounds to 2^63
		`"18446744073709551616"`, // 2^64
		`"9223372036854775808.00"`,
		`"1e19"`,
	} {
		var n FlexInt
		require.Error(t, json.Unmarshal([]byte(bad), &n), "%s must be rejected, not wrapped", bad)
		assert.NotEqual(t, -9223372036854775808, int(n), "%s must never wrap negative", bad)
	}
}

func TestFlexInt_RejectsTheValueThatRoundsToMinInt64(t *testing.T) {
	for _, bad := range []string{
		`"-9223372036854775809"`,    // rounds to -2^63
		`-9223372036854775809`,      // same value as a JSON number
		`"-9223372036854775809.00"`, // the decimal branch answers this shape
		`"-18446744073709551616"`,   // -2^64
	} {
		var n FlexInt
		assert.Error(t, json.Unmarshal([]byte(bad), &n), "%s must be rejected, not clamped", bad)
	}
}

func TestFlexNum_RejectsNonFinite(t *testing.T) {
	for _, bad := range []string{`"NaN"`, `"Inf"`, `"-Inf"`, `"+Inf"`, `"Infinity"`} {
		var n FlexNum
		require.Error(t, json.Unmarshal([]byte(bad), &n), "%s must be rejected", bad)
	}
	for in, want := range map[string]float64{`"2.50"`: 2.5, `2.5`: 2.5, `"100000.00"`: 100000, `null`: 0} {
		var n FlexNum
		require.NoError(t, json.Unmarshal([]byte(in), &n))
		assert.Equal(t, want, float64(n))
	}
}

func TestFlexInt_StillAcceptsTheLargestValuesThatFit(t *testing.T) {
	for in, want := range map[string]int{
		`"9223372036854775807"`:     9223372036854775807, // int64 max
		`9223372036854775807`:       9223372036854775807,
		`"-9223372036854775808"`:    -9223372036854775808, // int64 min
		`"9223372036854775807.00"`:  9223372036854775807,  // int64 max on the wire form money uses
		`"-9223372036854775808.00"`: -9223372036854775808,
		`"9223372036854774784.00"`:  9223372036854774784,
	} {
		var n FlexInt
		require.NoError(t, json.Unmarshal([]byte(in), &n), "%s must still decode", in)
		assert.Equal(t, want, int(n), "%s", in)
	}
}

func TestFlexInt_DecimalIsExactPastTwoToThe53(t *testing.T) {
	for in, want := range map[string]int{
		`"9007199254740993.00"`:  9007199254740993,  // 2^53+1; via float64 this gave ...992
		`"12345678901234567.00"`: 12345678901234567, // via float64 this gave ...568
		`"100000.00"`:            100000,            // the everyday form, unchanged
		`"-250500.000"`:          -250500,
		`"0.00"`:                 0,
		`"1e5"`:                  100000, // exponent form still goes through float64
	} {
		var n FlexInt
		require.NoError(t, json.Unmarshal([]byte(in), &n), "%s must decode", in)
		assert.Equal(t, want, int(n), "%s", in)
	}

	for _, bad := range []string{
		`"9007199254740993.50"`, // float64 rounds the fraction away; the digits do not
		`"100000.50"`,
		`"100.001"`,
	} {
		var n FlexInt
		assert.Error(t, json.Unmarshal([]byte(bad), &n), "%s carries a real fraction and must error", bad)
	}
}
