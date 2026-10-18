package main

import (
	"fmt"
	"log"
	"math"
	"math/rand"
	"net/http"
	"net/http/pprof"
	"runtime/metrics"
	"time"

	"golang.org/x/net/websocket"
)

// -----------------------------------------------------------------------------
// Constantes Métier & Configuration de la Simulation "BarMash"
// -----------------------------------------------------------------------------

const (
	// Graine fixe pour garantir un comportement 100% déterministe
	MasterSeed int64 = 42

	// Dimensions de l'établissement
	WorldSize float64 = 2000.0

	// Population de clients autonomes
	TotalClients int = 10000

	// Fréquence cible de mise à jour (Ticks Per Second)
	TargetTPS int = 30

	// Paramètres Bar & Boisson équilibrés
	BarCounterCount        int     = 8
	ServingRadius          float64 = 50.0
	DrinkDurationTicks     int     = 150  // ~5 secondes pour savourer sa bière
	VomitDurationTicks     int     = 60   // ~2 secondes immobilisé pour vomir
	VomitRadius            float64 = 24.0 // Taille de flaque raisonnable
	VomitPerturbRadius     float64 = 50.0
	MaxStamina             float64 = 100.0
	DefaultClientSpeed     float64 = 3.4
	MaxNeighborsCheckNaive int     = 60 // Échantillon de balayage naïf O(N * K) pour simuler la bousculade
)

// -----------------------------------------------------------------------------
// Énumérations & Structures (Volontairement Naïves & Basées Pointeur)
// -----------------------------------------------------------------------------

// ClientState représente l'état d'ébriété et d'action du client
type ClientState int

const (
	StateSober    ClientState = 0 // Sobre / En route vers un bar ou en balade
	StateDrinking ClientState = 1 // En train de consommer tout en circulant dans la salle
	StateVomiting ClientState = 2 // En train de vomir (bloqué sur place temporairement)
)

func (s ClientState) String() string {
	switch s {
	case StateSober:
		return "Sober"
	case StateDrinking:
		return "Drinking"
	case StateVomiting:
		return "Vomiting"
	default:
		return "Unknown"
	}
}

// Vec2 est la donnée chaude du client : lue 60 fois par tick par le scan de bousculade.
// 4 positions par ligne de cache de 64 o ; 10 000 positions = 160 Ko, résidentes en L2.
type Vec2 struct {
	X, Y float64
}

// Client modélise un fêtard dans le bar (données froides et tièdes, lues une fois par tick).
// O3 : la position est séparée dans la tranche contiguë positions (séparation chaud/froid).
type Client struct {
	ID             int64
	Speed          float64
	Alcohol        float64 // Alcoolémie en g/L (augmente avec les bières)
	Stamina        float64 // Jauge d'endurance (0.0 - 100.0)
	Tolerance      float64 // Seuil individuel d'alcoolémie avant risque critique de vomi
	State          ClientState
	StateTimer     int // Nombre de ticks restants dans l'état courant
	TargetBarID    int
	DrinksConsumed int
	WanderAngle    float64
}

// BarCounter représente un comptoir de distribution de bières
type BarCounter struct {
	ID          int     `json:"id"`
	Name        string  `json:"name"`
	X           float64 `json:"x"`
	Y           float64 `json:"y"`
	W           float64 `json:"w"`
	H           float64 `json:"h"`
	BeersServed int     `json:"beers_served"`
}

// VomitPuddle représente une flaque de vomi au sol bloquant la case.
// Stockée par valeur (aucun pointeur) : le tableau des flaques est marqué noscan par le runtime.
// remaining_ticks est dérivé à l'encodage de la frame : ExpireTick - currentTick.
type VomitPuddle struct {
	ID         int64
	X          float64
	Y          float64
	Radius     float64
	ExpireTick int64 // Tick de disparition (horodatage absolu)
}

// PuddleLifetimeTicks est identique pour toutes les flaques : elles expirent dans leur ordre de création (FIFO).
const PuddleLifetimeTicks = VomitDurationTicks * 2

// MaxPuddlesCapacity dimensionne la tranche des flaques une fois pour toutes (~460 flaques en croisière).
const MaxPuddlesCapacity = 2048

// -----------------------------------------------------------------------------
// Télémétrie & Payloads WebSocket
// -----------------------------------------------------------------------------

// SimStats regroupe les indicateurs de performance et d'état
type SimStats struct {
	Seed           int64   `json:"seed"`
	Tick           int64   `json:"tick"`
	TPS            int     `json:"tps"`
	TickDurationMs float64 `json:"tick_duration_ms"`
	TotalClients   int     `json:"total_clients"`
	SoberCount     int     `json:"sober_count"`
	DrinkingCount  int     `json:"drinking_count"`
	VomitingCount  int     `json:"vomiting_count"`
	BeersServed    int     `json:"beers_served"`
	ActivePuddles  int     `json:"active_puddles"`
	AvgAlcohol     float64 `json:"avg_alcohol"`
	HeapAllocMB    float64 `json:"heap_alloc_mb"`
	TotalAllocMB   float64 `json:"total_alloc_mb"`
	NumGC          uint32  `json:"num_gc"`
}

// La frame envoyée à l'interface Web Canvas à chaque tick est encodée par appendFrame (frame.go) :
// {"stats":SimStats,"world_size":2000,"bars":[...],"puddles":[...],"clients":[{"x","y","s","a"}...]}

// -----------------------------------------------------------------------------
// État Global de la Simulation (Moteur Naïf)
// -----------------------------------------------------------------------------

var (
	simRng          *rand.Rand
	clients         []Client              // Tranche contiguë de valeurs (données froides)
	positions       []Vec2                // positions[i] = position du client i (données chaudes)
	barCenters      [BarCounterCount]Vec2 // Centres des comptoirs, invariants : précalculés à l'init
	bars            []BarCounter
	vomitPuddles    []VomitPuddle // Flaques actives, contiguës et triées par ExpireTick croissant
	puddleIdSeq     int64
	totalBeersCount int
	currentTick     int64
	lastTickTimeMs  float64
	measuredTPS     int
)

// initSimulation initialise le bar, les comptoirs et les 10 000 clients avec MasterSeed
func initSimulation(seed int64) {
	simRng = rand.New(rand.NewSource(seed))
	puddleIdSeq = 0
	totalBeersCount = 0
	currentTick = 0
	vomitPuddles = make([]VomitPuddle, 0, MaxPuddlesCapacity)

	// Création des 8 comptoirs de bar répartis dans la salle
	bars = []BarCounter{
		{ID: 0, Name: "Bar Central Nord", X: WorldSize*0.5 - 120, Y: WorldSize*0.35 - 30, W: 240, H: 60},
		{ID: 1, Name: "Bar Central Sud", X: WorldSize*0.5 - 120, Y: WorldSize*0.65 - 30, W: 240, H: 60},
		{ID: 2, Name: "Tireuse Ouest 1", X: 100, Y: WorldSize * 0.25, W: 60, H: 200},
		{ID: 3, Name: "Tireuse Ouest 2", X: 100, Y: WorldSize * 0.65, W: 60, H: 200},
		{ID: 4, Name: "Tireuse Est 1", X: WorldSize - 160, Y: WorldSize * 0.25, W: 60, H: 200},
		{ID: 5, Name: "Tireuse Est 2", X: WorldSize - 160, Y: WorldSize * 0.65, W: 60, H: 200},
		{ID: 6, Name: "Comptoir VIP Nord", X: WorldSize*0.3 - 80, Y: 100, W: 160, H: 60},
		{ID: 7, Name: "Comptoir VIP Sud", X: WorldSize*0.7 - 80, Y: WorldSize - 160, W: 160, H: 60},
	}
	for b := range bars {
		barCenters[b] = Vec2{X: bars[b].X + bars[b].W*0.5, Y: bars[b].Y + bars[b].H*0.5}
	}
	buildBarJSONPrefixes()
	buildBarLUT()

	// Deux tranches contiguës allouées une seule fois (ordre des tirages RNG inchangé)
	clients = make([]Client, TotalClients)
	positions = make([]Vec2, TotalClients)
	for i := 0; i < TotalClients; i++ {
		c := &clients[i]
		c.ID = int64(i)
		// Répartition initiale pseudo-aléatoire dans le bar
		positions[i].X = 150.0 + simRng.Float64()*(WorldSize-300.0)
		positions[i].Y = 150.0 + simRng.Float64()*(WorldSize-300.0)
		c.Speed = DefaultClientSpeed * (0.85 + simRng.Float64()*0.4)
		c.Alcohol = simRng.Float64() * 0.15 // Commence sobre
		c.Stamina = MaxStamina - simRng.Float64()*10.0
		// Tolérance individuelle à l'alcool réaliste (entre 2.2 et 4.5 g/L)
		c.Tolerance = 2.2 + simRng.Float64()*2.3
		c.State = StateSober
		// Échelonnement initial pour éviter que tous se ruent en même temps à la seconde 0
		c.StateTimer = simRng.Intn(120)
		c.TargetBarID = simRng.Intn(len(bars))
		c.DrinksConsumed = 0
		c.WanderAngle = simRng.Float64() * 2 * math.Pi
	}
}

// -----------------------------------------------------------------------------
// Boucle de Mise à Jour par Tick (Volontairement Naïve & Pénalisante)
// -----------------------------------------------------------------------------

// updateSimulation exécute un cycle de calcul complet avec :
// 1. Allocations dynamiques de tranches intermédiaires à la volée (Heap pressure)
// 2. Calculs de distance bruts en boucle imbriquée (CPU stalls, Cache Misses)
// 3. Détection des flaques de vomi et perturbation des voisins
func updateSimulation() {
	start := time.Now()
	currentTick++

	// O1 : Expiration FIFO. Les flaques ont toutes la même durée de vie, donc les flaques
	// expirées forment toujours un préfixe : aucune décrémentation ni nouvelle tranche par tick.
	expired := 0
	for expired < len(vomitPuddles) && vomitPuddles[expired].ExpireTick <= currentTick {
		expired++
	}
	if expired > 0 {
		vomitPuddles = vomitPuddles[:copy(vomitPuddles, vomitPuddles[expired:])]
	}
	buildPuddleIndex()

	// O3 : Parcours séquentiel de deux tranches contiguës : données froides (clients)
	// et données chaudes (positions), lues dans l'ordre par le prefetcher matériel.
	for i := 0; i < len(clients); i++ {
		c := &clients[i]
		pos := &positions[i]

		// Élimination métabolique naturelle de l'alcool (~0.01 g/L par seconde)
		if c.Alcohol > 0 {
			c.Alcohol = math.Max(0, c.Alcohol-0.00035)
		}

		// ---------------------------------------------------------------------
		// ÉTAPE A : Évaluation des Flaques de Vomi (O2 : index CSR, voisinage 3 x 3)
		// ---------------------------------------------------------------------
		isTrappedInVomit, isSmellingVomit := false, false
		if len(vomitPuddles) > 0 { // Fast-path : aucun appel en début de soirée
			isTrappedInVomit, isSmellingVomit = probePuddles(pos.X, pos.Y)
		}

		// Effet du vomi sur le client
		if isTrappedInVomit {
			c.Stamina = math.Max(0, c.Stamina-1.0)
		} else if isSmellingVomit {
			c.Stamina = math.Max(0, c.Stamina-0.2)
			c.WanderAngle += (simRng.Float64() - 0.5) * 0.4
		}

		// ---------------------------------------------------------------------
		// Balayage d'un échantillon de voisins (Bousculade en O(N * K))
		// O3 : seules les positions (160 Ko, résidentes en L2) sont lues, et le rejet
		// AABB écarte les voisins lointains avant tout calcul de racine carrée.
		// ---------------------------------------------------------------------
		pushX, pushY := 0.0, 0.0
		scanStep := len(clients) / MaxNeighborsCheckNaive
		if scanStep < 1 {
			scanStep = 1
		}

		px, py := pos.X, pos.Y
		for nIdx := (i * 7) % scanStep; nIdx < len(positions); nIdx += scanStep {
			if nIdx == i {
				continue
			}
			other := &positions[nIdx]
			// Rejet AABB exact : |dx| >= 22 implique dx*dx >= 484 (arrondi monotone),
			// donc nDist >= 22 et le voisin aurait été écarté par le test ci-dessous.
			// math.Abs (masque de bit, sans branchement) est indispensable : la forme
			// « ndx >= 22 || ndx <= -22 » branche sur le signe de dx (~50 % de
			// mauvaises prédictions) et rend le tick 60 % plus lent (cf. bench/03_*).
			ndx := px - other.X
			if math.Abs(ndx) >= 22.0 {
				continue
			}
			ndy := py - other.Y
			if math.Abs(ndy) >= 22.0 {
				continue
			}
			nDist := math.Sqrt(ndx*ndx + ndy*ndy)

			if nDist > 0.001 && nDist < 22.0 {
				repelFactor := (22.0 - nDist) / 22.0
				pushX += (ndx / nDist) * repelFactor * 1.0
				pushY += (ndy / nDist) * repelFactor * 1.0
			}
		}

		// Amortissement de la poussée pour éviter les saccades excessives
		pushDist := math.Sqrt(pushX*pushX + pushY*pushY)
		if pushDist > c.Speed*1.0 {
			pushX = (pushX / pushDist) * c.Speed * 1.0
			pushY = (pushY / pushDist) * c.Speed * 1.0
		}

		// ---------------------------------------------------------------------
		// ÉTAPE B : Machine à États Finis (FSM) du Client
		// ---------------------------------------------------------------------
		switch c.State {
		case StateVomiting:
			// Le client est plié en deux au sol pendant son malaise
			c.StateTimer--
			c.Stamina = 0.0

			if c.StateTimer <= 0 {
				// Fin de la crise de vomi : le client reprend ses esprits
				c.State = StateSober
				c.StateTimer = 90 + simRng.Intn(60) // Pause repos/eau avant d'avoir à nouveau soif
				c.Alcohol *= 0.50                   // L'estomac est vidé, l'alcoolémie redescend
				c.Stamina = 40.0                    // Reprend son souffle
				c.WanderAngle = simRng.Float64() * 2 * math.Pi
				c.TargetBarID = simRng.Intn(len(bars))
			}

		case StateDrinking:
			// Le client a sa pinte en main : il se déplace et circule dans le bar en buvant !
			c.StateTimer--
			// Absorption progressive d'alcool tout au long de la pinte (+0.40 g/L au total)
			c.Alcohol += 0.0028

			// Récupération progressive d'endurance
			if c.Stamina < MaxStamina {
				c.Stamina += 0.3
			}

			// Déplacement nonchalant avec son verre dans la salle
			c.WanderAngle += (simRng.Float64() - 0.5) * 0.25
			moveSpeed := c.Speed * 0.65
			if isTrappedInVomit {
				moveSpeed *= 0.35
			}
			sinA, cosA := math.Sincos(c.WanderAngle) // O5 : une seule réduction d'argument
			vx := cosA * moveSpeed
			vy := sinA * moveSpeed

			pos.X += vx + pushX
			pos.Y += vy + pushY

			// Risque de vomir : se déclenche uniquement en cas de dépassement de tolérance
			if c.Alcohol > c.Tolerance {
				excessRatio := (c.Alcohol - c.Tolerance) / c.Tolerance
				// Probabilité modérée et progressive (ex: 0.8% à 1.5% par tick en état critique)
				vomitProb := math.Min(0.015, excessRatio*0.012)
				if simRng.Float64() < vomitProb {
					c.State = StateVomiting
					c.StateTimer = VomitDurationTicks
					c.Stamina = 0.0

					// Création d'une nouvelle mare de vomi, ajoutée par valeur dans la tranche préallouée
					puddleIdSeq++
					vomitPuddles = append(vomitPuddles, VomitPuddle{
						ID:         puddleIdSeq,
						X:          pos.X,
						Y:          pos.Y,
						Radius:     VomitRadius,
						ExpireTick: currentTick + int64(PuddleLifetimeTicks), // Reste au sol après le vomi
					})
					continue
				}
			}

			// Fin de la bière consommée
			if c.StateTimer <= 0 {
				c.State = StateSober
				c.StateTimer = 60 + simRng.Intn(80) // Période de discussion/pause avant la suivante
				c.TargetBarID = simRng.Intn(len(bars))
			}

		case StateSober:
			if c.Stamina < MaxStamina {
				c.Stamina += 0.3
			}

			// Si le client est en phase de transition / balade dans le bar
			if c.StateTimer > 0 {
				c.StateTimer--
				c.WanderAngle += (simRng.Float64() - 0.5) * 0.30
				moveSpeed := c.Speed * 0.85
				if isTrappedInVomit {
					moveSpeed *= 0.35
				}
				sinA, cosA := math.Sincos(c.WanderAngle) // O5 : une seule réduction d'argument
				vx := cosA * moveSpeed
				vy := sinA * moveSpeed
				pos.X += vx + pushX
				pos.Y += vy + pushY
			} else {
				// Le client a soif et se dirige activement vers un comptoir de bar !

				// O4 : Recherche du bar le plus proche sans allocation, sur les centres
				// précalculés (tableau de 128 o, 2 lignes de cache). La distance et le
				// vecteur du gagnant sont réutilisés : même expression flottante, une
				// racine carrée en moins.
				// O7 : table de Voronoï certifiée (bar_lut.go), repli sur le scan aux frontières.
				nearestBarIdx, distToBar, dirX, dirY := nearestBar(pos.X, pos.Y)

				targetBar := &bars[nearestBarIdx]
				targetCenterX := barCenters[nearestBarIdx].X
				targetCenterY := barCenters[nearestBarIdx].Y

				if distToBar <= ServingRadius {
					// Arrivée au comptoir : commande servie !
					c.State = StateDrinking
					c.StateTimer = DrinkDurationTicks
					c.DrinksConsumed++
					targetBar.BeersServed++
					totalBeersCount++

					// Le client s'oriente vers la salle pour s'éloigner du comptoir en buvant
					c.WanderAngle = math.Atan2(pos.Y-targetCenterY, pos.X-targetCenterX) + (simRng.Float64()-0.5)*0.8
				} else {
					effSpeed := c.Speed
					if isTrappedInVomit {
						effSpeed *= 0.35
					} else if isSmellingVomit {
						effSpeed *= 0.75
					}

					vx := (dirX / distToBar) * effSpeed
					vy := (dirY / distToBar) * effSpeed

					pos.X += vx + pushX
					pos.Y += vy + pushY
				}
			}
		}

		// Maintien dans les limites du bar (murs infranchissables)
		if pos.X < 50.0 {
			pos.X = 50.0
		} else if pos.X > WorldSize-50.0 {
			pos.X = WorldSize - 50.0
		}
		if pos.Y < 50.0 {
			pos.Y = 50.0
		} else if pos.Y > WorldSize-50.0 {
			pos.Y = WorldSize - 50.0
		}
	}

	lastTickTimeMs = float64(time.Since(start).Microseconds()) / 1000.0
}

// -----------------------------------------------------------------------------
// Statistiques & Métriques d'Exécution
// -----------------------------------------------------------------------------

func getSimulationStats(currentTPS int) SimStats {
	metrics.Read(memSamples) // O6 : sans pause Stop-The-World, contrairement à runtime.ReadMemStats
	heapAlloc := memSamples[0].Value.Uint64()
	totalAlloc := memSamples[1].Value.Uint64()
	numGC := memSamples[2].Value.Uint64()

	soberCount, drinkingCount, vomitingCount := 0, 0, 0
	totalAlcohol := 0.0

	for i := range clients {
		c := &clients[i]
		switch c.State {
		case StateSober:
			soberCount++
		case StateDrinking:
			drinkingCount++
		case StateVomiting:
			vomitingCount++
		}
		totalAlcohol += c.Alcohol
	}

	avgAlcohol := 0.0
	if len(clients) > 0 {
		avgAlcohol = totalAlcohol / float64(len(clients))
	}

	return SimStats{
		Seed:           MasterSeed,
		Tick:           currentTick,
		TPS:            currentTPS,
		TickDurationMs: math.Round(lastTickTimeMs*100) / 100,
		TotalClients:   len(clients),
		SoberCount:     soberCount,
		DrinkingCount:  drinkingCount,
		VomitingCount:  vomitingCount,
		BeersServed:    totalBeersCount,
		ActivePuddles:  len(vomitPuddles),
		AvgAlcohol:     math.Round(avgAlcohol*100) / 100,
		HeapAllocMB:    math.Round((float64(heapAlloc)/(1024*1024))*100) / 100,
		TotalAllocMB:   math.Round((float64(totalAlloc)/(1024*1024))*100) / 100,
		NumGC:          uint32(numGC),
	}
}

// -----------------------------------------------------------------------------
// Serveur WebSocket & Télémétrie en Temps Réel
// -----------------------------------------------------------------------------

func wsHandler(ws *websocket.Conn) {
	defer ws.Close()
	log.Printf("[WebSocket] Nouvelle connexion client établie.")

	initSimulation(MasterSeed)

	ws.PayloadType = websocket.TextFrame
	frameBuf := make([]byte, 0, FrameBufferCapacity)

	ticker := time.NewTicker(time.Second / time.Duration(TargetTPS))
	defer ticker.Stop()

	ticksInSecond := 0
	lastTPSTime := time.Now()
	liveTPS := TargetTPS

	for range ticker.C {
		updateSimulation()

		ticksInSecond++
		if time.Since(lastTPSTime) >= time.Second {
			liveTPS = ticksInSecond
			measuredTPS = liveTPS
			ticksInSecond = 0
			lastTPSTime = time.Now()

			stats := getSimulationStats(liveTPS)
			log.Printf("[BarMash] Tick #%05d | TPS: %2d | Durée: %5.2f ms | RAM Heap: %6.2f MB | GC: %3d | Sobres: %5d | Buveurs: %5d | Vomisseurs: %4d | Flaques: %4d | Bières: %6d",
				stats.Tick, stats.TPS, stats.TickDurationMs, stats.HeapAllocMB, stats.NumGC,
				stats.SoberCount, stats.DrinkingCount, stats.VomitingCount, stats.ActivePuddles, stats.BeersServed)
		}

		// O6 : encodage append-only dans le buffer de la connexion, puis écriture directe
		// de la frame texte (plus de conversion string(data) recopiant ~360 Ko).
		stats := getSimulationStats(liveTPS)
		frameBuf = appendFrame(frameBuf[:0], &stats)
		if _, err := ws.Write(frameBuf); err != nil {
			log.Printf("[WebSocket] Déconnexion du client web.")
			break
		}
	}
}

// AdminAddr expose pprof sur la boucle locale uniquement, isolé du port public.
const AdminAddr = "localhost:6060"

// startAdmin démarre le serveur d'administration pprof sur un mux dédié :
// l'application publique n'utilise pas http.DefaultServeMux, où net/http/pprof s'enregistre.
func startAdmin() {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	go func() {
		log.Printf("[Admin] pprof indisponible : %v", http.ListenAndServe(AdminAddr, mux))
	}()
}

// -----------------------------------------------------------------------------
// Point d'Entrée Principal (Serveur HTTP + Boucle Autonome)
// -----------------------------------------------------------------------------

func main() {
	initSimulation(MasterSeed)

	startAdmin()

	appMux := http.NewServeMux()
	appMux.Handle("/", http.FileServer(http.Dir(".")))
	appMux.Handle("/ws", websocket.Handler(wsHandler))

	port := 8080
	fmt.Println("================================================================================")
	fmt.Println("   🍺 BARMASH : LA TOURNÉE INFERNALE • Moteur Naïf & Non-Optimisé")
	fmt.Println("   Simulation Haute Charge : 10 000 Clients Autonomes en Bar")
	fmt.Println("================================================================================")
	fmt.Printf("   Interface Web & Télémétrie : http://localhost:%d\n", port)
	fmt.Printf("   Profiling pprof (local)    : http://%s/debug/pprof/\n", AdminAddr)
	fmt.Println("   Graine fixe (MasterSeed)   : 42")
	fmt.Println("   Fréquence cible (TargetTPS): 30 TPS")
	fmt.Println("================================================================================")
	fmt.Println("Appuyez sur Ctrl+C pour arrêter le serveur.")

	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", port), appMux))
}
