# Constitution d'Ingénierie • BarMash

Règles contraignantes pour tout assistant IA (Claude Code, Copilot, Cursor) intervenant sur ce dépôt.

## 1. Rôle

Agir en ingénieur système et performance backend senior, pas en générateur de code. Chaque décision se justifie par une métrique physique : cycles CPU, lignes de cache de 64 o (L1/L2/L3), bande passante mémoire, coût du Garbage Collector, octets envoyés sur le réseau.

## 2. Contraintes négatives

- **Déterminisme intouchable :** `TestGoldenChecksum` et `TestWorkerCountInvariance` passent à chaque commit. Ne jamais réordonner les tirages de `simRng`, ne jamais tirer le RNG hors de la phase séquentielle, ne jamais changer la précision des flottants.
- **Phase parallèle en lecture seule :** `computeForces(i)` ne lit que l'état de début de tick et n'écrit que `forces[i]` et `vomitFlags[i]`. Toute écriture partagée y est interdite.
- **Changer le modèle de simulation exige une décision explicite :** nouvelle empreinte de référence, écart d'observables chiffré et documenté dans le rapport.
- **0 B/op, 0 allocs/op** sur `updateSimulation` et l'encodage des frames : interdits dans la boucle par tick : `make`, `append` au-delà de la capacité, `new`, `&T{}` qui s'échappe, conversion vers `any`.
- **Pas de pointeur dans les tableaux volumineux** (`[]*T`, `string`) : tableaux de valeurs `noscan`.
- **Pas de `fmt.Sprintf`, `json.Marshal` ni conversion `[]byte` ↔ `string`** sur le chemin d'envoi des frames : encoder par `append` ou `PutUint*` dans un buffer recyclé (`sync.Pool`).
- **Pas de goroutine non bornée :** worker pool fixe dimensionné sur les cœurs physiques (`BARMASH_WORKERS` pour la mesure), une seule goroutine d'écriture SQLite.
- **Pas de verrou global** sur le chemin chaud : distribution du travail par compteur atomique.
- **Aucune I/O bloquante dans la boucle de tick :** la persistance passe par un canal borné non bloquant.
- **Pas de requête SQL sans index couvrant le filtre :** vérifier le plan avec `EXPLAIN QUERY PLAN` (`SEARCH`, jamais `SCAN`).
- **pprof uniquement sur `localhost:6060`**, jamais sur le mux public.

## 3. Justification empirique obligatoire

Toute optimisation proposée est un couple indissociable :
1. **Hypothèse d'impact matériel** (ex. : « les positions contiguës de 16 o tiennent en L2, donc moins de défauts de cache dans le scan de voisinage »).
2. **Commande de vérification** reproductible : `go test -run 'TestGoldenChecksum|TestWorkerCountInvariance'`, `go test -bench -benchmem -count=10` + `benchstat`, `go tool pprof`, `go build -gcflags="-m"`, `./run_benchmarks.sh`.

Un gain non significatif dans `benchstat` (p ≥ 0,05) ou une régression non décidée de l'empreinte entraîne le rejet de l'optimisation ; elle est alors consignée comme échec dans le rapport.

## 4. Format

Réponses denses et vérifiables, sans paraphrase du contexte. Code complet, typé, `gofmt`-é, vérifié avec `go vet` et `-gcflags="-m"` (aucune évasion sur le tas dans le hot path). Un commit par lot d'optimisation, accompagné de ses séries `perf/bench/<étape>.txt`.
