# BarMash - Commandes de reproductibilité des mesures
# Prérequis : go install golang.org/x/perf/cmd/benchstat@latest
#             go install golang.org/x/tools/go/analysis/passes/fieldalignment/cmd/fieldalignment@latest

STEP  ?= current
COUNT ?= 10
OLD   ?= perf/bench/00_baseline.txt
NEW   ?= perf/bench/$(STEP).txt

.PHONY: run test golden bench benchstat profile live-profile escape align

run: ## Lance le serveur temps réel (UI :8080, pprof localhost:6060)
	go run .

test: ## Tests unitaires + checksum de non-régression logique
	go test -run . -v .

golden: ## Vérifie uniquement l'empreinte déterministe (MasterSeed = 42)
	go test -run TestGoldenChecksum -v .

bench: ## Série de COUNT échantillons enregistrée dans perf/bench/$(STEP).txt
	go test -run XXX -bench 'UpdateSimulation|BuildFrame' -benchmem -count=$(COUNT) -timeout 30m . | tee $(NEW)

benchstat: ## Comparaison statistique (Mann-Whitney) entre OLD et NEW
	benchstat $(OLD) $(NEW)

profile: ## Profil CPU + mémoire du scénario de croisière, puis flamegraph interactif
	go test -run XXX -bench 'UpdateSimulation/phase=rush' -benchtime=3s \
		-cpuprofile perf/profiles/$(STEP)_cpu_rush.pprof -memprofile perf/profiles/$(STEP)_mem_rush.pprof .
	go tool pprof -http=:8081 perf/profiles/$(STEP)_cpu_rush.pprof

live-profile: ## Capture 20 s de profil CPU sur le serveur en cours d'exécution
	curl -s -o perf/profiles/$(STEP)_cpu_live.pprof "http://localhost:6060/debug/pprof/profile?seconds=20"

escape: ## Décisions d'échappement du compilateur (Heap vs Stack)
	go build -gcflags="-m" . 2>&1 | grep -E "escapes|moved to heap"

align: ## Audit d'alignement des structures
	fieldalignment ./...
