# Plan d'Optimisation • BarMash

**Machine :** AMD Ryzen 7 9700X (8 cœurs / 16 threads, L2 1 Mo/cœur, L3 32 Mo) • Windows 11 • Go 1.27.0
**Protocole :** `TestGoldenChecksum` (non-régression bit à bit) + `benchstat` n=10 (p < 0.05) + profils `pprof`

---

## 1. Diagnostic de départ (profil CPU `phase=rush`, `profiles/00_cpu_rush.pprof`)

| Zone du Hot Path | Part CPU | Cause physique |
| :--- | ---: | :--- |
| Scan des flaques (`for _, vp := range vomitPuddles`) | **~60 %** | O(N × P) = 10 000 × ~450 tests/tick, chaque flaque derrière un pointeur `*VomitPuddle` dispersé sur le tas |
| Scan de bousculade (60 voisins / client) | **~33 %** | Lecture de `clients[nIdx].X/Y` via `[]*Client` : 1 indirection + 1 ligne de cache de 96 o par voisin |
| `math.Sin` / `math.Cos` | ~5 % | Deux appels trigonométriques séparés par client mobile |
| `make()` dans la boucle | ~2 % | `barDistances`, `neighborsCrowd`, nouvelles flaques et tranche filtrée à chaque tick |

Frame réseau : **1,08 ms** et **586 Ko alloués** par frame (369 Ko de JSON), `runtime.ReadMemStats` (*Stop-The-World*) à chaque tick.

**Constat clé :** le benchmark initial ne mesurait que le début de soirée (0 flaque). En régime de croisière, un tick coûte **5,54 ms au lieu de 1,46 ms** : le vrai goulot (les flaques) était invisible.

---

## 2. Positionnement face au TP fil rouge ZombieHorde

Les leviers du TP de référence ne sont **pas** repris tels quels ; chaque problème reçoit une solution différente :

| Problème | TP ZombieHorde (référence) | BarMash (ce projet) |
| :--- | :--- | :--- |
| Disposition mémoire des agents | Réordonnancement des champs pour une struct de 64 o | **Séparation chaud/froid** : positions isolées dans une tranche contiguë `[]Vec2` de 16 o (160 Ko, tient dans le L2) |
| Allocations par tick | Buffers persistants réinitialisés à chaque tick | **Suppression à la source** : flaques par valeur, expiration FIFO par horodatage, tranches mortes supprimées |
| Recherche de proximité | Grille 1D fixe `[100*100]Cell` des agents | **Index CSR des flaques** reconstruit par tri par comptage, + liste des flaques créées pendant le tick |
| Coût de `math.Sqrt` | Comparaison quadratique dx² + dy² ≤ R² | **Rejet AABB** (\|dx\| ≥ R ou \|dy\| ≥ R) avant tout calcul : résultat bit à bit identique, `Sqrt` conservé pour les seuls candidats |
| Réseau | Streaming binaire | **Encodeur JSON append-only** octet-identique à `encoding/json` (front inchangé) + écriture directe de la frame WebSocket |

---

## 3. Feuille de route (ordonnée par la loi d'Amdahl)

| # | Optimisation | Hypothèse matérielle | Vérification |
| :- | :--- | :--- | :--- |
| O1 | Flaques stockées par valeur `[]VomitPuddle` + expiration FIFO par `ExpireTick` | Suppression des sauts de pointeurs et des allocations par flaque ; tableau sans pointeur → `noscan`, ignoré par le GC | `make bench STEP=01_puddles_values` puis `benchstat` |
| O2 | Index spatial CSR des flaques (cellules de 50 u, voisinage 3×3) | O(N × P) → O(N × ~3) : ~450 tests/client → ~3 | `make bench STEP=02_puddle_index` + `make profile` |
| O3 | Séparation chaud/froid des positions (`[]Vec2`) + rejet AABB du voisinage | Une ligne de cache de 64 o porte 4 positions au lieu de 0,66 client ; le jeu de données chaud (160 Ko) reste en L2 | `make bench STEP=03_hot_cold` |
| O4 | Zéro allocation résiduelle (tranches mortes, centres de bars précalculés) | 0 B/op et 0 allocs/op sur le tick | `make escape` + `make bench STEP=04_zero_alloc` |
| O5 | Trigonométrie fusionnée `math.Sincos` | Une seule réduction d'argument au lieu de deux | `make golden` (bit-exactitude) + `benchstat` |
| O6 | Frame réseau : encodeur JSON append-only, buffer réutilisé, `runtime/metrics` au lieu de `ReadMemStats` | Fin des 586 Ko/frame de déchets et de la pause STW par tick | `BenchmarkBuildFrame` + test d'égalité octet à octet |
| O7 | Expérimentation : table de Voronoï précalculée pour le bar le plus proche (compromis espace-temps) | À confirmer : la recherche du bar pèse peu dans le profil, le gain attendu est borné par Amdahl | `benchstat` : retenu ou rejeté selon la p-value |

Chaque étape fait l'objet d'un commit séparé avec `bench/<étape>.txt`, comparé par `benchstat` à l'étape précédente. Une optimisation qui casse `TestGoldenChecksum` ou n'apporte pas de gain significatif est documentée comme échec puis annulée.
