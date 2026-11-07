// Package releasearchive writes the archive a release publishes for one
// platform. Every archive holds the provider's executable, README.md and
// LICENSE at its root, as a gzipped tar file, or a zip file on Windows.
//
// The provider links DuckDB through cgo, so each platform's executable is
// built on a runner of that platform and packed there; the release job then
// collects the archives. Two runs over the same files write the same bytes:
// entries are sorted by name, carry the modification time the caller gives
// and no owner, so the archive depends on its inputs and not on the runner.
package releasearchive

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"slices"
	"strings"
	"time"
)

// File is one entry of an archive: the file at Source, stored as Name with
// Mode.
type File struct {
	Name   string
	Source string
	Mode   fs.FileMode
}

// Write writes the archive called name, which ends in .tar.gz or .zip, of
// files to w. Every entry carries modified as its modification time.
func Write(w io.Writer, name string, modified time.Time, files []File) error {
	sorted := slices.Clone(files)
	slices.SortFunc(sorted, func(a, b File) int { return strings.Compare(a.Name, b.Name) })
	for i := 1; i < len(sorted); i++ {
		if sorted[i].Name == sorted[i-1].Name {
			return fmt.Errorf("the archive lists %s twice", sorted[i].Name)
		}
	}
	switch {
	case strings.HasSuffix(name, ".tar.gz"):
		return writeTarGz(w, modified, sorted)
	case strings.HasSuffix(name, ".zip"):
		return writeZip(w, modified, sorted)
	}
	return fmt.Errorf("archive %s is neither .tar.gz nor .zip", name)
}

func writeTarGz(w io.Writer, modified time.Time, files []File) error {
	compressed, err := gzip.NewWriterLevel(w, gzip.BestCompression)
	if err != nil {
		return err
	}
	archive := tar.NewWriter(compressed)
	for _, file := range files {
		contents, err := os.ReadFile(file.Source)
		if err != nil {
			return err
		}
		header := &tar.Header{
			Name: file.Name, Mode: int64(file.Mode.Perm()), Size: int64(len(contents)),
			ModTime: modified.UTC(), Typeflag: tar.TypeReg, Format: tar.FormatPAX,
		}
		if err := archive.WriteHeader(header); err != nil {
			return err
		}
		if _, err := archive.Write(contents); err != nil {
			return err
		}
	}
	return errors.Join(archive.Close(), compressed.Close())
}

func writeZip(w io.Writer, modified time.Time, files []File) error {
	archive := zip.NewWriter(w)
	for _, file := range files {
		contents, err := os.ReadFile(file.Source)
		if err != nil {
			return err
		}
		header := &zip.FileHeader{Name: file.Name, Method: zip.Deflate, Modified: modified.UTC()}
		header.SetMode(file.Mode.Perm())
		entry, err := archive.CreateHeader(header)
		if err != nil {
			return err
		}
		if _, err := entry.Write(contents); err != nil {
			return err
		}
	}
	return archive.Close()
}
