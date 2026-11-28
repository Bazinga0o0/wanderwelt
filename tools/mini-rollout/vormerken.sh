#!/bin/bash
# Merkt ein Update auf dem Koordinator-Server vor. Es wird erst eingespielt,
# wenn seit 5 Minuten niemand mehr hostet (siehe wanderwelt-rollout).
#
#   vormerken.sh <name> [--koordinator DATEI] [--artifact DATEI]...
#                [--opt DATEI[=NAME]]... [--starter VERSION] [--manifest-py DATEI] [--config-py DATEI]
#
# --starter 0.1.10 setzt im Manifest starter.version/url/sha256/size aus der
# Artefakt-Datei wanderwelt-0.1.10.exe (die mit --artifact mitgegeben wird).
# Ziel: ssh-Host „mini“ (root), anderer Host mit WW_ROLLOUT_HOST.
set -euo pipefail
host=${WW_ROLLOUT_HOST:-mini}
[ $# -ge 1 ] || { sed -n '2,11p' "$0"; exit 2; }
name=$1; shift
case $name in *[!A-Za-z0-9._-]* | .*) echo "Name nur aus Buchstaben, Ziffern, . _ -" >&2; exit 2 ;; esac
koord=""; starter=""; mpy=""; cpy=""; arts=(); opts=()
while [ $# -gt 0 ]; do
	case $1 in
	--koordinator) koord=$2; shift 2 ;;
	--artifact) arts+=("$2"); shift 2 ;;
	--opt) opts+=("$2"); shift 2 ;;
	--starter) starter=$2; shift 2 ;;
	--manifest-py) mpy=$2; shift 2 ;;
	--config-py) cpy=$2; shift 2 ;;
	*) echo "unbekannt: $1" >&2; exit 2 ;;
	esac
done
entry="$(date +%Y%m%d-%H%M)-$name"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
[ -z "$koord" ] || cp "$koord" "$tmp/koordinator"
[ -z "$mpy" ] || cp "$mpy" "$tmp/manifest.py"
[ -z "$cpy" ] || cp "$cpy" "$tmp/config.py"
if [ ${#arts[@]} -gt 0 ]; then
	mkdir "$tmp/artifacts"
	for a in "${arts[@]}"; do cp "$a" "$tmp/artifacts/"; done
fi
if [ ${#opts[@]} -gt 0 ]; then
	mkdir "$tmp/opt"
	for o in "${opts[@]}"; do src=${o%%=*}; name=$(basename "$src"); [ "$o" = "$src" ] || name=${o#*=}; cp "$src" "$tmp/opt/$name"; done
fi
if [ -n "$starter" ]; then
	[ -f "$tmp/artifacts/wanderwelt-$starter.exe" ] || { echo "--artifact wanderwelt-$starter.exe fehlt" >&2; exit 2; }
	echo "$starter" >"$tmp/starter-version"
fi
[ -n "$(ls -A "$tmp")" ] || { echo "nichts vorzumerken" >&2; exit 2; }
(cd "$tmp" && find . -type f ! -name SHA256SUMS -print0 | sort -z | xargs -0 sha256sum >SHA256SUMS)
q=/var/lib/wanderwelt-rollout/queue
tar -C "$tmp" -cf - . | ssh "$host" "set -e; rm -rf $q/.incoming-$entry; mkdir -p $q/.incoming-$entry; tar -C $q/.incoming-$entry -xf -; mv $q/.incoming-$entry $q/$entry"
echo "vorgemerkt: $entry"
ssh "$host" wanderwelt-rollout status
