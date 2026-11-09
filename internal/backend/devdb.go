package backend

import (
	"context"
	"crypto/rand"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"google.golang.org/grpc/codes"
	protocol "ptah.run/provider-sdk/protocol/experimental"
	"ptah.run/provider-sdk/provider"
	providerruntime "ptah.run/provider-sdk/runtime"

	"github.com/stokaro/ptah-duckdb-plugin/internal/duckdbversion"
)

// ProvisionDevDatabase answers `--dev-url docker://duckdb` with a new
// database file in a directory of its own under the system's temporary
// directory: DuckDB runs inside the provider, so a dev database needs no
// container. A file rather than an in-memory database, because the host
// opens the dev database by an address it parses as a URL, which
// duckdb://:memory: is not. The spec may name the linked DuckDB,
// `docker://duckdb/2.0` or `docker://duckdb/2.0.0`, and no other version: the
// provider has no other. The release removes the directory; a process that
// ends without one leaves it in the temporary directory.
func (p *Provider) ProvisionDevDatabase(ctx context.Context, request *protocol.ProvisionDevRequest) (*protocol.DevDatabase, error) {
	description := provider.NegotiatedDescription(Description(), provider.HostFeatures(ctx))
	if err := provider.ValidateProvisionDevRequest(description, request); err != nil {
		return nil, providerruntime.ErrorWithReason(codes.InvalidArgument, "the dev database request is not one the provider can answer")
	}
	if reason := devSpecRefusal(request.GetSpec()); reason != "" {
		return &protocol.DevDatabase{Refusal: &protocol.DevDatabaseRefusal{Kind: protocol.DevDatabaseRefusal_UNSUPPORTED, Reason: reason}}, nil
	}
	dir, err := os.MkdirTemp("", "ptah-duckdb-dev-")
	if err != nil {
		return &protocol.DevDatabase{Refusal: &protocol.DevDatabaseRefusal{Kind: protocol.DevDatabaseRefusal_UNAVAILABLE,
			Reason: "the provider could not create a directory for the dev database in the temporary directory"}}, nil
	}
	lease := rand.Text()
	p.devMu.Lock()
	defer p.devMu.Unlock()
	if p.devDirs == nil {
		p.devDirs = make(map[string]string)
	}
	p.devDirs[lease] = dir
	return &protocol.DevDatabase{Lease: lease, Url: fileURL(filepath.Join(dir, "dev.duckdb"))}, nil
}

// fileURL is the address of the database file at path, an absolute path, in
// a form both a URL parser and the provider's address grammar read: a drive
// path takes a slash before it.
func fileURL(path string) string {
	slashed := filepath.ToSlash(path)
	if !strings.HasPrefix(slashed, "/") {
		slashed = "/" + slashed
	}
	return "duckdb://" + slashed
}

// devSpecRefusal says why the provider does not provision a spec, or returns
// the empty string. The host sends a spec whose engine is duckdb, in either
// docker form.
func devSpecRefusal(spec string) string {
	parsed, err := url.Parse(spec)
	switch {
	case err != nil || parsed.Scheme != "docker" || !strings.EqualFold(parsed.Host, engineID):
		return "DuckDB runs inside the provider and has no image; write --dev-url docker://duckdb, or the address of a database file"
	case parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil:
		return "a duckdb dev database takes no options"
	}
	linked := strings.TrimPrefix(duckdbversion.Library, "v")
	version, _, _ := strings.Cut(strings.Trim(parsed.Path, "/"), "/")
	if version != "" && version != linked && !strings.HasPrefix(linked, version+".") {
		return "the provider links DuckDB " + linked + " and provisions that version only"
	}
	return ""
}

// ReleaseDevDatabase removes the directory of the dev database a lease
// names. Releasing a lease that is gone, or one the provider never gave,
// succeeds and removes nothing.
func (p *Provider) ReleaseDevDatabase(ctx context.Context, request *protocol.ReleaseDevRequest) (*protocol.Empty, error) {
	description := provider.NegotiatedDescription(Description(), provider.HostFeatures(ctx))
	if err := provider.ValidateReleaseDevRequest(description, request); err != nil {
		return nil, providerruntime.ErrorWithReason(codes.InvalidArgument, "the dev database release is not one the provider can answer")
	}
	p.devMu.Lock()
	defer p.devMu.Unlock()
	dir, found := p.devDirs[request.GetLease()]
	if !found {
		return &protocol.Empty{}, nil
	}
	if err := os.RemoveAll(dir); err != nil {
		return nil, providerruntime.ErrorWithReason(codes.FailedPrecondition, "the provider could not remove the dev database; a session may still hold it")
	}
	delete(p.devDirs, request.GetLease())
	return &protocol.Empty{}, nil
}
