# Journal d'Audit Initial (Séance 1) • BarMash : La Tournée Infernale
**Module :** Optimisations & Performances Backend (Sup de Vinci - RNCP Bloc 4)  
**Sujet :** Diagnostic & Audit de Performance — Version Naïve sous Haute Charge (10 000 agents)  
**Machine de test :** AMD Ryzen 7 5800H (16 threads, x86_64, Windows)  
**Graine :** MasterSeed = 42  

---

## 1. Analyse de la Structure Mémoire & Cache Misses

### Taille de la structure `Client`
L'exécution de `go test -v -run TestClientStructSize` révèle :
```text
Taille de la structure Client : 96 octets (Ligne de cache standard = 64 octets)
⚠️ NOTE ARCHITECTURE : La structure fait 96 octets et dépasse une seule ligne de cache (64B).
   Un accès à un seul client peut déclencher jusqu'à 2 lectures de lignes de cache (Cache Line Splits).
```

### Problème de Localité Spatiale
1. **Ligne de Cache Standard (64 octets) :**  
   Comme la structure fait 96 octets, elle est à cheval sur deux lignes de cache. Pour chaque agent lu, le contrôleur mémoire CPU doit souvent charger deux lignes de 64B consécutives.
2. **Dispersion sur le Tas (`[]*Client`) :**  
   Chaque client est alloué indépendamment via `new(Client)`. Les adresses en mémoire virtuelle sont discontinues. Le matériel de préchargement (*Hardware Prefetcher*) ne peut pas prédire la prochaine adresse mémoire, provoquant des **DRAM Stalls** (~150 à 250 cycles d'inactivité CPU par saut).

---

## 2. Résultats des Micro-Benchmarks Comparatifs

Commande exécutée :
```bash
go test -bench="Benchmark[123]" -benchmem
```

| Benchmark | Structure Mémoire | Temps / Opération | Allocs Mémoire | Impact Matériel |
| :--- | :--- | :--- | :--- | :--- |
| **`Benchmark1_ContiguousSlice`** | `[]Client` (Tranche contiguë) | **15.1 µs** | 0 B/op (0 alloc) | **100% optimal** (Prefetcher matériel L1/L2 à plein régime) |
| **`Benchmark2_PointersSequential`** | `[]*Client` (Pointeurs séquentiels) | **16.3 µs** | 0 B/op (0 alloc) | Pénalité d'indirection (accès pointeur 64-bit puis objet) |
| **`Benchmark3_PointersScattered`** | `[]*Client` (Sauts aléatoires) | **27.6 µs** | 0 B/op (0 alloc) | **1.8x plus lent** (Destruction totale de la localité spatiale) |

---

## 3. Coût de la Boucle de Simulation Naïve

Commande exécutée :
```bash
go test -bench="BenchmarkUpdateSimulationNaive" -benchtime=5s -benchmem
```

Résultats mesurés :
```text
BenchmarkUpdateSimulationNaive-16    1177    3 057 917 ns/op    347 492 B/op    2 714 allocs/op
```

### Constats Majeurs :
1. **Goulot d'Étranglement Mémoire (Heap Churn) :**
   - Chaque tick de simulation alloue **~347 Ko de mémoire éphémère** sur le Tas avec plus de 2 700 allocations dynamiques.
   - À 30 ticks par seconde, cela représente un débit de **~10.4 Mo/seconde d'ordures mémoire**.
   - Le *Garbage Collector (GC)* de Go se déclenche en continu (`num_gc` augmente rapidement), causant des micro-pauses *Stop-The-World* et un jitter important sur la fluidité.
2. **Coût de Calcul ($\mathcal{O}(N \times K)$) :**
   - Le balayage brut pour calculer les distances avec `math.Sqrt()` consomme la majorité du temps CPU.
   - En intégrant l'encodage JSON et le transport WebSocket, le TPS réel s'effondre en-dessous de l'objectif de 30 TPS sous forte bousculade.

---

## 4. Feuille de Route d'Optimisation

| Axe d'Amélioration | Technique Cible | Gain Attendu |
| :--- | :--- | :--- |
| **Localité de Cache** | Transformation vers un tableau contigu `[]Client` compacté à ≤ 64 octets | Réduction drastique des Cache Misses et saturation du prefetcher |
| **Zéro-Allocation** | Élimination de tous les `make()` dans la boucle au profit de buffers statiques | **0 B/op** (Éradication des pauses GC) |
| **Partitionnement Spatial** | Grille spatiale 1D contiguë (`[CellCount]Cell`) | Passage de $\mathcal{O}(N \times K)$ à $\mathcal{O}(1)$ local pour les voisins et le vomi |
