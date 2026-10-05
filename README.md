# BarMash : La Tournée Infernale 🍺🤮

Moteur de simulation spatiale en temps réel modélisant une soirée festive à haute charge dans un bar avec **10 000 clients autonomes**, plusieurs comptoirs de distribution et des mares de vomi dynamiques.

> **Objectif Pédagogique (Sup de Vinci - RNCP Bloc 4 Backend) :**  
> Ce projet constitue la **version initiale et naïve (non-optimisée)**. Elle a été délibérément conçue avec des anti-patterns de conception mémoire (allocations dynamiques systématiques sur le Tas, pointeurs dispersés, scans $\mathcal{O}(N^2)$) pour mettre en évidence l'**effondrement des performances**, les **CPU Stalls**, les **défauts de cache (Cache Misses)** et la **pression sur le Garbage Collector (GC)**.

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

## 2. Anti-Patterns Volontaires (Version Naïve pour l'Audit)

1. **Pointeurs Dispersés sur le Tas (`[]*Client`) :**
   - Chaque client est instancié individuellement via `new(Client)` sur la Heap.
   - Les structures mesurent **96 octets** (dépassant la ligne de cache L1/L2 de 64 octets), forçant de multiples *Cache Line Splits*.
   - Le *Hardware Prefetcher* du processeur ne peut pas anticiper les sauts d'adresses en RAM, provoquant des *CPU Stalls* continus.
2. **Allocations Dynamiques par Frame (`make()` dans la boucle de rendu) :**
   - Création de tranches éphémères à chaque tick pour filtrer les flaques de vomi (`make([]*VomitPuddle, ...)`).
   - Allocation à chaque tick d'une tranche de distances par client (`make([]BarDistance, len(bars))`).
   - Allocation de tranches pour le calcul de voisinage (`neighborsCrowd := make([]float64, 0, 8)`).
   - Résultat mesuré : **~247 Ko alloués par tick** (~760 allocations/op), déclenchant des cycles répétés du Garbage Collector.
3. **Calcul Brut de Distances $\mathcal{O}(N \times K)$ / $\mathcal{O}(N^2)$ :**
   - Chaque client calcule naïvement la distance Euclidienne avec `math.Sqrt()` vers tous les bars et effectue un scan naïf de bousculade sur les autres agents.

---

## 3. Structure du Projet

```text
tp/
├── main.go         # Moteur de simulation naïf, boucle de calcul et serveur WebSocket
├── main_test.go    # Micro-benchmarks (Cache Lines, Heap Allocations, Contigu vs Pointers)
├── index.html      # Interface web Canvas 2D temps réel avec HUD télémétrique
├── AUDIT_SEANCE_1.md # Rapport d'audit initial de performance
├── go.mod          # Module Go
└── go.sum          # Sommes de contrôle
```

---

## 4. Démarrage Rapide

### Prérequis
- **Go 1.20+** (installé et accessible dans le `PATH`)
- Un navigateur moderne (Chrome, Edge, Firefox)

### Lancer la Simulation en Temps Réel
```bash
go run main.go
```
Puis ouvrir votre navigateur sur :
👉 **[http://localhost:8080](http://localhost:8080)**

Le terminal affiche un rapport d'exécution à chaque seconde :
```text
[BarMash] Tick #00120 | TPS: 21 | Durée: 31.40 ms | RAM Heap: 42.15 MB | GC: 14 | Sobres:  6420 | Buveurs:  2780 | Vomisseurs:   800 | Flaques:  390 | Bières:  14210
```

---

## 5. Exécution des Micro-Benchmarks

Pour quantifier l'impact de la disposition mémoire et des allocations :

```bash
# Tester l'impact de la disposition mémoire (Contigu vs Séquentiel vs Sauts Aléatoires)
go test -bench="Benchmark[123]" -benchmem

# Mesurer le coût global de la boucle de calcul naïve
go test -bench="BenchmarkUpdateSimulationNaive" -benchmem
```

---

## 6. Prochaines Étapes d'Optimisation (Séances 2 & 3)

1. **Transformation DoD / SoA & Tranche Contiguë :** Remplacer `[]*Client` par un tableau contigu `[]Client` aligné sur 64 octets.
2. **Zéro-Allocation Mémoire (0 B/op) :** Réutiliser des buffers fixes ou des tableaux de dimensions statiques pour éliminer complètement la pression sur le GC.
3. **Partitionnement Spatial 1D / 2D (Grille Spatiale) :** Remplacer le scan global brut par un hash spatial contigu pour ramener la complexité à $\mathcal{O}(1)$ par agent.
