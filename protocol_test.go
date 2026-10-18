package main

import (
	"hash/fnv"
	"math"
	"math/rand"
	"testing"
)

// -----------------------------------------------------------------------------
// Protocole de Mesure Déterministe
//
// 1. Checksum de référence : l'état complet de la simulation après GoldenTicks
//    ticks (MasterSeed = 42) est haché bit à bit. Toute optimisation doit
//    reproduire exactement ce hash : aucune régression logique n'est tolérée.
// 2. Scénarios de charge : la charge d'un tick dépend fortement de la phase de
//    la soirée (aucune flaque avant ~2 500 ticks, ~450 flaques à 5 000 ticks).
//    Chaque benchmark repart d'un snapshot figé pour mesurer une charge stable.
// -----------------------------------------------------------------------------

const (
	// GoldenTicks couvre l'apparition des flaques (> 2 500 ticks) pour valider toutes les branches.
	GoldenTicks = 4000
	// GoldenChecksum est l'empreinte FNV-1a de l'état produit par la version naïve (commit de référence).
	GoldenChecksum uint64 = 0xaa36b4f0b73c614c

	// RushWarmupTicks place la simulation en régime de croisière (~450 flaques actives).
	RushWarmupTicks = 5000
	// TicksPerRestore borne la dérive de l'état mesuré : on restaure le snapshot tous les 100 ticks.
	TicksPerRestore = 100
)

// stateChecksum hache tous les champs observables de la simulation, y compris l'état du RNG.
func stateChecksum() uint64 {
	h := fnv.New64a()
	var buf [8]byte
	put := func(v uint64) {
		for i := 0; i < 8; i++ {
			buf[i] = byte(v >> (8 * i))
		}
		h.Write(buf[:])
	}
	putF := func(f float64) { put(math.Float64bits(f)) }

	for i := range clients {
		c := &clients[i]
		putF(positions[i].X)
		putF(positions[i].Y)
		putF(c.Speed)
		putF(c.Alcohol)
		putF(c.Stamina)
		putF(c.Tolerance)
		putF(c.WanderAngle)
		put(uint64(c.State))
		put(uint64(c.StateTimer))
		put(uint64(c.TargetBarID))
		put(uint64(c.DrinksConsumed))
	}
	for _, p := range vomitPuddles {
		put(uint64(p.ID))
		putF(p.X)
		putF(p.Y)
		putF(p.Radius)
		put(uint64(p.ExpireTick - currentTick)) // Équivalent observable de l'ancien RemainingTicks
	}
	for _, b := range bars {
		put(uint64(b.BeersServed))
	}
	put(uint64(totalBeersCount))
	put(uint64(puddleIdSeq))
	put(uint64(currentTick))
	put(uint64(simRng.Int63())) // Vérifie que la séquence de tirages aléatoires est identique
	return h.Sum64()
}

// TestGoldenChecksum garantit que l'optimisation ne modifie pas le comportement de la simulation.
func TestGoldenChecksum(t *testing.T) {
	initSimulation(MasterSeed)
	for i := 0; i < GoldenTicks; i++ {
		updateSimulation()
	}
	got := stateChecksum()
	t.Logf("Checksum après %d ticks : %#016x (flaques actives : %d, bières : %d)", GoldenTicks, got, len(vomitPuddles), totalBeersCount)
	if GoldenChecksum != 0 && got != GoldenChecksum {
		t.Fatalf("RÉGRESSION LOGIQUE : checksum %#016x, attendu %#016x", got, GoldenChecksum)
	}
}

// -----------------------------------------------------------------------------
// Snapshots de Scénarios (dépendants de la disposition mémoire du moteur)
// -----------------------------------------------------------------------------

type simSnapshot struct {
	tick        int64
	clients     []Client
	positions   []Vec2
	puddles     []VomitPuddle
	bars        []BarCounter
	puddleIdSeq int64
	beers       int
}

func takeSnapshot() *simSnapshot {
	s := &simSnapshot{
		tick:        currentTick,
		clients:     append([]Client(nil), clients...),
		positions:   append([]Vec2(nil), positions...),
		puddles:     append([]VomitPuddle(nil), vomitPuddles...),
		bars:        append([]BarCounter(nil), bars...),
		puddleIdSeq: puddleIdSeq,
		beers:       totalBeersCount,
	}
	return s
}

// restore réinjecte le snapshot ; le RNG est réensemencé de façon déterministe.
func (s *simSnapshot) restore() {
	copy(clients, s.clients)
	copy(positions, s.positions)
	vomitPuddles = append(vomitPuddles[:0], s.puddles...)
	copy(bars, s.bars)
	currentTick = s.tick
	puddleIdSeq = s.puddleIdSeq
	totalBeersCount = s.beers
	simRng = rand.New(rand.NewSource(MasterSeed + s.tick))
}

var snapshotCache = map[int]*simSnapshot{}

// scenario renvoie (et met en cache) le snapshot de la simulation après warmup ticks.
func scenario(warmup int) *simSnapshot {
	if s, ok := snapshotCache[warmup]; ok {
		return s
	}
	initSimulation(MasterSeed)
	for i := 0; i < warmup; i++ {
		updateSimulation()
	}
	s := takeSnapshot()
	snapshotCache[warmup] = s
	return s
}

var benchScenarios = []struct {
	name   string
	warmup int
}{
	{"phase=early", 0},              // Début de soirée : aucune flaque
	{"phase=rush", RushWarmupTicks}, // Régime de croisière : ~450 flaques actives
}

// BenchmarkUpdateSimulation mesure le coût d'UN tick (ns/op = temps par tick).
func BenchmarkUpdateSimulation(b *testing.B) {
	for _, sc := range benchScenarios {
		b.Run(sc.name, func(b *testing.B) {
			snap := scenario(sc.warmup)
			snap.restore()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if i%TicksPerRestore == 0 {
					b.StopTimer()
					snap.restore()
					b.StartTimer()
				}
				updateSimulation()
			}
		})
	}
}

// BenchmarkBuildFrame mesure la préparation + l'encodage d'une frame réseau en régime de croisière.
func BenchmarkBuildFrame(b *testing.B) {
	snap := scenario(RushWarmupTicks)
	snap.restore()
	b.ReportAllocs()
	b.ResetTimer()
	var size int
	for i := 0; i < b.N; i++ {
		data, err := buildFrame(TargetTPS)
		if err != nil {
			b.Fatal(err)
		}
		size = len(data)
	}
	b.ReportMetric(float64(size), "bytes/frame")
}
