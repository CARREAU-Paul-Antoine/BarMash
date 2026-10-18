package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"sync"
	"time"

	"golang.org/x/net/websocket"
)

// -----------------------------------------------------------------------------
// Serveur WebSocket & Télémétrie en Temps Réel
// -----------------------------------------------------------------------------

// frameBufPool recycle les buffers de frame (512 Ko) d'une connexion à l'autre :
// un navigateur qui se reconnecte ne réalloue pas son buffer.
var frameBufPool = sync.Pool{New: func() any { b := make([]byte, 0, FrameBufferCapacity); return &b }}

var store *TelemetryStore // nil si la base n'a pas pu être ouverte : la simulation tourne sans historique

func wsHandler(ws *websocket.Conn) {
	defer ws.Close()
	jsonFormat := ws.Request().URL.Query().Get("format") == "json" // Ancien format (O6), pour compatibilité
	log.Printf("[WebSocket] Nouvelle connexion client établie (format %s).", map[bool]string{true: "json", false: "binaire"}[jsonFormat])

	initSimulation(MasterSeed)
	session := time.Now().UnixMilli()

	// O9 : arrêt précoce. Une goroutine lit la socket et annule le contexte dès que le
	// navigateur ferme la connexion : la boucle s'arrête sans attendre l'échec d'une écriture.
	ctx, cancel := context.WithCancel(ws.Request().Context())
	defer cancel()
	go func() {
		var discard [512]byte
		for {
			if _, err := ws.Read(discard[:]); err != nil {
				cancel()
				return
			}
		}
	}()

	if !jsonFormat {
		ws.PayloadType = websocket.TextFrame
		if _, err := ws.Write(initMessageJSON(session)); err != nil {
			return
		}
		ws.PayloadType = websocket.BinaryFrame
	} else {
		ws.PayloadType = websocket.TextFrame
	}

	bp := frameBufPool.Get().(*[]byte)
	defer frameBufPool.Put(bp)

	ticker := time.NewTicker(time.Second / time.Duration(TargetTPS))
	defer ticker.Stop()

	ticksInSecond := 0
	lastTPSTime := time.Now()
	liveTPS := TargetTPS

	for {
		select {
		case <-ctx.Done():
			log.Printf("[WebSocket] Déconnexion du client web (session %d).", session)
			return
		case <-ticker.C:
		}
		updateSimulation()

		ticksInSecond++
		if time.Since(lastTPSTime) >= time.Second {
			liveTPS = ticksInSecond
			measuredTPS = liveTPS
			ticksInSecond = 0
			lastTPSTime = time.Now()

			stats := getSimulationStats(liveTPS)
			if store != nil {
				store.Record(telemetryFromStats(session, &stats)) // Non bloquant (O11)
			}
			log.Printf("[BarMash] Tick #%05d | TPS: %2d | Durée: %5.2f ms | RAM Heap: %6.2f MB | GC: %3d | Sobres: %5d | Buveurs: %5d | Vomisseurs: %4d | Flaques: %4d | Bières: %6d",
				stats.Tick, stats.TPS, stats.TickDurationMs, stats.HeapAllocMB, stats.NumGC,
				stats.SoberCount, stats.DrinkingCount, stats.VomitingCount, stats.ActivePuddles, stats.BeersServed)
		}

		// O6 / O10 : encodage dans le buffer recyclé puis écriture directe de la frame.
		stats := getSimulationStats(liveTPS)
		if jsonFormat {
			*bp = appendFrame((*bp)[:0], &stats)
		} else {
			*bp = appendBinaryFrame((*bp)[:0], &stats)
		}
		if _, err := ws.Write(*bp); err != nil {
			log.Printf("[WebSocket] Déconnexion du client web (session %d).", session)
			return
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

	// Ctrl+C annule le contexte : le serveur s'arrête proprement et l'historique en attente est écrit.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// BARMASH_DB / BARMASH_DB_NOINDEX servent aux tests de charge (base pré-remplie, avec ou sans index).
	dbPath := DBPath
	if p := os.Getenv("BARMASH_DB"); p != "" {
		dbPath = p
	}
	if err := os.MkdirAll("data", 0o755); err == nil {
		if s, err := openTelemetryStore(dbPath, os.Getenv("BARMASH_DB_NOINDEX") == ""); err == nil {
			store = s
			store.Start(ctx)
		} else {
			log.Printf("[SQLite] historique désactivé : %v", err)
		}
	}

	startAdmin()

	appMux := http.NewServeMux()
	appMux.Handle("/", http.FileServer(http.Dir("web")))
	appMux.Handle("/ws", websocket.Handler(wsHandler))
	if store != nil {
		appMux.Handle("/api/history", store.historyHandler(true))
	}

	port := 8080
	srv := &http.Server{Addr: fmt.Sprintf(":%d", port), Handler: appMux, BaseContext: func(_ net.Listener) context.Context { return ctx }}

	fmt.Println("================================================================================")
	fmt.Println("   🍺 BARMASH : LA TOURNÉE INFERNALE • Moteur Optimisé (0 alloc/tick)")
	fmt.Println("   Simulation Haute Charge : 10 000 Clients Autonomes en Bar")
	fmt.Println("================================================================================")
	fmt.Printf("   Interface Web & Télémétrie : http://localhost:%d\n", port)
	fmt.Printf("   Historique SQLite          : http://localhost:%d/api/history?session=..&from=0&to=100000\n", port)
	fmt.Printf("   Profiling pprof (local)    : http://%s/debug/pprof/\n", AdminAddr)
	fmt.Printf("   Workers (phase parallèle)  : %d\n", defaultWorkers())
	fmt.Println("   Graine fixe (MasterSeed)   : 42")
	fmt.Println("   Fréquence cible (TargetTPS): 30 TPS")
	fmt.Println("================================================================================")
	fmt.Println("Appuyez sur Ctrl+C pour arrêter le serveur.")

	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
	}()
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
	if store != nil {
		store.Close() // Attend que la goroutine d'écriture ait vidé son lot
	}
}
