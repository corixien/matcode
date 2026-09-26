#!/bin/sh
# install.sh — build matcode from this checkout and put `mtc` on PATH.
#
#   ./install.sh                 # install to $PREFIX (default ~/.local/bin)
#   PREFIX=/home/mateo/bin ./install.sh
#   ./install.sh --uninstall     # remove what this script installed
#   ./install.sh --check         # report locations, change nothing
#
# Installs `mtc` (the CLI) and `matcode` (same binary, spec §13: matcode —
# `mtc` in the terminal). No sudo, no network: the binary is built from
# this source tree, so it needs a Go toolchain and nothing else.
set -eu

PREFIX="${PREFIX:-$HOME/.local/bin}"
BINDIR="$PREFIX"

self() { printf 'install.sh: %s\n' "$*" >&2; }
die()  { self "$*"; exit 1; }

src_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
action=install
case "${1:-}" in
    ""|--prefix=*) action=install ;;
    --uninstall)   action=uninstall ;;
    --check)       action=check ;;
    -h|--help)     sed -n '2,11p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) die "unknown option $1 (try --help)" ;;
esac
case "${1:-}" in --prefix=*) PREFIX="${1#--prefix=}"; BINDIR="$PREFIX";; esac

names="mtc matcode"

case "$action" in
check)
    printf 'source  %s\n' "$src_dir"
    printf 'prefix  %s\n' "$BINDIR"
    for n in $names; do
        if [ -e "$BINDIR/$n" ]; then printf 'have    %s\n' "$BINDIR/$n"; fi
    done
    case ":$PATH:" in
        *":$BINDIR:"*) printf 'PATH    %s is on PATH\n' "$BINDIR" ;;
        *)             printf 'PATH    %s NOT on PATH — add: export PATH="%s:$PATH"\n' "$BINDIR" "$BINDIR" ;;
    esac
    exit 0
    ;;
uninstall)
    for n in $names; do
        # -L too: matcode is a symlink, dangling once mtc is gone.
        if [ -e "$BINDIR/$n" ] || [ -L "$BINDIR/$n" ]; then
            rm -f "$BINDIR/$n"
            printf 'removed %s\n' "$BINDIR/$n"
        fi
    done
    exit 0
    ;;
esac

command -v go >/dev/null 2>&1 || die "go toolchain not found in PATH"
cd "$src_dir"
command -v gofmt >/dev/null 2>&1 && [ -n "$(gofmt -l cmd internal 2>/dev/null)" ] &&
    self "warning: unformatted files (gofmt -l cmd internal)"

mkdir -p "$BINDIR" || die "cannot create $BINDIR"
tmp=$(mktemp "$BINDIR/.mtc.XXXXXX")
trap 'rm -f "$tmp"' EXIT
go build -o "$tmp" ./cmd/mtc || die "go build failed"
mv -f "$tmp" "$BINDIR/mtc"
trap - EXIT
chmod 755 "$BINDIR/mtc"
# `matcode` is the same binary under the product name.
ln -sf mtc "$BINDIR/matcode"

version=$("$BINDIR/mtc" version 2>/dev/null || true)
printf 'installed  %s → %s/mtc (+ matcode symlink)\n' "${version:-mtc}" "$BINDIR"
case ":$PATH:" in
    *":$BINDIR:"*) ;;
    *) printf 'note: %s is not on PATH — add to your shell profile:\n  export PATH="%s:$PATH"\n' "$BINDIR" "$BINDIR" ;;
esac
