# BarMash : La Tournée Infernale 🍺🤮

Moteur de simulation spatiale en temps réel modélisant une soirée festive à haute charge dans un bar avec **10 000 clients autonomes**, plusieurs comptoirs de distribution et des mares de vomi dynamiques.

> **Objectif Pédagogique (Sup de Vinci - RNCP Bloc 4 Backend) :**  
> Le projet part d'une **version naïve** (commit `343a4ee`), délibérément conçue avec des anti-patterns de conception mémoire (allocations dynamiques systématiques sur le Tas, pointeurs dispersés, scans $\mathcal{O}(N^2)$). Il l'optimise ensuite étape par étape : chaque gain est prouvé par `benchstat`, et chaque étape est validée par un checksum bit à bit de l'état de la simulation (aucune régression logique).
>
> **Résultat :** tick de croisière **5,54 ms → 1,36 ms (×4,1)**, frame réseau **1,09 ms → 0,13 ms (×8,4)**, **0 allocation** par tick et par frame, 528 → 4 cycles de GC en 25 s de service réel. Détail dans [docs/PLAN_OPTIMISATION.md](docs/PLAN_OPTIMISATION.md).

---

## 1. Règles Métier de la Simulation

- **Monde & Grille 2D :**
  - Espace délimité de **2 000 x 2 000 unités**.
  - **8 comptoirs de bar** stratégiquement disposés (bars centraux, tireuses latérales, comptoirs VIP).
- **Entités & États des 10 000 Clients :**
  - **`StateSober` (Sobre / En route) :** Cherche le comptoir le plus proche, calcule les distances et s'y déplace en traversant la foule.
  - **`StateDrinking` (En train de boire) :** Consomme sa pinte au comptoir pendant une durée déterminée, augmente son alcoolémie (+0.45 à +0.65 g/L par verre) et régénère son endurance.
  - **`StateVomiting` (En train de vomir) :** Si l'alcoolémie dépasse le seuil de tolérance individuel, le client s'effondre et vomit. Il reste immobilisé au sol pendant plusieurs ticks, bloque sa case et génère une **mare de vomi**.
- **Mares de Vomi (`VomitPuddle`) :**
  - Chaque flaque persiste au sol pendant un certain nombre de ticks.
  - Bloque la case et perturbe le voisinage : les clients traversant ou s'approchant d'une mare voient leur vitesse s'effondrer (-75%), perdent rapidement leur endurance et ont leur trajectoire déviée par l'odeur nauséabonde.
- **Déterminisme Rigoureux :**
  - Utilisation d'une graine fixe (`MasterSeed = 42`) garantissant la parfaite reproductibilité des tests et des métriques à chaque exécution.

---

## 2. Anti-Patterns de la Version Naïve et Optimisations Appliquées

| Anti-pattern naïf | Optimisation | Étape |
| :--- | :--- | :--- |
| Scan des ~450 flaques par client, en $\mathcal{O}(N \times P)$ (60 % du CPU) | Index spatial CSR reconstruit par tri par comptage, voisinage 3×3 | O2 |
| `[]*Client` de 96 o lus 60 fois par tick dans le scan de bousculade | Séparation chaud/froid : `positions []Vec2` (160 Ko, résident en L2) + rejet AABB sans branche | O3 |
| `make([]BarDistance)`, `neighborsCrowd`, `[]*VomitPuddle` à chaque tick | Suppression à la source, flaques par valeur avec expiration FIFO | O1, O4 |
| `math.Cos` + `math.Sin` | `math.Sincos` (bit-exact) | O5 |
| `json.Marshal` + `string(data)` + `runtime.ReadMemStats` (STW) par frame | Encodeur append-only octet-identique, buffer réutilisé, `runtime/metrics` | O6, O6b |
| 8 `math.Sqrt` pour trouver le bar le plus proche | Table de Voronoï certifiée (6,4 Ko) | O7 |

---

## 3. Structure du Projet

```text
├── main.go                 # Serveur HTTP/WebSocket, envoi des frames, pprof isolé
├── simulation.go           # Moteur : état, initialisation, boucle de tick, statistiques
├── puddle_index.go         # O2 : index spatial CSR des flaques
├── bar_lut.go              # O7 : table de Voronoï certifiée du bar le plus proche
├── frame.go                # O6 : encodeur de frame append-only (octet-identique à encoding/json)
├── headless.go             # Mode sans serveur (BARMASH_TICKS=N) pour Hyperfine et les profils
├── protocol_test.go        # Checksum de référence, scénarios early/rush, benchmarks
├── equivalence_test.go     # Chaque optimisation comparée à son implémentation naïve
├── main_test.go            # Micro-benchmarks de localité (Séance 2)
├── run_benchmarks.sh       # Campagne complète baseline vs final en une commande
├── constitution.md         # Garde-fous d'ingénierie imposés aux assistants IA
├── web/
│   └── index.html          # Interface web Canvas 2D temps réel avec HUD télémétrique
├── docs/
│   ├── AUDIT_SEANCE_1.md   # Audit initial de la version naïve
│   ├── PLAN_OPTIMISATION.md # Diagnostic, feuille de route, résultats et échecs constructifs
│   └── RAPPORT_AUDIT.pdf   # Rapport d'audit final
└── perf/
    ├── bench/              # Séries brutes (n=10) et comparaisons benchstat de chaque étape
    ├── campaign/           # Dernière campagne run_benchmarks.sh (benchstat, Hyperfine, pprof)
    └── profiles/           # Profils pprof (CPU benchmark et live)
```

---

## 4. Démarrage Rapide

### Prérequis
- **Go 1.26+** (installé et accessible dans le `PATH`)
- Un navigateur moderne (Chrome, Edge, Firefox)
- Outils de mesure : `go install golang.org/x/perf/cmd/benchstat@latest`

### Lancer la Simulation en Temps Réel
```bash
go run .
```
Puis ouvrir **[http://localhost:8080](http://localhost:8080)**. Le profiling pprof est exposé uniquement sur **http://localhost:6060/debug/pprof/**.

---

## 5. Mesures & Profiling

```bash
# Non-régression logique (checksum bit à bit après 4 000 ticks, MasterSeed = 42)
go test -run TestGoldenChecksum -v .

# Tous les tests d'équivalence (index, Voronoï, Sincos, frame octet-identique)
go test -v .

# Benchmarks d'un tick (early = début de soirée, rush = ~450 flaques) et d'une frame réseau
go test -run XXX -bench 'UpdateSimulation|BuildFrame' -benchmem -count=10 . > perf/bench/new.txt
benchstat perf/bench/00_baseline.txt perf/bench/new.txt

# Profil CPU du scénario de croisière + flamegraph
go test -run XXX -bench 'UpdateSimulation/phase=rush' -cpuprofile cpu.pprof .
go tool pprof -http=:8081 cpu.pprof

# Profil live de 20 s sur le serveur en cours d'exécution
curl -o cpu.pprof "http://localhost:6060/debug/pprof/profile?seconds=20"
```

Campagne complète baseline vs version finale en une commande (tests, benchstat, Hyperfine, profils) : `./run_benchmarks.sh` (résultats dans `perf/campaign/`).
