package main

// Tests d'équivalence : chaque optimisation est comparée, bit à bit ou octet à octet,
// à l'implémentation naïve qu'elle remplace (oracle).

import (
	"bytes"
	"encoding/json"
	"math"
	"math/rand"
	"strconv"
	"testing"
)

// -----------------------------------------------------------------------------
// O2 : index spatial CSR des flaques
// -----------------------------------------------------------------------------

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

// -----------------------------------------------------------------------------
// O7 : table de Voronoï du bar le plus proche
// -----------------------------------------------------------------------------

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

// -----------------------------------------------------------------------------
// O6 / O6b : encodeur de frame octet-identique à encoding/json
// -----------------------------------------------------------------------------

// Schéma de référence de la version naïve, encodé par encoding/json (oracle).
type oracleClientPoint struct {
	X       int16 `json:"x"`
	Y       int16 `json:"y"`
	State   int8  `json:"s"`
	Alcohol uint8 `json:"a"`
}

type oraclePuddle struct {
	ID             int64   `json:"id"`
	X              float64 `json:"x"`
	Y              float64 `json:"y"`
	Radius         float64 `json:"radius"`
	RemainingTicks int     `json:"remaining_ticks"`
}

type oracleFrame struct {
	Stats        SimStats            `json:"stats"`
	WorldSize    float64             `json:"world_size"`
	Bars         []BarCounter        `json:"bars"`
	VomitPuddles []oraclePuddle      `json:"puddles"`
	Clients      []oracleClientPoint `json:"clients"`
}

// oracleFrameJSON reproduit l'ancien buildFrame (structures intermédiaires + json.Marshal).
func oracleFrameJSON(stats SimStats) []byte {
	f := oracleFrame{
		Stats:        stats,
		WorldSize:    WorldSize,
		Bars:         bars,
		VomitPuddles: make([]oraclePuddle, len(vomitPuddles)),
		Clients:      make([]oracleClientPoint, len(clients)),
	}
	for i, p := range vomitPuddles {
		f.VomitPuddles[i] = oraclePuddle{p.ID, p.X, p.Y, p.Radius, int(p.ExpireTick - currentTick)}
	}
	for i := range clients {
		f.Clients[i] = oracleClientPoint{
			X:       int16(positions[i].X),
			Y:       int16(positions[i].Y),
			State:   int8(clients[i].State),
			Alcohol: uint8(math.Min(255, clients[i].Alcohol*50.0)),
		}
	}
	data, err := json.Marshal(f)
	if err != nil {
		panic(err)
	}
	return data
}

// TestFrameMatchesEncodingJSON garantit que l'interface web reçoit exactement les mêmes octets.
func TestFrameMatchesEncodingJSON(t *testing.T) {
	for _, sc := range benchScenarios {
		scenario(sc.warmup).restore()
		for tick := 0; tick < 3; tick++ { // Inclut des flaques créées pendant le tick
			updateSimulation()
			stats := getSimulationStats(TargetTPS)
			want := oracleFrameJSON(stats)
			got := appendFrame(nil, &stats)
			if !bytes.Equal(got, want) {
				i := 0
				for i < len(got) && i < len(want) && got[i] == want[i] {
					i++
				}
				lo := max(i-40, 0)
				t.Fatalf("%s : frame différente à l'octet %d\n got: %s\nwant: %s", sc.name, i,
					got[lo:min(i+40, len(got))], want[lo:min(i+40, len(want))])
			}
		}
		t.Logf("%s : %d octets identiques à encoding/json", sc.name, len(appendFrame(nil, new(SimStats))))
	}
}

// TestAppendJSONFloat couvre les bascules de notation 'f' / 'e' de encoding/json.
func TestAppendJSONFloat(t *testing.T) {
	values := []float64{0, math.Copysign(0, -1), 1, -1, 0.5, 1e-6, 9.99e-7, 1e-7, 1.5e-9, 1e20, 1e21, 123456789.125, -3.25e-8, 2000, 24, 0.01}
	rng := rand.New(rand.NewSource(MasterSeed))
	for i := 0; i < 100000; i++ {
		values = append(values, (rng.Float64()-0.5)*math.Pow(10, float64(rng.Intn(50)-25)))
	}
	for _, v := range values {
		want, _ := json.Marshal(v)
		if got := appendJSONFloat(nil, v); !bytes.Equal(got, want) {
			t.Fatalf("appendJSONFloat(%v) = %s, encoding/json = %s", v, got, want)
		}
	}
}

// TestAppendSmallInt compare la table décimale à strconv, y compris hors table et buffer plein.
func TestAppendSmallInt(t *testing.T) {
	for v := int64(-3000); v <= 3000; v++ {
		want := strconv.AppendInt([]byte("k:"), v, 10)
		if got := appendSmallInt([]byte("k:"), v); !bytes.Equal(got, want) {
			t.Fatalf("appendSmallInt(%d) = %q, attendu %q", v, got, want)
		}
		tight := append(make([]byte, 0, 3), 'k', ':')
		if got := appendSmallInt(tight, v); !bytes.Equal(got, want) {
			t.Fatalf("appendSmallInt(%d) buffer plein = %q, attendu %q", v, got, want)
		}
	}
}
