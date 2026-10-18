package main

import (
	"math"
	"math/rand"
	"testing"
)

// TestBarLUTMatchesScan compare la table de Voronoï au scan linéaire, bit à bit,
// sur des points aléatoires et sur une grille fine couvrant toutes les frontières.
func TestBarLUTMatchesScan(t *testing.T) {
	initSimulation(MasterSeed)
	check := func(x, y float64) {
		gi, gd, gx, gy := nearestBar(x, y)
		wi, wd, wx, wy := nearestBarScan(x, y)
		if gi != wi || math.Float64bits(gd) != math.Float64bits(wd) || gx != wx || gy != wy {
			t.Fatalf("(%v, %v) : LUT=(%d, %v) scan=(%d, %v)", x, y, gi, gd, wi, wd)
		}
	}
	rng := rand.New(rand.NewSource(MasterSeed))
	for i := 0; i < 1_000_000; i++ {
		check(rng.Float64()*(WorldSize+100)-50, rng.Float64()*(WorldSize+100)-50)
	}
	for x := 0.0; x <= WorldSize; x += 0.37 {
		for y := 0.0; y <= WorldSize; y += 7.3 {
			check(x, y)
		}
	}

	ambiguous := 0
	for _, k := range barLUT {
		if k == barLUTAmbiguous {
			ambiguous++
		}
	}
	t.Logf("Cellules certifiées : %d / %d (%.1f %% de repli sur le scan)", len(barLUT)-ambiguous, len(barLUT), 100*float64(ambiguous)/float64(len(barLUT)))
}
