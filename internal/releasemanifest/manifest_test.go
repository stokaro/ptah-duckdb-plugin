package releasemanifest_test

import (
	"encoding/json"
	"testing"

	qt "github.com/frankban/quicktest"

	"github.com/stokaro/ptah-duckdb-plugin/internal/backend"
	"github.com/stokaro/ptah-duckdb-plugin/internal/releasemanifest"
	protocol "github.com/stokaro/ptah/provider-sdk/protocol/experimental"
)

func TestBuild_HappyPath(t *testing.T) {
	c := qt.New(t)
	first, err := releasemanifest.Build(backend.Description())
	c.Assert(err, qt.IsNil)
	second, err := releasemanifest.Build(backend.Description())
	c.Assert(err, qt.IsNil)
	c.Assert(string(first), qt.Equals, string(second))
	c.Assert(string(first[len(first)-1:]), qt.Equals, "\n")

	var manifest releasemanifest.Manifest
	c.Assert(json.Unmarshal(first, &manifest), qt.IsNil)
	c.Assert(manifest.Provider, qt.Equals, backend.ProviderID)
	c.Assert(manifest.Engines, qt.HasLen, 0)
}

func TestBuild_LeavesOutAnEngineWithoutLines(t *testing.T) {
	c := qt.New(t)
	description := &protocol.Description{ProviderId: "example.org/none", Engines: []*protocol.Engine{{Id: "none"}}}
	out, err := releasemanifest.Build(description)
	c.Assert(err, qt.IsNil)
	c.Assert(string(out), qt.Equals, "{\n  \"provider\": \"example.org/none\",\n  \"engines\": []\n}\n")
}
