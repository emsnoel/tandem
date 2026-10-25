#!/usr/bin/env bash
# Print the fingerprint of tandem's build inputs. The algorithm lives in
# internal/sourcehash, shared by install.sh and tests/test-staleness.sh.
#
#   bin/lib/source-hash.sh [ROOT]     ROOT defaults to this repository
set -euo pipefail
here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
src=$(cd "$here/../.." && pwd)
root="${1:-$src}"
# The helper is built from this checkout; ROOT may be another tree to hash.
cd "$src" && exec go run ./cmd/tandem-source-hash "$root"
