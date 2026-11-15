#!/usr/bin/env bash
# Collect the licence texts the distributed binaries must carry.
#
# Everything the app links or embeds ships in researchos.exe, so its licence
# travels with it (AGENTS.md ships no CI; run this by hand after changing
# go.mod, adding an embedded asset, or before building an installer):
#
#     scripts/collect-licenses.sh
#
# It writes licenses/<slug>__<file>.txt for every licence, notice and
# copyright file of every module the binaries link, of the Go runtime, and of
# the fonts and icons the source embeds, plus licenses/MANIFEST.tsv, the
# module, version and licence of each dependency.
#
# Nothing in licenses/ changes until the whole plan is checked: the map below
# must match what ./cmd/... links, module for module, and every mapped module
# must actually hold a licence file. A run that fails therefore leaves the
# previous texts in place.
set -euo pipefail
cd "$(dirname "$0")/.."

# Module path = slug, the name its texts get in licenses/.
map=(
  "github.com/BurntSushi/toml=toml"
  "github.com/dustin/go-humanize=humanize"
  "github.com/ebitengine/purego=purego"
  "github.com/egoist/mygo=mygo"
  "github.com/go-text/typesetting=typesetting"
  "github.com/ledongthuc/pdf=pdf"
  "github.com/mattn/go-isatty=isatty"
  "github.com/ncruces/go-strftime=go-strftime"
  "github.com/remyoudompheng/bigfft=bigfft"
  "golang.org/x/image=x-image"
  "golang.org/x/net=x-net"
  "golang.org/x/sys=x-sys"
  "golang.org/x/text=x-text"
  "gopkg.in/yaml.v3=yaml.v3"
  "modernc.org/libc=libc"
  "modernc.org/mathutil=mathutil"
  "modernc.org/memory=memory"
  "modernc.org/sqlite=sqlite"
)

# The plan: one "module|version|slug|file..." line per module, built before
# anything is written.
plan=()
linked=$(go list -deps -f '{{if .Module}}{{.Module.Path}}{{end}}' ./cmd/... |
  grep -v '^researchos$' | sort -u)
for entry in "${map[@]}"; do
  mod=${entry%%=*}
  slug=${entry##*=}
  grep -qx "$mod" <<<"$linked" || {
    echo "mapped but not linked any more: $mod (drop it from the map)" >&2
    exit 1
  }
  dir=$(go list -m -f '{{.Dir}}' "$mod")
  ver=$(go list -m -f '{{.Version}}' "$mod")
  files=()
  for f in "$dir"/LICENSE* "$dir"/LICENCE* "$dir"/COPYING* "$dir"/NOTICE*; do
    [ -f "$f" ] || continue
    base=$(basename "$f")
    files+=("$f|${base%.*}") # LICENSE-3RD-PARTY.md and NOTICE.txt get one .txt
  done
  [ ${#files[@]} -gt 0 ] || {
    echo "no licence file in $dir ($mod)" >&2
    exit 1
  }
  plan+=("$mod|$ver|$slug|$(IFS=,; echo "${files[*]}")")
done
for mod in $linked; do
  grep -q "^${mod}=" <(printf '%s\n' "${map[@]}") || {
    echo "linked but unmapped: $mod (add it to the map and re-run)" >&2
    exit 1
  }
done

# The embedded assets: fonts beside the formula renderer, and the Lucide
# licence, which is fetched because the icon paths are carried in our source.
assets=(
  "internal/ui/formula/fonts/LICENCE|licenses/fonts__BaKoMa-CM__LICENCE.txt"
  "internal/ui/formula/fonts/OFL.txt|licenses/fonts__STIX-Two__OFL.txt"
  "$(go env GOROOT)/LICENSE|licenses/go-runtime__LICENSE.txt"
)
for pair in "${assets[@]}"; do
  [ -f "${pair%%|*}" ] || {
    echo "missing ${pair%%|*}" >&2
    exit 1
  }
done
lucide=licenses/icons__Lucide__LICENSE.txt.part
mkdir -p licenses
# A path relative to this directory, because curl is the system binary and
# does not see a shell's /tmp.
curl -fsSL -o "$lucide" https://raw.githubusercontent.com/lucide-icons/lucide/main/LICENSE || {
  echo "could not fetch the Lucide licence" >&2
  exit 1
}
[ -s "$lucide" ] || {
  echo "the fetched Lucide licence is empty" >&2
  rm -f "$lucide"
  exit 1
}

# Checked: write.
mkdir -p licenses
rm -f licenses/*__*.txt licenses/MANIFEST.tsv
manifest=licenses/MANIFEST.tsv
printf 'module\tversion\tlicence files\n' >$manifest
for line in "${plan[@]}"; do
  mod=${line%%|*}
  rest=${line#*|}
  ver=${rest%%|*}
  rest=${rest#*|}
  slug=${rest%%|*}
  IFS=, read -ra files <<<"${rest#*|}"
  for pair in "${files[@]}"; do
    src=${pair%%|*}
    out="licenses/${slug}__${pair##*|}.txt"
    cp "$src" "$out"
    printf '%s\t%s\t%s\n' "$mod" "$ver" "$out" >>$manifest
  done
done
for pair in "${assets[@]}"; do
  cp "${pair%%|*}" "${pair##*|}"
done
printf 'go runtime and stdlib\t%s\tlicenses/go-runtime__LICENSE.txt\n' \
  "$(go env GOVERSION)" >>$manifest
mv "$lucide" licenses/icons__Lucide__LICENSE.txt

echo "$(ls licenses/*__*.txt | wc -l) licence files in licenses/"
