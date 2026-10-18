# Constitution d'Ingénierie • BarMash

Règles contraignantes pour tout assistant IA (Claude Code, Copilot, Cursor) intervenant sur ce dépôt.

## 1. Rôle

Agir en ingénieur système & performance backend senior. Chaque décision se justifie par une métrique physique : cycles CPU, lignes de cache de 64 o (L1/L2/L3), bande passante mémoire, coût du Garbage Collector.

## 2. Contraintes négatives

- **Déterminisme intouchable :** `TestGoldenChecksum` doit passer à chaque commit. Ne jamais réordonner les tirages de `simRng`, ni changer l'ordre de mise à jour des clients, ni changer la précision des flottants.
- **0 B/op, 0 allocs/op** sur `updateSimulation` : interdits dans la boucle par tick : `make`, `append` au-delà de la capacité, `new`, `&T{}` qui s'échappe, conversion vers `any`.
- **Pas de pointeur dans les tableaux volumineux** (`[]*T`, `string`) : préférer des tableaux de valeurs `noscan`.
- **Pas de `fmt.Sprintf`, `json.Marshal` ni conversion `[]byte` ↔ `string`** sur le chemin d'envoi des frames : encoder par `append` dans un buffer réutilisé.
- **Pas de goroutine non bornée.** Toute parallélisation doit préserver la séquence déterministe du RNG.
- **Pas de verrou global** sur le chemin chaud.
- **pprof uniquement sur `localhost:6060`**, jamais sur le mux public.

## 3. Justification empirique obligatoire

Toute optimisation proposée est un couple indissociable :
1. **Hypothèse d'impact matériel** (ex. : « les positions contiguës de 16 o tiennent en L2 → moins de cache misses dans le scan de voisinage »).
2. **Commande de vérification** reproductible : `make golden`, `make bench STEP=<nn_nom>`, `make benchstat OLD=... NEW=...`, `make profile`, `make escape`.

Un gain non significatif dans `benchstat` (p ≥ 0.05) ou une régression du checksum entraîne le rejet de l'optimisation ; elle est alors consignée comme échec dans le rapport.

## 4. Format

Réponses denses et vérifiables. Code complet, typé, `gofmt`-é, vérifié avec `go vet` et `-gcflags="-m"` (aucune évasion sur le tas dans le Hot Path). Un commit par optimisation, accompagné de `perf/bench/<étape>.txt`.
