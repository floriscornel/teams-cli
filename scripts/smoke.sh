#!/usr/bin/env bash
# scripts/smoke.sh — the local half of the CI "smoke" job (PLAN.md "CI gates").
#
# It runs a built binary the way a user would, in an isolated config/state/cache
# directory, and asserts the release contract: a version prints, an unauthenticated
# `auth status` exits 3, and the binary carries no test-only endpoint override.
#
#   scripts/smoke.sh bin/teams        # a locally built binary
#   scripts/smoke.sh /usr/bin/teams   # an installed one
#   scripts/smoke.sh dist/…/teams --expect-version SNAPSHOT
#
# --expect-version asserts that `teams version` carries that substring. The CI
# smoke job passes the marker of the build it installed, which is what catches a
# build whose ldflags never reached the version variables (v1.0.0 shipped
# `teams dev`): a locally built binary may legitimately report a git-describe
# value, so the assertion is opt-in rather than a default.
#
# It needs no Go, no Node and no network.

set -euo pipefail

binary="${1:-bin/teams}"
expect_version=""
if [ "${2:-}" = "--expect-version" ]; then
  expect_version="${3:-}"
  [ -n "$expect_version" ] || { echo "smoke: --expect-version needs a value" >&2; exit 2; }
fi
if [ ! -x "$binary" ]; then
  echo "smoke: $binary is not an executable file" >&2
  exit 1
fi
binary=$(cd "$(dirname "$binary")" && pwd)/$(basename "$binary")

workdir=$(mktemp -d)
cleanup() { rm -rf "$workdir"; }
trap cleanup EXIT

export TEAMS_CONFIG="$workdir/config.toml"
export TEAMS_STATE_DIR="$workdir/state"
export TEAMS_CACHE_DIR="$workdir/cache"
export NO_COLOR=1
export TERM=dumb
export CI=true
# Nothing here may reach the network, and no command may touch the OS keychain.
export TEAMS_NO_KEYCHAIN=1
export TEAMS_NO_UPDATE_CHECK=1
unset TEAMS_ACCESS_TOKEN || true

fail() { echo "smoke: FAIL: $*" >&2; exit 1; }
pass() { echo "smoke: ok: $*"; }

# 1. The binary runs and reports a version.
version_output=$("$binary" version)
case "$version_output" in
  teams\ *) pass "version: $version_output" ;;
  *) fail "unexpected version output: $version_output" ;;
esac
if [ -n "$expect_version" ]; then
  case "$version_output" in
    *"$expect_version"*) pass "version carries $expect_version" ;;
    *) fail "version output does not carry '$expect_version': $version_output (did the ldflags reach internal/cli.Version?)" ;;
  esac
fi

# 2. Help lists the user-facing commands.
help_output=$("$binary" --help)
for command in auth cache config doctor profile whoami team channel chat thread search mentions user unread file alias \
  post reply edit delete react api; do
  case "$help_output" in
    *"$command"*) ;;
    *) fail "--help does not list $command" ;;
  esac
done
pass "help lists the command surface"

# 3. `auth status` on a fresh profile is an auth failure: exit 3.
set +e
status_output=$("$binary" auth status 2>&1)
status_code=$?
set -e
[ "$status_code" -eq 3 ] || fail "auth status exited $status_code, want 3"
case "$status_output" in
  *"not signed in"*) pass "auth status exits 3 on a fresh profile" ;;
  *) fail "auth status did not explain the missing account: $status_output" ;;
esac

# 4. Usage errors exit 2, and a read command without an account is an auth
#    failure (exit 3) rather than a crash or a prompt.
set +e
"$binary" no-such-command >/dev/null 2>&1
usage_code=$?
set -e
[ "$usage_code" -eq 2 ] || fail "an unknown command exited $usage_code, want 2"
pass "usage errors exit 2"

set +e
"$binary" team list >/dev/null 2>&1
read_code=$?
set -e
[ "$read_code" -eq 3 ] || fail "team list without an account exited $read_code, want 3"
pass "read commands require an account (exit 3)"

# 4a. A write command without an account is an auth failure too, and the
#     read-only switch refuses it before anything else (exit 2).
set +e
"$binary" post Engineering/General hi >/dev/null 2>&1
write_code=$?
set -e
[ "$write_code" -eq 3 ] || fail "post without an account exited $write_code, want 3"

set +e
readonly_output=$("$binary" --read-only post Engineering/General hi 2>&1)
readonly_code=$?
set -e
case "$readonly_code:$readonly_output" in
  2:*"read-only"*) pass "write commands require an account, and read-only refuses them" ;;
  *) fail "a read-only write exited $readonly_code: $readonly_output" ;;
esac

# 4b. A reference that cannot identify a message is a usage error with a hint.
set +e
ref_output=$("$binary" thread read not-a-message 2>&1)
ref_code=$?
set -e
case "$ref_code:$ref_output" in
  2:*"hint:"*) pass "an unusable reference exits 2 with a hint" ;;
  *) fail "an unusable reference exited $ref_code: $ref_output" ;;
esac

# 5. Config round trip, written 0600.
"$binary" config set profiles.bot.tenant contoso.example >/dev/null
got=$("$binary" config get profiles.bot.tenant)
[ "$got" = "contoso.example" ] || fail "config get returned '$got'"
if [ -e "$TEAMS_CONFIG" ]; then
  mode=$(stat -c '%a' "$TEAMS_CONFIG" 2>/dev/null || stat -f '%Lp' "$TEAMS_CONFIG")
  [ "$mode" = "600" ] || fail "the config file is $mode, want 600"
fi
pass "config round trip"

# 6. The rebuildable cache is inspectable offline, and clearing it never removes
#    the token material (there is none here, so check the config survives).
mkdir -p "$TEAMS_STATE_DIR/me" "$TEAMS_CACHE_DIR/me"
echo ciphertext >"$TEAMS_STATE_DIR/me/token.bin"
"$binary" cache info >/dev/null
"$binary" cache clear --all --yes >/dev/null
[ -e "$TEAMS_STATE_DIR/me/token.bin" ] || fail "cache clear --all deleted the token cache"
[ -e "$TEAMS_CONFIG" ] || fail "cache clear --all deleted the config file"
pass "cache info and cache clear behave"

# 7. No test-only endpoint override may ship. The markers match the ones in
#    .github/workflows/ci.yml; update both lists together.
banned="endpoint-override fakegraph-base-url fakeidp-base-url TEAMS_TEST_GRAPH_URL TEAMS_TEST_AUTHORITY"
for token in $banned; do
  if grep -a -q -e "$token" "$binary"; then
    fail "the binary contains the test-only marker '$token'"
  fi
  case "$help_output" in
    *"$token"*) fail "the binary exposes the test-only flag '$token'" ;;
  esac
done
pass "no test-only endpoint override in the binary"

echo "smoke: all checks passed for $binary"
