#!/usr/bin/env bash
# Test de charge HTTP de GET /api/history (Vegeta) : latences p50 / p95 / p99 avec et sans index.
#
#   perf/load_test.sh                      # 15 s par palier
#   DURATION=30s perf/load_test.sh
#
# Prérequis : go, vegeta (go install github.com/tsenart/vegeta/v12@latest).
set -euo pipefail

DURATION=${DURATION:-15s}
ROOT=$(cd "$(dirname "$0")/.." && pwd)
OUT=$ROOT/perf/campaign
BIN=$ROOT/bin
EXE=""
case "$(uname -s)" in MINGW* | MSYS* | CYGWIN*) EXE=".exe" ;; esac
command -v vegeta >/dev/null || { echo "outil manquant : vegeta" >&2; exit 1; }
mkdir -p "$OUT" "$BIN" "$ROOT/data"
cd "$ROOT"
go build -o "$BIN/barmash_final$EXE" .

# Bases de 200 000 lignes (50 sessions x 4 000 secondes), avec et sans index
for v in index noindex; do
	db="data/load_$v.db"
	if [ ! -f "$db" ]; then
		if [ "$v" = noindex ]; then export BARMASH_DB_NOINDEX=1; fi
		BARMASH_SEED_DB="$db" go test -count=1 -run TestSeedLoadTestDB . >/dev/null
		unset BARMASH_DB_NOINDEX
	fi
done

# Export complet d'une session (4 000 lignes, ~1 h de simulation) parmi 50.
# Assez lourd pour dépasser la résolution de l'horloge Go sous Windows (~0,5 ms).
TARGET="GET http://localhost:8080/api/history?session=25&from=0&to=119970"

run() { # run <variante> <débit>
	local v=$1 rate=$2 pid
	if [ "$v" = noindex ]; then export BARMASH_DB_NOINDEX=1; fi
	BARMASH_DB="data/load_$v.db" "$BIN/barmash_final$EXE" >/dev/null 2>&1 &
	pid=$!
	unset BARMASH_DB_NOINDEX
	for _ in $(seq 1 50); do curl -s -o /dev/null "http://localhost:8080/" && break; sleep 0.2; done
	echo "$TARGET" | vegeta attack -rate="$rate" -duration="$DURATION" > "$OUT/load_${v}_${rate}.bin"
	kill "$pid" 2>/dev/null || true
	wait "$pid" 2>/dev/null || true
	echo "== $v, $rate req/s pendant $DURATION"
	vegeta report "$OUT/load_${v}_${rate}.bin" | tee "$OUT/load_${v}_${rate}.txt"
	rm -f "$OUT/load_${v}_${rate}.bin"
}

run index 500
run noindex 50
run noindex 500
