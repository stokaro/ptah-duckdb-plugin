// Command releasearchive packs the provider executable built on this runner
// into the archive the release publishes for its platform:
//
//	go run ./internal/cmd/releasearchive -version 0.1.0 -os linux -arch amd64 \
//	  -executable bin/ptah-plugin-duckdb -out dist -modified 1767225600
//
// The archive is named as ptah-provider.json lists it, and holds the
// executable, README.md and LICENSE. -modified is a Unix time, the commit's,
// so two runs over one commit write the same bytes.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/stokaro/ptah-duckdb-plugin/internal/releasearchive"
	"github.com/stokaro/ptah-duckdb-plugin/internal/releasemanifest"
)

const project = "ptah-plugin-duckdb"

func main() {
	version := flag.String("version", "", "release version without the leading v")
	goos := flag.String("os", "", "operating system of the executable")
	goarch := flag.String("arch", "", "architecture of the executable")
	executable := flag.String("executable", "", "path of the built executable")
	out := flag.String("out", "dist", "directory the archive is written to")
	modified := flag.Int64("modified", 0, "modification time of every entry, as a Unix time")
	flag.Parse()
	if err := run(*version, releasemanifest.Platform{OS: *goos, Arch: *goarch}, *executable, *out, time.Unix(*modified, 0)); err != nil {
		fmt.Fprintln(os.Stderr, "releasearchive:", err)
		os.Exit(1)
	}
}

func run(version string, platform releasemanifest.Platform, executable, out string, modified time.Time) error {
	if version == "" || executable == "" {
		return fmt.Errorf("-version and -executable are required")
	}
	if !slices.Contains(releasemanifest.Platforms, platform) {
		return fmt.Errorf("%s/%s is not a platform the release ships", platform.OS, platform.Arch)
	}
	artifact := releasemanifest.ArtifactOf(project, project, version, platform)
	if err := os.MkdirAll(out, 0o750); err != nil {
		return err
	}
	file, err := os.Create(filepath.Join(out, artifact.Archive)) // #nosec G304 -- the release job names the output directory
	if err != nil {
		return err
	}
	files := []releasearchive.File{
		{Name: artifact.Executable, Source: executable, Mode: 0o755},
		{Name: "README.md", Source: "README.md", Mode: 0o644},
		{Name: "LICENSE", Source: "LICENSE", Mode: 0o644},
	}
	if err := releasearchive.Write(file, artifact.Archive, modified, files); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	fmt.Println(filepath.Join(out, artifact.Archive))
	return nil
}
