package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"math/rand"
	"net/http"
	"net/http/pprof"
	"runtime"
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

// Client modélise un fêtard dans le bar.
// ANTI-PATTERN ÉDUCATIF : Les instances sont dispersées sur le Tas (Heap)
// et accédées via des pointeurs []*Client, provoquant de nombreux sauts mémoire (Cache Misses).
type Client struct {
	ID             int64
	X, Y           float64
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
type VomitPuddle struct {
	ID             int64   `json:"id"`
	X              float64 `json:"x"`
	Y              float64 `json:"y"`
	Radius         float64 `json:"radius"`
	RemainingTicks int     `json:"remaining_ticks"` // Calculé à l'encodage : ExpireTick - currentTick
	ExpireTick     int64   `json:"-"`               // Tick de disparition (horodatage absolu)
}

// PuddleLifetimeTicks est identique pour toutes les flaques : elles expirent dans leur ordre de création (FIFO).
const PuddleLifetimeTicks = VomitDurationTicks * 2

// MaxPuddlesCapacity dimensionne la tranche des flaques une fois pour toutes (~460 flaques en croisière).
const MaxPuddlesCapacity = 2048

// BarDistance structure temporaire allouée dynamiquement pour chaque calcul de distance
type BarDistance struct {
	BarID    int
	Distance float64
}

// -----------------------------------------------------------------------------
// Télémétrie & Payloads WebSocket
// -----------------------------------------------------------------------------

// ClientPoint représente la position et l'état compact pour le streaming Canvas
type ClientPoint struct {
	X       int16 `json:"x"`
	Y       int16 `json:"y"`
	State   int8  `json:"s"` // 0: Sober, 1: Drinking, 2: Vomiting
	Alcohol uint8 `json:"a"` // Alcoolémie mise à l'échelle (0-255)
}

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

// FramePayload envoyé à l'interface Web Canvas à chaque tick
type FramePayload struct {
	Stats        SimStats      `json:"stats"`
	WorldSize    float64       `json:"world_size"`
	Bars         []BarCounter  `json:"bars"`
	VomitPuddles []VomitPuddle `json:"puddles"`
	Clients      []ClientPoint `json:"clients"`
}

// -----------------------------------------------------------------------------
// État Global de la Simulation (Moteur Naïf)
// -----------------------------------------------------------------------------

var (
	simRng          *rand.Rand
	clients         []*Client // Tranche de pointeurs dispersés (Heap Churn)
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

	// Instanciation naïve des 10 000 clients individuellement sur le Tas
	clients = make([]*Client, TotalClients)
	for i := 0; i < TotalClients; i++ {
		c := new(Client)
		c.ID = int64(i)
		// Répartition initiale pseudo-aléatoire dans le bar
		c.X = 150.0 + simRng.Float64()*(WorldSize-300.0)
		c.Y = 150.0 + simRng.Float64()*(WorldSize-300.0)
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

		clients[i] = c
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

	// ANTI-PATTERN 2 : Parcours de 10 000 pointeurs dispersés []*Client
	// Le processeur ne peut pas anticiper les adresses des structures Client (Prefetcher inefficace).
	for i := 0; i < len(clients); i++ {
		c := clients[i]

		// Élimination métabolique naturelle de l'alcool (~0.01 g/L par seconde)
		if c.Alcohol > 0 {
			c.Alcohol = math.Max(0, c.Alcohol-0.00035)
		}

		// ---------------------------------------------------------------------
		// ÉTAPE A : Évaluation des Flaques de Vomi (O2 : index CSR, voisinage 3 x 3)
		// ---------------------------------------------------------------------
		isTrappedInVomit, isSmellingVomit := false, false
		if len(vomitPuddles) > 0 { // Fast-path : aucun appel en début de soirée
			isTrappedInVomit, isSmellingVomit = probePuddles(c.X, c.Y)
		}

		// Effet du vomi sur le client
		if isTrappedInVomit {
			c.Stamina = math.Max(0, c.Stamina-1.0)
		} else if isSmellingVomit {
			c.Stamina = math.Max(0, c.Stamina-0.2)
			c.WanderAngle += (simRng.Float64() - 0.5) * 0.4
		}

		// ---------------------------------------------------------------------
		// ANTI-PATTERN 4 : Balayage Brut de Voisins (Bousculade en O(N * K))
		// Chaque client parcourt un échantillon d'autres clients pour évaluer la densité de foule
		// Boucle imbriquée pénalisante qui détruit les caches de données CPU (L1/L2).
		// ---------------------------------------------------------------------
		pushX, pushY := 0.0, 0.0
		scanStep := len(clients) / MaxNeighborsCheckNaive
		if scanStep < 1 {
			scanStep = 1
		}

		// Allocation inutile d'une tranche de voisinage (Heap Escape)
		neighborsCrowd := make([]float64, 0, 8)

		for nIdx := (i * 7) % scanStep; nIdx < len(clients); nIdx += scanStep {
			if nIdx == i {
				continue
			}
			other := clients[nIdx]
			ndx := c.X - other.X
			ndy := c.Y - other.Y
			nDist := math.Sqrt(ndx*ndx + ndy*ndy)

			if nDist > 0.001 && nDist < 22.0 {
				repelFactor := (22.0 - nDist) / 22.0
				pushX += (ndx / nDist) * repelFactor * 1.0
				pushY += (ndy / nDist) * repelFactor * 1.0
				neighborsCrowd = append(neighborsCrowd, nDist)
			}
		}
		_ = len(neighborsCrowd) // Empêche l'élimination par le compilateur

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
			vx := math.Cos(c.WanderAngle) * moveSpeed
			vy := math.Sin(c.WanderAngle) * moveSpeed

			c.X += vx + pushX
			c.Y += vy + pushY

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
						X:          c.X,
						Y:          c.Y,
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
				vx := math.Cos(c.WanderAngle) * moveSpeed
				vy := math.Sin(c.WanderAngle) * moveSpeed
				c.X += vx + pushX
				c.Y += vy + pushY
			} else {
				// Le client a soif et se dirige activement vers un comptoir de bar !

				// ANTI-PATTERN 3 : Recherche Naïve du Bar le Plus Proche avec make()
				// Création d'une tranche temporaire de distances sur le tas à chaque tick
				barDistances := make([]BarDistance, len(bars))
				nearestBarIdx := 0
				minDist := math.MaxFloat64

				for bIdx := 0; bIdx < len(bars); bIdx++ {
					bx := bars[bIdx].X + bars[bIdx].W*0.5
					by := bars[bIdx].Y + bars[bIdx].H*0.5
					dx := bx - c.X
					dy := by - c.Y
					d := math.Sqrt(dx*dx + dy*dy)

					barDistances[bIdx] = BarDistance{
						BarID:    bIdx,
						Distance: d,
					}

					if d < minDist {
						minDist = d
						nearestBarIdx = bIdx
					}
				}

				targetBar := &bars[nearestBarIdx]
				targetCenterX := targetBar.X + targetBar.W*0.5
				targetCenterY := targetBar.Y + targetBar.H*0.5

				dirX := targetCenterX - c.X
				dirY := targetCenterY - c.Y
				distToBar := math.Sqrt(dirX*dirX + dirY*dirY)

				if distToBar <= ServingRadius {
					// Arrivée au comptoir : commande servie !
					c.State = StateDrinking
					c.StateTimer = DrinkDurationTicks
					c.DrinksConsumed++
					targetBar.BeersServed++
					totalBeersCount++

					// Le client s'oriente vers la salle pour s'éloigner du comptoir en buvant
					c.WanderAngle = math.Atan2(c.Y-targetCenterY, c.X-targetCenterX) + (simRng.Float64()-0.5)*0.8
				} else {
					effSpeed := c.Speed
					if isTrappedInVomit {
						effSpeed *= 0.35
					} else if isSmellingVomit {
						effSpeed *= 0.75
					}

					vx := (dirX / distToBar) * effSpeed
					vy := (dirY / distToBar) * effSpeed

					c.X += vx + pushX
					c.Y += vy + pushY
				}
			}
		}

		// Maintien dans les limites du bar (murs infranchissables)
		if c.X < 50.0 {
			c.X = 50.0
		} else if c.X > WorldSize-50.0 {
			c.X = WorldSize - 50.0
		}
		if c.Y < 50.0 {
			c.Y = 50.0
		} else if c.Y > WorldSize-50.0 {
			c.Y = WorldSize - 50.0
		}
	}

	lastTickTimeMs = float64(time.Since(start).Microseconds()) / 1000.0
}

// -----------------------------------------------------------------------------
// Statistiques & Métriques d'Exécution
// -----------------------------------------------------------------------------

func getSimulationStats(currentTPS int) SimStats {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	soberCount, drinkingCount, vomitingCount := 0, 0, 0
	totalAlcohol := 0.0

	for _, c := range clients {
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
		HeapAllocMB:    math.Round((float64(m.HeapAlloc)/(1024*1024))*100) / 100,
		TotalAllocMB:   math.Round((float64(m.TotalAlloc)/(1024*1024))*100) / 100,
		NumGC:          m.NumGC,
	}
}

// buildFrame prépare et encode la charge utile envoyée au Canvas Web pour un tick.
func buildFrame(liveTPS int) ([]byte, error) {
	stats := getSimulationStats(liveTPS)
	clientPoints := make([]ClientPoint, len(clients))
	for idx, c := range clients {
		scaledAlc := uint8(math.Min(255, c.Alcohol*50.0))
		clientPoints[idx] = ClientPoint{
			X:       int16(c.X),
			Y:       int16(c.Y),
			State:   int8(c.State),
			Alcohol: scaledAlc,
		}
	}

	for i := range vomitPuddles {
		vomitPuddles[i].RemainingTicks = int(vomitPuddles[i].ExpireTick - currentTick)
	}

	payload := FramePayload{
		Stats:        stats,
		WorldSize:    WorldSize,
		Bars:         bars,
		VomitPuddles: vomitPuddles,
		Clients:      clientPoints,
	}

	return json.Marshal(payload)
}

// -----------------------------------------------------------------------------
// Serveur WebSocket & Télémétrie en Temps Réel
// -----------------------------------------------------------------------------

func wsHandler(ws *websocket.Conn) {
	defer ws.Close()
	log.Printf("[WebSocket] Nouvelle connexion client établie.")

	initSimulation(MasterSeed)

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

		data, err := buildFrame(liveTPS)
		if err != nil {
			log.Printf("[WebSocket] Erreur d'encodage JSON: %v", err)
			break
		}

		if err := websocket.Message.Send(ws, string(data)); err != nil {
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
