#!/bin/sh
# Tests update.sh against stand-ins for curl, cosign and docker, so no
# registry or Docker daemon is needed. Run with: sh deploy/update_test.sh
set -eu

here=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
A=sha256:$(printf 'a%.0s' $(seq 64))
B=sha256:$(printf 'b%.0s' $(seq 64))
IMG=ghcr.io/albertovaneckeveld/cloud

mkdir "$work/bin" "$work/deploy"
cp "$here/update.sh" "$work/deploy/"
: >"$work/deploy/.env"

# The stand-ins read their behaviour from files and log what they were asked.
cat >"$work/bin/curl" <<'STUB'
#!/bin/sh
case "$*" in
*/token*) echo '{"token":"t"}' ;;
*) printf 'HTTP/2 200\r\ndocker-content-digest: %s\r\n\r\n' "$(cat "$STUBS/digest")" ;;
esac
STUB
cat >"$work/bin/cosign" <<'STUB'
#!/bin/sh
echo "cosign $*" >>"$STUBS/calls"
[ ! -f "$STUBS/bad-signature" ]
STUB
cat >"$work/bin/docker" <<'STUB'
#!/bin/sh
echo "docker $*" >>"$STUBS/calls"
case "$*" in
inspect*) cat "$STUBS/health" ;;
*" ps -q platform") echo cid ;;
esac
STUB
chmod +x "$work/bin/"*

export STUBS="$work" PATH="$work/bin:$PATH"
run() { : >"$work/calls"; sh "$work/deploy/update.sh" >"$work/out" 2>&1; }
fail() { echo "FAIL: $*"; cat "$work/out"; exit 1; }
release() { sed -n 's/^PLATFORM_IMAGE=//p' "$work/deploy/release.env" 2>/dev/null || true; }

# A new, signed, healthy image is deployed.
echo "$A" >"$work/digest"; echo healthy >"$work/health"
run || fail "first deploy"
[ "$(release)" = "$IMG@$A" ] || fail "release.env not written"
grep -q "cosign verify $IMG@$A --certificate-identity https://github.com/AlbertovanEckeveld/cloud/.github/workflows/release.yml@refs/heads/main" "$work/calls" || fail "not verified by digest and identity"

# The same image again: no new verification, the stack is only brought up.
run || fail "unchanged run"
grep -q cosign "$work/calls" && fail "verified again although nothing changed"
grep -q "up -d" "$work/calls" || fail "stack not brought up"

# A bad signature: nothing is started, the old release stays.
echo "$B" >"$work/digest"; : >"$work/bad-signature"
run && fail "deployed an image with a bad signature"
grep -q "up -d" "$work/calls" && fail "started something after a bad signature"
[ "$(release)" = "$IMG@$A" ] || fail "release changed after a bad signature"
rm "$work/bad-signature"

# Signed but unhealthy: rolled back, and not retried on the next run.
echo unhealthy >"$work/health"
run && fail "kept an unhealthy image"
[ "$(release)" = "$IMG@$A" ] || fail "not rolled back"
[ "$(cat "$work/deploy/rejected")" = "$IMG@$B" ] || fail "unhealthy image not remembered"
[ ! -f "$work/deploy/release.env.new" ] || fail "release.env.new left behind"
run || fail "run after a rejected image"
grep -q cosign "$work/calls" && fail "retried a rejected image"

# A malformed digest from the registry is refused.
echo "sha256:../../etc" >"$work/digest"
run && fail "accepted a malformed digest"

echo "update.sh: all tests passed"
