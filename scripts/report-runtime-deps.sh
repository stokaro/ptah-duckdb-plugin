#!/usr/bin/env bash
# Print what the provider executable needs from the system it runs on: the
# shared libraries it loads and, on Linux, the newest glibc and libstdc++
# symbol versions it binds. The DuckDB library is linked statically; the C
# and C++ runtimes are not. The release job prints this for every archive so
# the README's minimum versions are measured, not assumed.
#
#   scripts/report-runtime-deps.sh <executable>
set -euo pipefail

binary=${1:?usage: report-runtime-deps.sh <executable>}

case "$(uname -s)" in
Linux)
	echo "### $(uname -m) Linux: shared libraries"
	readelf -d "$binary" | awk '/NEEDED/ { print "- " $NF }'
	echo "### newest symbol versions"
	for family in GLIBC GLIBCXX CXXABI; do
		newest=$(objdump -T "$binary" | grep -oE "\b${family}_[0-9.]+" | sort -uV | tail -1 || true)
		echo "- ${family}: ${newest:-none}"
	done
	;;
Darwin)
	echo "### $(uname -m) macOS: shared libraries"
	otool -L "$binary" | tail -n +2 | sed 's/^[[:space:]]*/- /'
	echo "### minimum macOS version"
	otool -l "$binary" | awk '/LC_BUILD_VERSION/ { found = 1 } found && /minos/ { print "- " $2; exit }'
	;;
MINGW* | MSYS* | CYGWIN*)
	echo "### Windows: imported DLLs"
	objdump -p "$binary" | awk '/DLL Name/ { print "- " $3 }' | sort -u
	;;
*)
	echo "unknown system $(uname -s)" >&2
	exit 1
	;;
esac
