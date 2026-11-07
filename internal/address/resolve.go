package address

import "path/filepath"

// Resolve returns the absolute path of the file target names. A relative path
// is joined to dir, which must be absolute: the provider passes the directory
// the host started it in, never the directory of its own executable. goos is
// the operating system the path is meant for, runtime.GOOS in production; it
// decides only which absolute forms are refused, so a test can state both
// platforms' refusals on any machine.
//
// A drive-letter path is refused on every system but Windows, where it is
// the only absolute form; on Windows a path that starts with one separator,
// which names no drive, is refused too. Both are refused before the
// filesystem is consulted.
func Resolve(target Target, dir, goos string) (string, error) {
	if target.Kind != File {
		return "", refusal("the address names no database file")
	}
	path := target.Path
	windows := goos == "windows"
	switch {
	case driveAbsolute(path) && !windows:
		return "", refusal("the address names a Windows drive path, and the provider runs on " + goos)
	case windows && rootedWithoutDrive(path):
		return "", refusal("the address names a path that starts at the root of no drive; give a drive, such as C:/data/app.duckdb")
	}
	native := filepath.FromSlash(path)
	if filepath.IsAbs(native) {
		return filepath.Clean(native), nil
	}
	if !filepath.IsAbs(dir) {
		return "", refusal("the provider has no absolute directory to resolve a relative path against")
	}
	return filepath.Join(dir, native), nil
}

// rootedWithoutDrive reports whether path starts with exactly one separator.
// Two separators of either kind start a UNC path, which Windows reads as
// absolute.
func rootedWithoutDrive(path string) bool {
	separator := func(b byte) bool { return b == '/' || b == '\\' }
	if path == "" || !separator(path[0]) {
		return false
	}
	return len(path) == 1 || !separator(path[1])
}
