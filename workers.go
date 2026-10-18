package main

import (
	"math"
	"os"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
)

// -----------------------------------------------------------------------------
// O9 : Tick en Deux Phases sur un Worker Pool Borné
//
// Phase 1 (parallèle) : pour chaque client, poussée de bousculade et contact avec
// les flaques, calculés uniquement à partir de l'état du DÉBUT de tick (lecture seule).
// Phase 2 (séquentielle) : machine à états, tirages RNG, déplacements, nouvelles flaques,
// dans l'ordre des clients. Les tirages RNG restent dans le même ordre : le résultat est
// identique quel que soit le nombre de workers (TestWorkerCountInvariance).
//
// Distribution : tranches de ForceChunkSize clients attribuées par un compteur atomique
// (pas de verrou, équilibrage dynamique). Les tranches écrivent des zones disjointes de
// forces[] et vomitFlags[] alignées sur 64 o : pas de faux partage entre cœurs.
// -----------------------------------------------------------------------------

// ForceChunkSize : 256 clients = 4 Ko de forces[] et 256 o de vomitFlags[] par tâche.
const ForceChunkSize = 256

const (
	flagTrapped  uint8 = 1 << 0
	flagSmelling uint8 = 1 << 1
)

var (
	forces     []Vec2  // Poussée de bousculade du client i (phase 1)
	vomitFlags []uint8 // Contact du client i avec les flaques présentes en début de tick (phase 1)
	pool       *forcePool
)

type forcePool struct {
	workers int
	start   chan struct{}
	done    sync.WaitGroup
	next    atomic.Int32
	chunks  int32
}

// defaultWorkers dimensionne le pool sur les cœurs physiques : les 2 threads SMT d'un
// cœur partagent ses unités flottantes, saturées par le scan (cf. rapport, section 3).
func defaultWorkers() int {
	if n, err := strconv.Atoi(os.Getenv("BARMASH_WORKERS")); err == nil && n > 0 {
		return n
	}
	return max(1, runtime.NumCPU()/2)
}

// setWorkerCount remplace le pool courant ; les anciens workers se terminent.
func setWorkerCount(n int) {
	if pool != nil {
		close(pool.start)
	}
	pool = &forcePool{workers: n, start: make(chan struct{}, n)}
	if n > 1 {
		for w := 0; w < n; w++ {
			go pool.worker()
		}
	}
}

func (p *forcePool) worker() {
	for range p.start {
		p.drain()
		p.done.Done()
	}
}

func (p *forcePool) drain() {
	for {
		c := p.next.Add(1) - 1
		if c >= p.chunks {
			return
		}
		lo := int(c) * ForceChunkSize
		hi := min(lo+ForceChunkSize, len(positions))
		for i := lo; i < hi; i++ {
			computeForces(i)
		}
	}
}

// run exécute la phase 1 sur tous les clients et rend la main quand elle est terminée.
func (p *forcePool) run() {
	p.chunks = int32((len(positions) + ForceChunkSize - 1) / ForceChunkSize)
	p.next.Store(0)
	if p.workers <= 1 {
		p.drain() // Mode séquentiel : aucune goroutine ni synchronisation
		return
	}
	p.done.Add(p.workers)
	for w := 0; w < p.workers; w++ {
		p.start <- struct{}{}
	}
	p.done.Wait()
}

// computeForces est la phase 1 pour le client i : elle ne lit que l'état de début de tick
// et n'écrit que forces[i] et vomitFlags[i].
func computeForces(i int) {
	px, py := positions[i].X, positions[i].Y

	var flags uint8
	if puddleIndexed > 0 {
		trapped, smelling := probeIndexedPuddles(px, py)
		if trapped {
			flags |= flagTrapped
		}
		if smelling {
			flags |= flagSmelling
		}
	}
	vomitFlags[i] = flags

	// Bousculade sur un échantillon fixe de 60 voisins (positions de début de tick).
	pushX, pushY := 0.0, 0.0
	scanStep := max(len(positions)/MaxNeighborsCheckNaive, 1)
	for nIdx := (i * 7) % scanStep; nIdx < len(positions); nIdx += scanStep {
		if nIdx == i {
			continue
		}
		other := &positions[nIdx]
		// Rejet AABB exact : |dx| >= 22 implique dx*dx >= 484 (arrondi monotone),
		// donc nDist >= 22 et le voisin aurait été écarté par le test ci-dessous.
		// math.Abs (masque de bit, sans branchement) est indispensable : la forme
		// « ndx >= 22 || ndx <= -22 » branche sur le signe de dx (~50 % de
		// mauvaises prédictions) et rend le tick 60 % plus lent (cf. perf/bench/03_*).
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
	speed := clients[i].Speed
	if pushDist := math.Sqrt(pushX*pushX + pushY*pushY); pushDist > speed*1.0 {
		pushX = (pushX / pushDist) * speed * 1.0
		pushY = (pushY / pushDist) * speed * 1.0
	}
	forces[i] = Vec2{pushX, pushY}
}
