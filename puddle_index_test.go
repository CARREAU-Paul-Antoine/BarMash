package main

import (
	"math"
	"math/rand"
	"testing"
)

// bruteForceProbe est la recherche linéaire de la version naïve, servant d'oracle.
func bruteForceProbe(x, y float64) (trapped, smelling bool) {
	for i := range vomitPuddles {
		vp := &vomitPuddles[i]
		dx := x - vp.X
		dy := y - vp.Y
		dist := math.Sqrt(dx*dx + dy*dy)
		if dist <= vp.Radius {
			return true, smelling
		} else if dist <= VomitPerturbRadius {
			smelling = true
		}
	}
	return false, smelling
}

// TestPuddleIndexMatchesBruteForce compare l'index CSR à l'oracle, y compris aux bords
// du monde, aux frontières de cellules et pour des flaques ajoutées après l'indexation.
func TestPuddleIndexMatchesBruteForce(t *testing.T) {
	saved := vomitPuddles
	defer func() { vomitPuddles = saved; buildPuddleIndex() }()

	rng := rand.New(rand.NewSource(7))
	vomitPuddles = make([]VomitPuddle, 0, MaxPuddlesCapacity)
	for i := 0; i < 600; i++ {
		vomitPuddles = append(vomitPuddles, VomitPuddle{X: rng.Float64() * WorldSize, Y: rng.Float64() * WorldSize, Radius: VomitRadius})
	}
	// Flaques aux coins et sur les frontières exactes de cellules
	for _, v := range []float64{0, PuddleCellSize, 2 * PuddleCellSize, WorldSize/2 - 0.0001, WorldSize - 0.0001} {
		vomitPuddles = append(vomitPuddles, VomitPuddle{X: v, Y: v, Radius: VomitRadius})
	}
	buildPuddleIndex()
	// Flaques créées « pendant le tick », hors index
	for i := 0; i < 5; i++ {
		vomitPuddles = append(vomitPuddles, VomitPuddle{X: rng.Float64() * WorldSize, Y: rng.Float64() * WorldSize, Radius: VomitRadius})
	}

	check := func(x, y float64) {
		gt, gs := probePuddles(x, y)
		wt, ws := bruteForceProbe(x, y)
		if gt != wt || (!wt && gs != ws) {
			t.Fatalf("(%.6f, %.6f) : index=(%v,%v) oracle=(%v,%v)", x, y, gt, gs, wt, ws)
		}
	}
	for i := 0; i < 200000; i++ {
		check(rng.Float64()*(WorldSize+40)-20, rng.Float64()*(WorldSize+40)-20)
	}
	// Points exactement à VomitRadius et VomitPerturbRadius d'une flaque
	for i := range vomitPuddles[:50] {
		p := vomitPuddles[i]
		for _, r := range []float64{VomitRadius, VomitPerturbRadius, VomitPerturbRadius + 1e-9} {
			check(p.X+r, p.Y)
			check(p.X, p.Y-r)
		}
	}
}
