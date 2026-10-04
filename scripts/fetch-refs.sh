#!/usr/bin/env bash
#
# fetch-refs.sh -- offline mirror of the authoritative reference docs (PLAN.md Phase 0).
#
# Everything lands in refs/, which is gitignored scratch space. Only
# refs/INDEX.md (curated topic map) and refs/MANIFEST.md (pinned commit SHAs) are
# tracked, so a fresh clone reproduces exactly the same references.
#
# Without flags the script is idempotent: it fetches only what is missing and
# keeps every source on the SHA recorded in refs/MANIFEST.md. Clones are shallow
# (--depth 1), partial (--filter=blob:none) and sparse where useful.
#
# Compatible with bash 3.2 (macOS /bin/bash): no associative arrays, no mapfile.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
REFS="$ROOT/refs"
MANIFEST="$REFS/MANIFEST.md"
GO_LIBS_DOC="$REFS/GO_LIBS.md"
GOLIBS_DIR="$SCRIPT_DIR/golibs"
SIZE_BUDGET_MB=500
BT="$(printf '\140')"   # backtick, kept in a variable so markdown output stays readable

export GIT_TERMINAL_PROMPT=0
export GIT_LFS_SKIP_SMUDGE=1

UPDATE=0
VERIFY_ONLY=0
CLEAN=0
SKIP_GO_LIBS=0
SHOW_LIST=0
ONLY=""

die()  { printf 'error: %s\n' "$*" >&2; exit 1; }
warn() { printf 'warning: %s\n' "$*" >&2; }
log()  { printf '%s\n' "$*"; }
trim() { printf '%s' "$1" | sed -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//'; }
dir_size() { du -sh "$1" 2>/dev/null | awk '{print $1}'; }

usage() {
  cat <<'USAGE'
fetch-refs.sh -- mirror the reference docs used to implement the teams CLI into refs/

refs/ is gitignored scratch space. Only refs/INDEX.md (curated topic map) and
refs/MANIFEST.md (pinned commit SHAs) are tracked, so a fresh clone reproduces
exactly the same references.

Usage:
  scripts/fetch-refs.sh                  fetch what is missing, keep the pinned SHAs
  scripts/fetch-refs.sh --update         move every source to its branch tip (prints a SHA changelog)
  scripts/fetch-refs.sh --source NAME    restrict to one source (repeatable); NAME comes from --list
  scripts/fetch-refs.sh --list           list sources, URLs and sparse paths
  scripts/fetch-refs.sh --verify         spot checks: doc hits, refs/INDEX.md paths, size budget
  scripts/fetch-refs.sh --clean          delete source checkouts (keeps INDEX.md and MANIFEST.md)
  scripts/fetch-refs.sh --skip-go-libs   skip "go mod download" and refs/GO_LIBS.md

Sources are pinned to a commit SHA in refs/MANIFEST.md. Delete that file, or run
with --update, to move to the current branch tips.
USAGE
}

while [ $# -gt 0 ]; do
  case "$1" in
    --update)       UPDATE=1 ;;
    --verify)       VERIFY_ONLY=1 ;;
    --clean)        CLEAN=1 ;;
    --skip-go-libs) SKIP_GO_LIBS=1 ;;
    --list)         SHOW_LIST=1 ;;
    --source)       [ $# -ge 2 ] || die "--source needs a name"; ONLY="$ONLY $2"; shift ;;
    --source=*)     ONLY="$ONLY ${1#*=}" ;;
    -h|--help)      usage; exit 0 ;;
    *)              die "unknown argument: $1 (try --help)" ;;
  esac
  shift
done
ONLY="$(trim "$ONLY")"

selected() {
  [ -z "$ONLY" ] && return 0
  local want
  for want in $ONLY; do
    [ "$want" = "$1" ] && return 0
  done
  return 1
}

# Each line: <name>|<git url>|<sparse patterns separated by ; , empty means full checkout>
sources() {
  cat <<'SPEC'
graph|https://github.com/microsoftgraph/microsoft-graph-docs-contrib|api-reference/v1.0/api/channel*;api-reference/v1.0/api/chat*;api-reference/v1.0/api/chatmessage*;api-reference/v1.0/api/team*;api-reference/v1.0/api/user*;api-reference/v1.0/api/drive*;api-reference/v1.0/api/search*;api-reference/v1.0/api/teamwork*;api-reference/v1.0/resources/channel*;api-reference/v1.0/resources/chat*;api-reference/v1.0/resources/team*;api-reference/v1.0/resources/user*;api-reference/v1.0/resources/drive*;api-reference/v1.0/resources/search*;api-reference/v1.0/resources/teamwork*;concepts/teams*;concepts/search-concept-messages*;concepts/search-concept-chat-messages*;concepts/throttling*;concepts/paging*;concepts/json-batching*;concepts/permissions-reference*;concepts/query-parameters*;concepts/delta-query*;/includes/throttling-teams.md;api-reference/v1.0/includes/permissions/*;api-reference/v1.0/resources/itembody*;api-reference/v1.0/resources/identityset*;api-reference/v1.0/resources/conversationmember*;api-reference/v1.0/resources/aaduserconversationmember*;api-reference/v1.0/resources/person*;api-reference/v1.0/resources/scoredemailaddress*;concepts/people*
openapi|https://github.com/microsoftgraph/msgraph-metadata|/openapi/v1.0/openapi.yaml
entra|https://github.com/MicrosoftDocs/entra-docs|docs/identity-platform/*.md;docs/identity-platform/**/*.md
msteams|https://github.com/MicrosoftDocs/msteams-docs|msteams-platform/concepts/build-and-test/deep-link*;/msteams-platform/bots/how-to/format-your-bot-messages.md;msteams-platform/bots/how-to/conversations/*.md;/msteams-platform/includes/bots/user-mention.md;msteams-platform/task-modules-and-cards/cards/cards-format*;msteams-platform/graph-api/**/*.md
kql|https://github.com/SharePoint/sp-dev-docs|/docs/general-development/keyword-query-language-kql-syntax-reference.md
msal-go|https://github.com/AzureAD/microsoft-authentication-library-for-go|
msal-ext|https://github.com/AzureAD/microsoft-authentication-extensions-for-go|
azure-sdk|https://github.com/Azure/azure-sdk-for-go|sdk/azcore/**;sdk/azidentity/**;sdk/security/keyvault/azsecrets/**
anthropic|https://github.com/anthropics/anthropic-sdk-go|
goreleaser|https://github.com/goreleaser/goreleaser|/www/content/**
teams-mcp|https://github.com/floriscornel/teams-mcp|src/**;/package.json;/vitest.config.ts;/tsconfig.json;/.github/workflows/*
SPEC
}

WORK="$(mktemp -d "${TMPDIR:-/tmp}/fetch-refs.XXXXXX")"
cleanup() {
  case "$WORK" in
    */fetch-refs.*) [ -d "$WORK" ] && rm -rf "$WORK" ;;
  esac
  return 0
}
trap cleanup EXIT INT TERM

# -------------------------------------------------------------------- git ops
assert_deletable() {
  case "$1" in
    "$REFS"/*) ;;
    *) die "refusing to delete $1: outside $REFS" ;;
  esac
  [ "$1" != "$REFS" ] || die "refusing to delete $REFS itself"
}

remove_dir() {
  [ -e "$1" ] || return 0
  assert_deletable "$1"
  rm -rf "$1"
}

is_sparse() {
  [ "$(git -C "$1" config --bool core.sparseCheckout 2>/dev/null || printf false)" = "true" ]
}

drop_row_from() {  # $1 = manifest/rows file, $2 = source name
  local f="$1" n="$2" tmp
  [ -f "$f" ] || return 0
  tmp="$f.tmp.$$"
  if grep -v "^| $n |" "$f" > "$tmp"; then mv "$tmp" "$f"; else mv "$tmp" "$f"; fi
  return 0
}

apply_sparse() {
  local dir="$1" pats="$2" args
  [ -n "$pats" ] || return 0
  IFS=';' read -r -a args <<< "$pats"
  git -C "$dir" sparse-checkout set --no-cone "${args[@]}"
  git -C "$dir" checkout --quiet
}

clone_source() {
  local url="$1" pats="$2" dir="$3"
  if [ -z "$pats" ]; then
    git clone --quiet --depth 1 --filter=blob:none "$url" "$dir"
  else
    git clone --quiet --depth 1 --filter=blob:none --sparse "$url" "$dir"
    apply_sparse "$dir" "$pats"
  fi
}

checkout_pinned() {
  local dir="$1" sha="$2"
  if ! git -C "$dir" cat-file -e "${sha}^{commit}" 2>/dev/null; then
    git -C "$dir" fetch --quiet --depth 1 --filter=blob:none origin "$sha" || return 1
  fi
  git -C "$dir" -c advice.detachedHead=false checkout --quiet --detach "$sha" || return 1
  if is_sparse "$dir"; then
    git -C "$dir" sparse-checkout reapply >/dev/null 2>&1 || true
  fi
  return 0
}

update_source() {
  local dir="$1" before after
  before="$(git -C "$dir" rev-parse HEAD)"
  git -C "$dir" fetch --quiet --depth 1 --filter=blob:none origin HEAD
  after="$(git -C "$dir" rev-parse FETCH_HEAD)"
  if [ "$before" != "$after" ]; then
    git -C "$dir" -c advice.detachedHead=false checkout --quiet --detach "$after"
    if is_sparse "$dir"; then
      git -C "$dir" sparse-checkout reapply >/dev/null 2>&1 || true
    fi
  fi
  printf '%s %s' "$before" "$after"
}

manifest_sha() {
  local name="$1" blank n u s mode
  [ -f "$MANIFEST" ] || return 0
  while IFS='|' read -r blank n u s mode; do
    if [ "$(trim "$n")" = "$name" ]; then
      printf '%s' "$(trim "$s")" | tr -cd '0-9a-f'
      return 0
    fi
  done < "$MANIFEST"
}

# --------------------------------------------------------------------- --list
show_list() {
  local name url pats p
  log "Sources mirrored into refs/ (shallow + partial + sparse checkouts):"
  while IFS='|' read -r name url pats; do
    [ -n "$name" ] || continue
    log ""
    log "$name  $url"
    if [ -z "$pats" ]; then
      log "  (full checkout)"
    else
      # "|| [ -n "$p"" matters: tr leaves no trailing newline, so a plain
      # read would silently drop the last pattern of every source.
      printf '%s' "$pats" | tr ';' '\n' | while IFS= read -r p || [ -n "$p" ]; do
        if [ -n "$p" ]; then log "  $p"; fi
      done
    fi
  done <<< "$(sources)"
}

# ------------------------------------------------------------------- --verify
verify() {
  local fails=0 size_mb hits

  vfile() {
    if [ -f "$1" ]; then
      log "ok      $2"
    else
      log "FAIL    $2 -- $1 is missing"
      fails=$((fails + 1))
    fi
  }

  # ripgrep is much faster over the ~115 MB mirror, but it is not installed
  # everywhere: a bare CI runner (or a container) has no `rg`, and the first CI
  # run of this script failed every spot check with "rg: command not found".
  # `grep -rl` is the portable fallback. Both branches swallow the "no match"
  # exit status, so `set -o pipefail` cannot abort the run before the check
  # reports it.
  vsearch() {
    local pattern="$1" path="$2"
    if command -v rg >/dev/null 2>&1; then
      rg -l --no-messages -- "$pattern" "$path" || true
    else
      grep -rl --exclude-dir=.git -e "$pattern" "$path" 2>/dev/null || true
    fi
  }

  vcheck() {
    local desc="$1" pattern="$2" path="$3"
    if [ ! -e "$path" ]; then
      log "FAIL    $desc -- $path does not exist (run scripts/fetch-refs.sh first)"
      fails=$((fails + 1))
      return 0
    fi
    hits="$(vsearch "$pattern" "$path" | wc -l | tr -d ' ')"
    if [ "$hits" -gt 0 ]; then
      log "ok      $desc -- $hits file(s)"
    else
      log "FAIL    $desc -- no match for /$pattern/ under $path"
      fails=$((fails + 1))
    fi
  }

  log "Phase 0 spot checks:"
  vfile "$REFS/INDEX.md" "refs/INDEX.md (curated topic map) exists"
  vfile "$MANIFEST" "refs/MANIFEST.md (pinned SHAs) exists"
  vcheck "Graph reaction API (setReaction)" "setReaction" "$REFS/graph"
  vcheck "Entra error code AADSTS65001" "AADSTS65001" "$REFS/entra"
  vcheck "Graph hostedContents" "hostedContents" "$REFS/graph/api-reference"
  vcheck "Teams deep link format (l/message)" "l/message" "$REFS/msteams"
  vcheck "Graph OpenAPI spec (chatMessage)" "chatMessage" "$REFS/openapi"
  vcheck "KQL syntax reference (sent)" "sent" "$REFS/kql"
  vcheck "teams-mcp reference (processMentions)" "processMentions" "$REFS/teams-mcp"
  vcheck "Graph chatMessage POST (hostedContents)" "temporaryId" "$REFS/graph/api-reference/v1.0/api/chatmessage-post.md"
  vfile "$REFS/graph/includes/throttling-teams.md" "Teams throttling include (referenced by throttling-limits.md) exists"
  vfile "$REFS/graph/concepts/search-concept-chat-messages.md" "Teams message-search semantics exist"
  vfile "$REFS/teams-mcp/vitest.config.ts" "teams-mcp coverage thresholds exist"
  vcheck "KQL scope terms for Teams search (IsMentioned)" "IsMentioned" "$REFS/graph/concepts/search-concept-chat-messages.md"
  vcheck "People API (relevance-ranked person search)" "relevance" "$REFS/graph/api-reference/v1.0/api/user-list-people.md"
  vcheck "GoReleaser Homebrew casks" "homebrew_casks" "$REFS/goreleaser/www/content/customization/publish"

  # Regression guard: a path cited in the curated index must exist, so the index
  # cannot rot away from the mirror it describes.
  local idx_missing="" p
  while IFS= read -r p; do
    [ -n "$p" ] || continue
    case "$p" in
      *'*'*) continue ;;              # a glob: nothing to assert
      refs/GO_LIBS.md) continue ;;    # generated, may be absent with --skip-go-libs
    esac
    if [ ! -e "$p" ] && [ ! -d "${p%/}" ]; then idx_missing="$idx_missing $p"; fi
  done < <(grep -o 'refs/[A-Za-z0-9_][A-Za-z0-9_./*-]*' "$REFS/INDEX.md" 2>/dev/null | sed -e 's/[.,]$//' | sort -u)
  if [ -z "$(trim "$idx_missing")" ]; then
    log "ok      every refs/ path cited in refs/INDEX.md exists"
  else
    log "FAIL    refs/INDEX.md cites path(s) that do not exist:$idx_missing"
    fails=$((fails + 1))
  fi

  if [ ! -d "$REFS" ]; then
    log "FAIL    refs/ does not exist"
    fails=$((fails + 1))
  else
    size_mb="$(du -sm "$REFS" | awk '{print $1}')"
    if [ "$size_mb" -le "$SIZE_BUDGET_MB" ]; then
      log "ok      refs/ is ${size_mb} MB (budget ${SIZE_BUDGET_MB} MB)"
    else
      log "FAIL    refs/ is ${size_mb} MB, over the ${SIZE_BUDGET_MB} MB budget"
      fails=$((fails + 1))
    fi
  fi

  if [ "$fails" -gt 0 ]; then
    log ""
    log "$fails check(s) failed"
    return 1
  fi
  log ""
  log "all checks passed"
  return 0
}

# -------------------------------------------------------------- go module cache
generate_go_libs() {
  [ "$SKIP_GO_LIBS" = 1 ] && { log "--skip-go-libs: not writing refs/GO_LIBS.md"; return 0; }
  [ -f "$GOLIBS_DIR/go.mod" ] || { warn "$GOLIBS_DIR/go.mod not found; skipping refs/GO_LIBS.md"; return 0; }
  command -v go >/dev/null 2>&1 || { warn "go not found on PATH; skipping refs/GO_LIBS.md"; return 0; }

  log ""
  log "go mod download (source for the libraries PLAN.md picked)"
  ( cd "$GOLIBS_DIR" && go mod download ) || warn "go mod download reported an error; continuing"

  local mods="$WORK/go-modules.txt"
  if ! ( cd "$GOLIBS_DIR" && go list -m -f '{{.Path}}|{{.Version}}|{{.Dir}}' all ) > "$mods" 2>"$WORK/go-list.err"; then
    warn "go list -m all failed; skipping refs/GO_LIBS.md"
    sed -e 's/^/  /' "$WORK/go-list.err" >&2 || true
    return 0
  fi

  {
    log "# Go library sources in the module cache"
    log ""
    log "Generated by ${BT}scripts/fetch-refs.sh${BT} from ${BT}scripts/golibs/go.mod${BT}. These are the"
    log "third-party libraries PLAN.md selects for the CLI, downloaded so their source can be"
    log "grepped offline next to ${BT}refs/${BT}:"
    log ""
    log "    rg \"WithHTTPClient\" \"\$(go env GOMODCACHE)\""
    log "    (cd scripts/golibs && go doc github.com/spf13/cobra.Command)"
    log ""
    log "The paths below are machine specific, which is why this file is not tracked in git."
    log ""
    log "If the global module cache is not writable (a sandboxed agent or CI runner), move the Go"
    log "caches into the checkout before running the fetch:"
    log ""
    log "    GOPATH=\"\$PWD/.cache/gopath\" GOCACHE=\"\$PWD/.cache/go-build\" scripts/fetch-refs.sh"
    log ""
    log "| Module | Version | Path in module cache |"
    log "|---|---|---|"
    awk -F'|' -v bt="$BT" '$1 != "" && $2 != "" && $3 != "" { printf "| %s%s%s | %s | %s%s%s |\n", bt, $1, bt, $2, bt, $3, bt }' "$mods"
    log ""
    log "Modules that are in the build list but whose source is not extracted yet:"
    log ""
    awk -F'|' -v bt="$BT" '$1 != "" && $2 != "" && $3 == "" { printf "- %s%s%s %s\n", bt, $1, bt, $2 }' "$mods" | sort
  } > "$GO_LIBS_DOC"

  log "wrote refs/GO_LIBS.md ($(grep -c '^|' "$GO_LIBS_DOC" | tr -d ' ') module rows)"
}

# ----------------------------------------------------------------- entry point
if [ "$SHOW_LIST" = 1 ]; then
  show_list
  exit 0
fi

mkdir -p "$REFS"

if [ "$VERIFY_ONLY" = 1 ]; then
  if verify; then exit 0; else exit 1; fi
fi

if [ "$CLEAN" = 1 ]; then
  while IFS='|' read -r name url pats; do
    [ -n "$name" ] || continue
    if selected "$name" && [ -d "$REFS/$name" ]; then
      log "remove  refs/$name"
      remove_dir "$REFS/$name"
      drop_row_from "$MANIFEST" "$name"
    fi
  done <<< "$(sources)"
  log "kept    refs/INDEX.md and refs/MANIFEST.md"
  exit 0
fi

ROWS="$WORK/rows.txt"
CHANGELOG="$WORK/changelog.txt"
FAILED=""
: > "$ROWS"
: > "$CHANGELOG"

# A --source run only rewrites the rows of the selected sources, so seed the rest
# from the existing manifest and replace rows one by one.
if [ -n "$ONLY" ] && [ -f "$MANIFEST" ]; then
  grep '^|' "$MANIFEST" | grep -v '^|---' | grep -v '^| Source ' > "$ROWS" || true
fi

log "Reference mirror in $REFS"
log ""

while IFS='|' read -r name url pats; do
  [ -n "$name" ] || continue
  selected "$name" || continue
  dir="$REFS/$name"
  pinned="$(manifest_sha "$name")"

  if [ -d "$dir/.git" ]; then
    current="$(git -C "$dir" rev-parse HEAD)"
    if [ "$UPDATE" = 1 ]; then
      read -r before after <<< "$(update_source "$dir")"
      if [ "$before" = "$after" ]; then
        log "  uptodate $name ${after:0:8}"
      else
        log "  updated  $name ${before:0:8} -> ${after:0:8}"
      fi
      printf '%s %s %s\n' "$name" "${before:0:8}" "${after:0:8}" >> "$CHANGELOG"
    elif [ -n "$pinned" ] && [ "$current" != "$pinned" ]; then
      if checkout_pinned "$dir" "$pinned"; then
        log "  pinned   $name -> ${pinned:0:8} (was ${current:0:8})"
      else
        warn "$name: cannot reach pinned ${pinned:0:8}; staying on ${current:0:8} (re-run with --update to re-pin)"
        FAILED="$FAILED $name"
      fi
    else
      log "  present  $name ${current:0:8}"
    fi

    if [ "$UPDATE" != 1 ] && [ -n "$pats" ] && is_sparse "$dir"; then
      current_pats="$(git -C "$dir" sparse-checkout list 2>/dev/null | tr '\n' ';')"
      if [ "$current_pats" != "$pats;" ]; then
        log "  sparse   $name (sparse paths changed)"
        apply_sparse "$dir" "$pats"
      fi
    fi
  else
    log "  clone    $name <- $url"
    remove_dir "$dir"
    clone_source "$url" "$pats" "$dir"
    if [ -n "$pinned" ]; then
      if checkout_pinned "$dir" "$pinned"; then
        log "  pinned   $name -> ${pinned:0:8}"
      else
        warn "$name: cannot fetch pinned ${pinned:0:8}; using the branch tip instead"
        FAILED="$FAILED $name"
      fi
    fi
  fi

  sha="$(git -C "$dir" rev-parse HEAD)"
  if [ -z "$pats" ]; then mode="full"; else mode="sparse"; fi
  drop_row_from "$ROWS" "$name"
  printf '| %s | %s | %s%s%s | %s |\n' "$name" "$url" "$BT" "$sha" "$BT" "$mode" >> "$ROWS"
done <<< "$(sources)"

{
  log "<!-- Generated by scripts/fetch-refs.sh. Do not edit by hand. -->"
  log ""
  log "# Reference mirror manifest"
  log ""
  log "Pinned commits for the offline reference mirror in ${BT}refs/${BT} (see PLAN.md Phase 0)."
  log "Run ${BT}scripts/fetch-refs.sh${BT} to reproduce this exact state, or"
  log "${BT}scripts/fetch-refs.sh --update${BT} to move to the current branch tips."
  log ""
  log "Checkout sizes are deliberately not recorded: ${BT}du${BT} output drifts between runs, which"
  log "would make this tracked file change on every fetch. ${BT}--verify${BT} reports the total size."
  log ""
  log "| Source | Repository | Commit | Checkout |"
  log "|---|---|---|---|"
  if [ -s "$ROWS" ]; then
    cat "$ROWS"
  else
    log "| _(nothing fetched)_ | | | |"
  fi
  log ""
  log "## Sparse paths"
  log ""
  log "Each checkout is shallow (${BT}--depth 1${BT}), partial (${BT}--filter=blob:none${BT}) and limited to"
  log "these gitignore-style patterns via ${BT}git sparse-checkout set --no-cone${BT}:"
  while IFS='|' read -r name url pats; do
    [ -n "$name" ] || continue
    log ""
    if [ -z "$pats" ]; then
      log "- **$name** -- full checkout"
    else
      log "- **$name**"
      log ""
      printf '  %s\n' "${BT}${BT}${BT}"
      # See show_list(): without "|| [ -n "$p"" the final pattern is dropped.
      printf '%s' "$pats" | tr ';' '\n' | while IFS= read -r p || [ -n "$p" ]; do
        if [ -n "$p" ]; then printf '  %s\n' "$p"; fi
      done
      printf '  %s\n' "${BT}${BT}${BT}"
    fi
  done <<< "$(sources)"
} > "$MANIFEST"

log ""
log "wrote refs/MANIFEST.md ($(wc -l < "$ROWS" | tr -d ' ') source(s))"

if [ -s "$CHANGELOG" ]; then
  log ""
  log "SHA changelog (--update):"
  while read -r n b a; do
    if [ "$b" = "$a" ]; then
      log "  $n $b (unchanged)"
    else
      log "  $n $b -> $a"
    fi
  done < "$CHANGELOG"
fi

generate_go_libs

log ""
log "refs/ total size: $(dir_size "$REFS") (budget ${SIZE_BUDGET_MB} MB)"
if [ -n "$(trim "$FAILED")" ]; then
  warn "sources that could not be moved to their pinned commit:$FAILED"
fi
log "done"
