#!/usr/bin/env bash
# Fetch the upstream OKF example bundles from Google's knowledge-catalog into
# examples/upstream/ for testing waqwaq against real, third-party OKF data.
#
# crypto_bitcoin and ga4 are already vendored under examples/ (Apache 2.0). This
# also fetches stackoverflow, which is NOT vendored because its content derives
# from the Stack Exchange dump under CC-BY-SA 4.0; fetching it locally for testing
# is fine, redistributing it in this repo is not. examples/upstream/ is gitignored.
#
#   ./hack/fetch-upstream-bundles.sh
#   ./waqwaq validate examples/upstream/stackoverflow
#   ./waqwaq serve   examples/upstream/stackoverflow
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
dest="$root/examples/upstream"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

git clone --depth 1 --filter=blob:none --sparse \
  https://github.com/GoogleCloudPlatform/knowledge-catalog.git "$tmp/kc"
git -C "$tmp/kc" sparse-checkout set --skip-checks \
  okf/bundles/crypto_bitcoin okf/bundles/ga4 okf/bundles/stackoverflow

for b in crypto_bitcoin ga4 stackoverflow; do
  src="$tmp/kc/okf/bundles/$b"
  out="$dest/$b"
  rm -rf "$out"; mkdir -p "$out/.waqwaq"
  # Copy the markdown, drop viz.html (CDN viewer, not needed by a Go server).
  (cd "$src" && find . -name '*.md' -exec sh -c 'mkdir -p "$2/$(dirname "$1")"; cp "$1" "$2/$1"' _ {} "$out" \;)
  printf '{"title":"%s (OKF, upstream)","mcp_description":"upstream Google OKF bundle: %s"}\n' "$b" "$b" \
    > "$out/.waqwaq/config.json"
  echo "fetched $b -> $out"
done

echo
echo "Validate them:"
for b in crypto_bitcoin ga4 stackoverflow; do
  echo "  waqwaq validate examples/upstream/$b"
done
