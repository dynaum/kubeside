#!/usr/bin/env bash
# Verify the Homebrew tap serves the release that was just built.
#
# The formula lives in another repository, so GoReleaser pushes it over SSH with
# a deploy key, held in TAP_DEPLOY_KEY. Without that secret the formula is
# generated and the push is skipped, and the skip is silent: the
# release ships, `brew install` keeps handing out the previous version, and
# nothing anywhere looks broken. v1.0.1 sat on the tap for ten commits that way.
#
# The deploy key does not retire this check. Keys get rotated, revoked and
# mistyped, and every one of those failures looks exactly like the original.
# This is what turns it from silence into a red run.
#
# GoReleaser creates the release as a draft, so this runs before anything is
# public. A failure means nothing shipped, rather than something shipped wrong.
#
#   scripts/verify-tap.sh v1.2.0
#
# Reads dist/checksums.txt when GoReleaser just wrote one, and falls back to the
# release's own copy so it can be run by hand long after the fact.

set -euo pipefail
# The formula is ASCII, and a byte sequence the current locale dislikes should
# not be the reason a release gate fails.
export LC_ALL=C

TAG="${1:-}"
if [ -z "$TAG" ]; then
  echo "usage: $0 <tag>   e.g. $0 v1.2.0" >&2
  exit 2
fi
VERSION="${TAG#v}"

OWNER_REPO="dynaum/homebrew-tap"
FORMULA_PATH="Formula/kubeside.rb"
API="https://api.github.com/repos/${OWNER_REPO}/contents/${FORMULA_PATH}"

fail() {
  echo >&2
  echo "TAP IS STALE: $*" >&2
  cat >&2 <<EOF

The release is still a draft, so nothing is public yet.

Most likely TAP_DEPLOY_KEY is missing, or its public half is no longer a
deploy key on ${OWNER_REPO}. Check both:
  gh api repos/dynaum/kubeside/actions/secrets --jq '.secrets[].name'
  gh api repos/${OWNER_REPO}/keys --jq '.[] | "\(.title) read_only=\(.read_only)"'

The key must have write access and must not be password-protected. Store the
private half with:
  gh secret set TAP_DEPLOY_KEY --repo dynaum/kubeside < key

Re-run this workflow afterwards. To fix the formula by hand instead, update
${FORMULA_PATH} in ${OWNER_REPO} from this release's checksums.txt,
then re-run.
EOF
  exit 1
}

# The push can take a moment to be readable, and the raw CDN caches for minutes.
# The contents API does not, so ask it, and give it a few tries.
formula=""
for attempt in 1 2 3 4 5; do
  # The raw media type hands back the file itself, so there is no JSON or
  # base64 to unpick. raw.githubusercontent would be simpler still and caches
  # for minutes, which is exactly the window this runs in.
  if formula="$(curl -fsSL -H 'Accept: application/vnd.github.raw' "$API" 2>/dev/null)" \
     && [ -n "$formula" ]; then
    break
  fi
  echo "tap not readable yet, attempt ${attempt}/5"
  sleep 5
done
[ -n "$formula" ] || fail "could not read ${FORMULA_PATH} from ${OWNER_REPO}"

got_version="$(printf '%s\n' "$formula" | sed -n 's/^[[:space:]]*version "\([^"]*\)".*/\1/p' | head -1)"
[ "$got_version" = "$VERSION" ] || fail "formula says version ${got_version:-<none>}, this release is ${VERSION}"

# A version bump with the previous binaries behind it would install the old
# build under the new name, which is worse than not updating at all.
urls="$(printf '%s\n' "$formula" | sed -n 's/^[[:space:]]*url "\([^"]*\)".*/\1/p')"
[ -n "$urls" ] || fail "formula lists no urls"
while read -r url; do
  case "$url" in
    */download/"$TAG"/*) ;;
    *) fail "formula points at $url, which is not $TAG" ;;
  esac
done <<< "$urls"

# Checksums, so a formula cannot name this tag while carrying stale hashes.
sums_file="dist/checksums.txt"
if [ ! -f "$sums_file" ]; then
  sums_file="$(mktemp)"
  curl -fsSL "https://github.com/dynaum/kubeside/releases/download/${TAG}/checksums.txt" \
    -o "$sums_file" || fail "no dist/checksums.txt and none on the ${TAG} release"
fi

shas="$(printf '%s\n' "$formula" | sed -n 's/^[[:space:]]*sha256 "\([0-9a-f]\{64\}\)".*/\1/p')"
[ -n "$shas" ] || fail "formula lists no sha256 values"
while read -r sha; do
  grep -q "^${sha}  " "$sums_file" \
    || fail "sha256 ${sha} is in the formula but not in ${TAG}'s checksums"
done <<< "$shas"

n_urls="$(printf '%s\n' "$urls" | wc -l | tr -d ' ')"
n_shas="$(printf '%s\n' "$shas" | wc -l | tr -d ' ')"
[ "$n_urls" = "$n_shas" ] || fail "${n_urls} urls but ${n_shas} checksums"

echo "tap serves ${VERSION}: ${n_urls} platforms, every checksum matches ${TAG}"
