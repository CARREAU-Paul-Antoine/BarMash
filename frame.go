package main

import (
	"encoding/json"
	"math"
	"runtime/metrics"
	"strconv"
)

// -----------------------------------------------------------------------------
// O6 : Encodage de Frame Append-Only (Zéro Allocation, Octet-Identique)
//
// La frame est écrite par append dans un buffer réutilisé par connexion, au lieu
// de construire une []ClientPoint puis d'appeler json.Marshal (réflexion, ~600 Ko
// de déchets par frame). Le format produit est octet pour octet celui de
// encoding/json (cf. TestFrameMatchesEncodingJSON) : l'interface web est inchangée.
// -----------------------------------------------------------------------------

// FrameBufferCapacity couvre une frame de croisière (~360 Ko) sans réallocation.
const FrameBufferCapacity = 512 << 10

// memSamples lit la télémétrie mémoire via runtime/metrics : contrairement à
// runtime.ReadMemStats, aucune pause Stop-The-World n'est déclenchée à chaque tick.
var memSamples = []metrics.Sample{
	{Name: "/memory/classes/heap/objects:bytes"}, // Équivalent de MemStats.HeapAlloc
	{Name: "/gc/heap/allocs:bytes"},              // Équivalent de MemStats.TotalAlloc
	{Name: "/gc/cycles/total:gc-cycles"},         // Équivalent de MemStats.NumGC
}

// barJSONPrefix contient, pour chaque comptoir, tout son objet JSON jusqu'à
// "beers_served": inclus : seuls les compteurs de bières varient d'un tick à l'autre.
var barJSONPrefix [][]byte

// buildBarJSONPrefixes est appelé à l'initialisation (chemin froid).
func buildBarJSONPrefixes() {
	barJSONPrefix = barJSONPrefix[:0]
	for i := range bars {
		b := &bars[i]
		name, _ := json.Marshal(b.Name) // Échappement JSON exact, une seule fois
		p := append([]byte(`{"id":`), strconv.Itoa(b.ID)...)
		p = append(p, `,"name":`...)
		p = append(p, name...)
		p = appendJSONFloat(append(p, `,"x":`...), b.X)
		p = appendJSONFloat(append(p, `,"y":`...), b.Y)
		p = appendJSONFloat(append(p, `,"w":`...), b.W)
		p = appendJSONFloat(append(p, `,"h":`...), b.H)
		barJSONPrefix = append(barJSONPrefix, append(p, `,"beers_served":`...))
	}
}

// appendJSONFloat reproduit exactement le formatage float64 de encoding/json.
func appendJSONFloat(b []byte, f float64) []byte {
	abs := math.Abs(f)
	format := byte('f')
	if abs != 0 && (abs < 1e-6 || abs >= 1e21) {
		format = 'e'
	}
	b = strconv.AppendFloat(b, f, format, -1, 64)
	if format == 'e' {
		// encoding/json écrit e-7 et non e-07
		if n := len(b); n >= 4 && b[n-4] == 'e' && b[n-3] == '-' && b[n-2] == '0' {
			b[n-2] = b[n-1]
			b = b[:n-1]
		}
	}
	return b
}

func appendStats(b []byte, s *SimStats) []byte {
	b = strconv.AppendInt(append(b, `{"seed":`...), s.Seed, 10)
	b = strconv.AppendInt(append(b, `,"tick":`...), s.Tick, 10)
	b = strconv.AppendInt(append(b, `,"tps":`...), int64(s.TPS), 10)
	b = appendJSONFloat(append(b, `,"tick_duration_ms":`...), s.TickDurationMs)
	b = strconv.AppendInt(append(b, `,"total_clients":`...), int64(s.TotalClients), 10)
	b = strconv.AppendInt(append(b, `,"sober_count":`...), int64(s.SoberCount), 10)
	b = strconv.AppendInt(append(b, `,"drinking_count":`...), int64(s.DrinkingCount), 10)
	b = strconv.AppendInt(append(b, `,"vomiting_count":`...), int64(s.VomitingCount), 10)
	b = strconv.AppendInt(append(b, `,"beers_served":`...), int64(s.BeersServed), 10)
	b = strconv.AppendInt(append(b, `,"active_puddles":`...), int64(s.ActivePuddles), 10)
	b = appendJSONFloat(append(b, `,"avg_alcohol":`...), s.AvgAlcohol)
	b = appendJSONFloat(append(b, `,"heap_alloc_mb":`...), s.HeapAllocMB)
	b = appendJSONFloat(append(b, `,"total_alloc_mb":`...), s.TotalAllocMB)
	b = strconv.AppendUint(append(b, `,"num_gc":`...), uint64(s.NumGC), 10)
	return append(b, '}')
}

// appendFrame écrit la frame complète du tick courant à la suite de dst.
func appendFrame(dst []byte, stats *SimStats) []byte {
	b := appendStats(append(dst, `{"stats":`...), stats)
	b = appendJSONFloat(append(b, `,"world_size":`...), WorldSize)

	b = append(b, `,"bars":[`...)
	for i := range bars {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, barJSONPrefix[i]...)
		b = strconv.AppendInt(b, int64(bars[i].BeersServed), 10)
		b = append(b, '}')
	}

	b = append(b, `],"puddles":[`...)
	for i := range vomitPuddles {
		p := &vomitPuddles[i]
		if i > 0 {
			b = append(b, ',')
		}
		b = strconv.AppendInt(append(b, `{"id":`...), p.ID, 10)
		b = appendJSONFloat(append(b, `,"x":`...), p.X)
		b = appendJSONFloat(append(b, `,"y":`...), p.Y)
		b = appendJSONFloat(append(b, `,"radius":`...), p.Radius)
		b = strconv.AppendInt(append(b, `,"remaining_ticks":`...), p.ExpireTick-currentTick, 10)
		b = append(b, '}')
	}

	// Mêmes conversions que l'ancienne ClientPoint (int16, int8, uint8 mis à l'échelle)
	b = append(b, `],"clients":[`...)
	for i := range clients {
		c := &clients[i]
		if i > 0 {
			b = append(b, ',')
		}
		b = strconv.AppendInt(append(b, `{"x":`...), int64(int16(positions[i].X)), 10)
		b = strconv.AppendInt(append(b, `,"y":`...), int64(int16(positions[i].Y)), 10)
		b = strconv.AppendInt(append(b, `,"s":`...), int64(int8(c.State)), 10)
		b = strconv.AppendUint(append(b, `,"a":`...), uint64(uint8(math.Min(255, c.Alcohol*50.0))), 10)
		b = append(b, '}')
	}
	return append(b, "]}"...)
}
