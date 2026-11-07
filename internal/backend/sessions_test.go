package backend_test

import (
	"context"
	"testing"

	qt "github.com/frankban/quicktest"
	protocol "github.com/stokaro/ptah/provider-sdk/protocol/experimental"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/stokaro/ptah-duckdb-plugin/internal/backend"
	"github.com/stokaro/ptah-duckdb-plugin/internal/testcheck"
)

// These refusals come before any database is opened; the integration tests
// open real ones.
func TestOpen_FailurePath(t *testing.T) {
	tests := []struct {
		name    string
		request *protocol.OpenRequest
		code    codes.Code
		reason  string
	}{
		{"another engine", &protocol.OpenRequest{Engine: "sqlite", Address: "duckdb://:memory:"}, codes.InvalidArgument,
			"the provider serves the duckdb engine only"},
		{"no address", &protocol.OpenRequest{Engine: "duckdb"}, codes.InvalidArgument,
			"the address does not start with duckdb://"},
		{"unknown option", &protocol.OpenRequest{Engine: "duckdb", Address: "duckdb://app.duckdb?threads=2"}, codes.InvalidArgument,
			"the address carries an option the provider does not read; it reads access_mode only"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := testcheck.New(t)
			session, err := backend.New().Open(context.Background(), tc.request)
			c.Assert(session, qt.IsNil)
			c.Assert(status.Code(err), qt.Equals, tc.code)
			c.Assert(status.Convert(err).Message(), qt.Equals, tc.reason)
		})
	}
}

func TestClose_AnUnknownSessionSucceeds(t *testing.T) {
	c := testcheck.New(t)
	c.Assert(backend.New().Close(context.Background(), &protocol.CloseRequest{SessionId: "never-opened"}), qt.IsNil)
}

func TestPrepareCommonObjects_FailurePath(t *testing.T) {
	c := testcheck.New(t)
	prepared, err := backend.New().PrepareCommonObjects(context.Background(), &protocol.PrepareCommonObjectsRequest{Engine: "duckdb"})
	c.Assert(status.Code(err), qt.Equals, codes.FailedPrecondition)
	c.Assert(status.Convert(err).Message(), qt.Equals, "the provider does not prepare declared views, sequences or comments yet")
	c.Assert(prepared, qt.IsNil)
}

func TestNormalizeValues_HappyPath(t *testing.T) {
	c := testcheck.New(t)
	normalized, err := backend.New().NormalizeValues(context.Background(), &protocol.NormalizeValuesRequest{})
	c.Assert(err, qt.IsNil)
	c.Assert(normalized.GetComplete(), qt.IsTrue)
}

func TestNormalizeValues_FailurePath(t *testing.T) {
	c := testcheck.New(t)
	normalized, err := backend.New().NormalizeValues(context.Background(), &protocol.NormalizeValuesRequest{Values: []*protocol.ValueInput{{}}})
	c.Assert(status.Code(err), qt.Equals, codes.InvalidArgument)
	c.Assert(normalized, qt.IsNil)
}
