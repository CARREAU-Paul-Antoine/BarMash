package main

import (
	"bytes"
	"encoding/json"
	"math"
	"math/rand"
	"testing"
)

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
