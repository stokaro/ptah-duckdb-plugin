package releasearchive_test

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"

	"github.com/stokaro/ptah-duckdb-plugin/internal/releasearchive"
	"github.com/stokaro/ptah-duckdb-plugin/internal/testcheck"
)

var modified = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

// entry is what an archive holds for one file.
type entry struct {
	Name     string
	Mode     os.FileMode
	Modified time.Time
	Contents string
}

// sources writes the files an archive packs and returns them, listed out of
// name order.
func sources(c *qt.C) []releasearchive.File {
	c.Helper()
	dir := c.TempDir()
	write := map[string]string{"bin": "binary", "README.md": "readme", "LICENSE": "license"}
	for name, contents := range write {
		c.Assert(os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o600), qt.IsNil)
	}
	return []releasearchive.File{
		{Name: "ptah-plugin-duckdb", Source: filepath.Join(dir, "bin"), Mode: 0o755},
		{Name: "README.md", Source: filepath.Join(dir, "README.md"), Mode: 0o644},
		{Name: "LICENSE", Source: filepath.Join(dir, "LICENSE"), Mode: 0o644},
	}
}

func readTarGz(c *qt.C, data []byte) []entry {
	c.Helper()
	compressed, err := gzip.NewReader(bytes.NewReader(data))
	c.Assert(err, qt.IsNil)
	archive := tar.NewReader(compressed)
	var entries []entry
	for header, err := archive.Next(); err != io.EOF; header, err = archive.Next() {
		c.Assert(err, qt.IsNil)
		contents, err := io.ReadAll(archive)
		c.Assert(err, qt.IsNil)
		entries = append(entries, entry{header.Name, header.FileInfo().Mode().Perm(), header.ModTime.UTC(), string(contents)})
	}
	return entries
}

func readZip(c *qt.C, data []byte) []entry {
	c.Helper()
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	c.Assert(err, qt.IsNil)
	var entries []entry
	for _, file := range archive.File {
		opened, err := file.Open()
		c.Assert(err, qt.IsNil)
		contents, err := io.ReadAll(opened)
		c.Assert(err, qt.IsNil)
		c.Assert(opened.Close(), qt.IsNil)
		entries = append(entries, entry{file.Name, file.Mode().Perm(), file.Modified.UTC(), string(contents)})
	}
	return entries
}

func wantEntries() []entry {
	return []entry{
		{"LICENSE", 0o644, modified, "license"},
		{"README.md", 0o644, modified, "readme"},
		{"ptah-plugin-duckdb", 0o755, modified, "binary"},
	}
}

func TestWriteTarGz_HappyPath(t *testing.T) {
	c := testcheck.New(t)
	files := sources(c)
	var first, second bytes.Buffer
	c.Assert(releasearchive.Write(&first, "p_1.0.0_linux_amd64.tar.gz", modified, files), qt.IsNil)
	c.Assert(releasearchive.Write(&second, "p_1.0.0_linux_amd64.tar.gz", modified, files), qt.IsNil)
	c.Assert(first.Bytes(), qt.DeepEquals, second.Bytes())
	c.Assert(readTarGz(c, first.Bytes()), qt.DeepEquals, wantEntries())
}

func TestWriteZip_HappyPath(t *testing.T) {
	c := testcheck.New(t)
	files := sources(c)
	var first, second bytes.Buffer
	c.Assert(releasearchive.Write(&first, "p_1.0.0_windows_amd64.zip", modified, files), qt.IsNil)
	c.Assert(releasearchive.Write(&second, "p_1.0.0_windows_amd64.zip", modified, files), qt.IsNil)
	c.Assert(first.Bytes(), qt.DeepEquals, second.Bytes())
	c.Assert(readZip(c, first.Bytes()), qt.DeepEquals, wantEntries())
}

func TestWriteUnknownFormat_FailurePath(t *testing.T) {
	c := testcheck.New(t)
	var out bytes.Buffer
	err := releasearchive.Write(&out, "p_1.0.0_linux_amd64.rar", modified, sources(c))
	c.Assert(err, qt.ErrorMatches, `archive p_1.0.0_linux_amd64.rar is neither .tar.gz nor .zip`)
	c.Assert(out.Len(), qt.Equals, 0)
}

func TestWriteRepeatedEntry_FailurePath(t *testing.T) {
	c := testcheck.New(t)
	files := sources(c)
	var out bytes.Buffer
	err := releasearchive.Write(&out, "p_1.0.0_linux_amd64.tar.gz", modified, append(files, files[2]))
	c.Assert(err, qt.ErrorMatches, `the archive lists LICENSE twice`)
	c.Assert(out.Len(), qt.Equals, 0)
}
