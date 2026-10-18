package main

import (
	"fmt"
	"log"
	"net/http"
	"net/http/pprof"
	"time"

	"golang.org/x/net/websocket"
)

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
	appMux.Handle("/", http.FileServer(http.Dir("web")))
	appMux.Handle("/ws", websocket.Handler(wsHandler))

	port := 8080
	fmt.Println("================================================================================")
	fmt.Println("   🍺 BARMASH : LA TOURNÉE INFERNALE • Moteur Optimisé (0 alloc/tick)")
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
