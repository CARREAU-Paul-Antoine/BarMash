#!/usr/bin/env bash
# Campagne de mesure complète BarMash : baseline (commit naïf) vs version courante.
#
#   ./run_benchmarks.sh            # campagne complète (~15 min)
#   TICKS=2000 RUNS=5 COUNT=5 ./run_benchmarks.sh
#
# Prérequis : go, benchstat (golang.org/x/perf/cmd/benchstat), hyperfine.
set -euo pipefail

BASELINE_REF=${BASELINE_REF:-343a4ee}   # Moteur naïf + protocole de mesure, avant toute optimisation
TICKS=${TICKS:-5000}                    # 5 000 ticks : début de soirée puis ~450 flaques actives
WARMUP=${WARMUP:-3}
RUNS=${RUNS:-10}
COUNT=${COUNT:-10}

ROOT=$(cd "$(dirname "$0")" && pwd)
OUT=$ROOT/perf/campaign
BIN=$ROOT/bin
EXE=""
case "$(uname -s)" in MINGW* | MSYS* | CYGWIN*) EXE=".exe" ;; esac

for tool in go benchstat hyperfine; do
	command -v "$tool" >/dev/null || { echo "outil manquant : $tool" >&2; exit 1; }
done
mkdir -p "$OUT" "$BIN"
cd "$ROOT"

echo "== 1/8 Environnement"
{
	go version
	go env GOOS GOARCH
	date -u +"%Y-%m-%dT%H:%M:%SZ"
	git rev-parse --short HEAD
} | tee "$OUT/environment.txt"

echo "== 2/8 Construction de la baseline ($BASELINE_REF) et de la version courante"
WT=$(mktemp -d)
trap 'git worktree remove --force "$WT" >/dev/null 2>&1 || true' EXIT
git worktree add -q --detach "$WT" "$BASELINE_REF"
cp headless.go "$WT/"   # Même harnais headless des deux côtés
(cd "$WT" && go build -o "$BIN/barmash_baseline$EXE" .)
go build -o "$BIN/barmash_final$EXE" .

echo "== 3/8 Non-régression (empreinte de référence, oracles d'équivalence, invariance au nombre de workers)"
go test -count=1 . | tee "$OUT/tests.txt"
go test -count=1 -run TestHistoryQueryPlan -v . | grep "index=" | tee "$OUT/query_plan.txt"

echo "== 4/8 Micro-benchmarks Go du tick et de la frame (n=$COUNT, même session pour les deux versions)"
(cd "$WT" && go test -run XXX -bench 'UpdateSimulation|BuildFrame' -benchmem -count="$COUNT" -timeout 60m .) > "$OUT/bench_baseline.txt"
go test -run XXX -bench 'UpdateSimulation|BuildFrame' -benchmem -count="$COUNT" -timeout 60m . > "$OUT/bench_final.txt"
benchstat "$OUT/bench_baseline.txt" "$OUT/bench_final.txt" | tee "$OUT/benchstat.txt"

echo "== 5/8 Montée en charge du worker pool (1 à 16 workers)"
for w in 1 2 4 8 16; do
	BARMASH_WORKERS=$w go test -run XXX -bench 'UpdateSimulation' -benchmem -count="$COUNT" . > "$OUT/workers_$w.txt"
done
benchstat "$OUT/workers_1.txt" "$OUT/workers_2.txt" "$OUT/workers_4.txt" "$OUT/workers_8.txt" "$OUT/workers_16.txt" | tee "$OUT/workers.benchstat.txt"

echo "== 6/8 I/O : frame binaire, insertions SQLite, requêtes indexées, sync.Pool"
go test -run XXX -bench 'BuildFrameBinary|TelemetryInsert|HistoryQuery|HistoryHandler' -benchmem -count="$COUNT" -timeout 60m . > "$OUT/bench_io.txt"
benchstat "$OUT/bench_io.txt" | tee "$OUT/io.benchstat.txt"

echo "== 7/8 Hyperfine : $TICKS ticks de bout en bout (warmup $WARMUP, $RUNS runs)"
export BARMASH_TICKS=$TICKS
hyperfine -N --warmup "$WARMUP" --runs "$RUNS" \
	--export-json "$OUT/hyperfine.json" --export-markdown "$OUT/hyperfine.md" \
	-n baseline "$BIN/barmash_baseline$EXE" \
	-n final "$BIN/barmash_final$EXE"
# Depuis O9 (modèle en deux phases), l'état final diffère légèrement du naïf : on l'affiche pour comparaison.
"$BIN/barmash_baseline$EXE" | tee "$OUT/state_baseline.txt"
"$BIN/barmash_final$EXE" | tee "$OUT/state_final.txt"

echo "== 8/8 Profils pprof réels (CPU + allocations) des deux binaires"
for v in baseline final; do
	BARMASH_CPUPROFILE="$OUT/${v}_cpu.pprof" BARMASH_MEMPROFILE="$OUT/${v}_allocs.pprof" "$BIN/barmash_$v$EXE" >/dev/null
	go tool pprof -top -nodecount=15 "$BIN/barmash_$v$EXE" "$OUT/${v}_cpu.pprof" > "$OUT/${v}_cpu_top.txt" 2>/dev/null
	go tool pprof -sample_index=alloc_space -top -nodecount=10 "$BIN/barmash_$v$EXE" "$OUT/${v}_allocs.pprof" > "$OUT/${v}_allocs_top.txt" 2>/dev/null
done
unset BARMASH_TICKS

echo
echo "Résultats dans $OUT"
echo "Flamegraph interactif : go tool pprof -http=:8081 $BIN/barmash_final$EXE $OUT/final_cpu.pprof"
