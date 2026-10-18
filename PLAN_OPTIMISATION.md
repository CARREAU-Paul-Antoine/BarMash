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

---

## 4. Résultats mesurés (benchstat, n = 10, p < 0.05)

Toutes les étapes conservent le checksum de référence `0xaa36b4f0b73c614c` (état bit à bit après 4 000 ticks).

| Étape | Commit | Tick early | Tick rush | Frame réseau | Verdict |
| :--- | :--- | ---: | ---: | ---: | :--- |
| Baseline | `343a4ee` | 1,461 ms | 5,538 ms | 1,090 ms | 434 Ko et 3 400 allocs/tick |
| O1 Flaques par valeur + FIFO | `b3feb19` | +1,4 % | +0,6 % | ~ | **Échec** : hypothèse invalidée |
| O2 Index CSR des flaques | `aa4442e` | ~ | **−66,1 %** | ~ | ×2,95 en croisière |
| O3a AABB `\|\|` (variante) | `bench/03a_*` | **+52 %** | **+44 %** | ~ | **Échec** : rejetée |
| O3 Chaud/froid + AABB `math.Abs` | `ee75cd5` | −21,0 % | −15,5 % | ~ | Retenue |
| O4 Zéro allocation | `ed9efdf` | −9,9 % | −6,1 % | ~ | **0 B/op, 0 allocs/op** |
| O5 `math.Sincos` | `c3bbced` | −8,2 % | −4,6 % | ~ | Bit-exact |
| O6 Encodeur append-only + `runtime/metrics` | `4812ed8` | (+2,8 %)¹ | (+1,8 %)¹ | **−66,5 %** | 0 B/frame |
| O6b Table décimale | `414297b` | ~ | ~ | **−60,5 %** | |
| O7 Table de Voronoï certifiée | `f54df13` | −8,9 % | −6,2 % | ~² | Plafond d'Amdahl : 11,5 % |
| **Total** | | **0,921 ms (−37 %)** | **1,358 ms (−75 %, ×4,1)** | **0,130 ms (−88 %, ×8,4)** | **0 alloc** |

¹ `updateSimulation` est inchangé. Un A/B entrelacé (`bench/ab_*_interleaved.txt`, +2,8 %, p=0,000) et la comparaison du code machine montrent que seuls le pas des flaques (48 → 40 o) et le padding `NOP` diffèrent : c'est un **biais d'alignement du code**, pas un effet algorithmique.
² −18,7 % mesuré sur un code non modifié : variance inter-séries de `BuildFrame` (±4 à 12 %).

### Validation sous charge réelle (serveur + client WebSocket, 25 s, 750 ticks)

| Mesure | Naïf (`343a4ee`) | Optimisé (`f54df13`) |
| :--- | ---: | ---: |
| Débit | 30 frames/s | 30 frames/s |
| CPU consommé (profil live de 20 s) | 2 610 ms (13,1 % d'un cœur) | 840 ms (4,2 %) — **−68 %** |
| Cycles de GC | 528 | **4** |
| Mémoire allouée cumulée | 1 611 Mo | **8 Mo** |
| État au tick 750 (sobres / buveurs / bières) | 6 828 / 3 172 / 22 688 | **identique** |
| pprof sur le port public `:8080/debug/pprof/` | — | **404** (isolé sur `localhost:6060`) |

---

## 5. Échecs constructifs

### Échec n°1 — O1 : flaques par valeur (gain nul)
- **Hypothèse :** les ~450 `*VomitPuddle` dispersés sur le tas provoquent des *cache misses* dans le scan des flaques (60 % du CPU).
- **Mesure :** early +1,4 % (p=0,002), rush +0,6 % (p=0,000).
- **Cause physique :** les flaques naïves étaient allouées consécutivement et tiennent en ~18 Ko, entièrement dans le L1d (48 Ko). Le déréférencement ne coûtait presque rien ; le coût était **algorithmique** (4,5 M tests par tick). La struct passait en outre de 40 à 48 o.
- **Leçon :** le profil désignait la bonne boucle, mais pas la bonne cause. C'est O2 (réduction de complexité) qui a apporté ×2,95.

### Échec n°2 — O3a : rejet AABB `ndx >= 22 || ndx <= -22` (+52 %)
- **Hypothèse :** éviter `math.Sqrt` sur 97 % des voisins.
- **Mesure :** early +52 %, rush +44 % ; la séparation chaud/froid seule gagnait 6 %.
- **Cause physique :** le premier saut dépend du **signe** de dx, qui est aléatoire, d'où ~50 % de mauvaises prédictions de branchement. 600 k voisins × 50 % × ~15 cycles ≈ 0,9 ms, soit exactement l'écart mesuré.
- **Correction :** `math.Abs(ndx) >= 22` (masque de bit, sans branchement) ne laisse qu'un saut, prévisible à 97 % : **−19 %**. Le même test logique écrit deux fois peut coûter +60 % ou −19 %.
