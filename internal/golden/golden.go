// Package golden compares test output with testdata/<name>.golden; -update rewrites the files.
package golden

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var update = flag.Bool("update", false, "rewrite testdata/*.golden with the current output")

// Assert fails t when got differs from testdata/<name>.golden, or writes that file under -update.
func Assert(t testing.TB, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		require.NoError(t, os.MkdirAll("testdata", 0o755))
		require.NoError(t, os.WriteFile(path, got, 0o644))
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "run go test with -update to create %s", path)
	assert.Equal(t, string(want), string(got), "output differs from %s; rerun with -update if the change is intended", path)
}
