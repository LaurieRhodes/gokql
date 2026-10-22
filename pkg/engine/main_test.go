package engine

import (
	"os"
	"testing"
)

// TestMain clears GOKQL_EMBED for the whole package.
//
// Found by accident, which is the point: a shell with GOKQL_EMBED set
// (entirely normal for anyone actually using this feature) silently
// redirected every mocked embedding test at a real server. The tests
// did not error, they just measured the wrong thing -- 768-dim live
// vectors where the fixture expected [5,0,0]. A test suite whose
// result depends on the developer's environment is not a test suite.
//
// Engine.SetEmbedder and the EmbedEndpoint var remain the supported
// ways for a test to choose a backend; the environment is not.
func TestMain(m *testing.M) {
	os.Unsetenv("GOKQL_EMBED")
	os.Unsetenv("GOKQL_EMBED_API_KEY")
	os.Exit(m.Run())
}
