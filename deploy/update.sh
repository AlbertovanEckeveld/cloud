#!/bin/sh
# Deploys the newest signed platform image from GHCR (ADR 0020).
#
# Run every few minutes by cloud-update.timer, or by hand to apply a change
# in this directory. It always brings the stack up; a new image is started
# only after cosign has verified that release.yml on main built it, and it
# is rolled back if it does not become healthy.
set -eu

REPO=albertovaneckeveld/cloud
TAG=main
IMAGE=ghcr.io/$REPO
IDENTITY=https://github.com/AlbertovanEckeveld/cloud/.github/workflows/release.yml@refs/heads/main
ISSUER=https://token.actions.githubusercontent.com
HEALTH_TIMEOUT=180

cd "$(dirname "$0")"

log() { echo "update: $*"; }
die() { echo "update: $*" >&2; exit 1; }
compose() { release=$1; shift; docker compose --env-file .env --env-file "$release" "$@"; }

# 1. Where does the tag point now? This answer is untrusted until step 2.
token=$(curl -fsS --max-time 30 "https://ghcr.io/token?scope=repository:$REPO:pull" |
	sed -n 's/.*"token":"\([^"]*\)".*/\1/p')
[ -n "$token" ] || die "no registry token from ghcr.io"
digest=$(curl -fsS --max-time 30 -o /dev/null -D - \
	-H "Authorization: Bearer $token" \
	-H "Accept: application/vnd.oci.image.index.v1+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.docker.distribution.manifest.v2+json" \
	"https://ghcr.io/v2/$REPO/manifests/$TAG" | tr -d '\r' | sed -n 's/^docker-content-digest: *//Ip')
printf '%s' "$digest" | grep -Eq '^sha256:[0-9a-f]{64}$' || die "no valid digest for $IMAGE:$TAG"
new=$IMAGE@$digest

current=$(sed -n 's/^PLATFORM_IMAGE=//p' release.env 2>/dev/null || true)
if [ "$new" = "$current" ] || [ "$new" = "$(cat rejected 2>/dev/null || true)" ]; then
	[ -n "$current" ] || die "the only image ($new) was rejected earlier; see the log"
	compose release.env up -d --remove-orphans --quiet-pull
	exit 0
fi

# 2. Only an image signed by our release workflow on main gets further.
log "verifying $new"
cosign verify "$new" --certificate-identity "$IDENTITY" --certificate-oidc-issuer "$ISSUER" >/dev/null ||
	die "signature check failed for $new; not deploying"

# 3. Start it and wait for its health check.
printf 'PLATFORM_IMAGE=%s\n' "$new" >release.env.new
compose release.env.new pull --quiet platform
compose release.env.new up -d --remove-orphans --quiet-pull
cid=$(compose release.env.new ps -q platform)
waited=0
while [ "$waited" -lt "$HEALTH_TIMEOUT" ]; do
	case $(docker inspect -f '{{.State.Health.Status}}' "$cid" 2>/dev/null || echo gone) in
	healthy)
		mv release.env.new release.env
		log "deployed $new"
		exit 0
		;;
	unhealthy | gone) break ;;
	esac
	sleep 3
	waited=$((waited + 3))
done

# 4. Not healthy: remember this image so the timer does not retry it, and
# go back to the previous one. Migrations only run forward, so the previous
# version must cope with the new schema (ADR 0020).
echo "$new" >rejected
compose release.env.new logs --no-color --tail 50 platform >&2 || true
rm -f release.env.new
if [ -n "$current" ]; then
	compose release.env up -d --remove-orphans --quiet-pull
	die "$new did not become healthy; rolled back to $current"
fi
die "$new did not become healthy and there is no previous version"
