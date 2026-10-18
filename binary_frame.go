package main

import (
	"encoding/binary"
	"encoding/json"
	"math"
)

// -----------------------------------------------------------------------------
// O10 : Frame Réseau Binaire (format BMF v1, little-endian)
//
// La frame JSON (O6) pèse ~360 Ko, dont 92 % pour les 10 000 objets clients. La frame
// binaire envoie des champs à taille fixe : 6 octets par client au lieu de ~36. Les
// métadonnées statiques des comptoirs (noms, géométrie) partent une seule fois à la
// connexion, dans un message JSON texte « init ».
//
//	En-tête (44 o)
//	  0 u8  version (1)        1 u8  tps               2 u16 nombre de comptoirs
//	  4 u32 tick               8 f32 durée du tick (ms)
//	 12 u16 sobres            14 u16 buveurs           16 u16 vomisseurs
//	 18 u16 nombre de flaques 20 u32 bières servies    24 f32 alcoolémie moyenne
//	 28 f32 tas (Mo)          32 f32 alloué cumulé (Mo) 36 u32 cycles GC
//	 40 u16 nombre de clients 42 u16 réservé
//	Comptoirs  : u32 bières servies, par comptoir
//	Flaques    : u16 x, u16 y, u8 rayon, u8 ticks restants      (6 o)
//	Clients    : i16 x, i16 y, u8 état, u8 alcoolémie × 50      (6 o)
// -----------------------------------------------------------------------------

const (
	BinaryFrameVersion = 1
	binaryHeaderSize   = 44
	binaryPuddleSize   = 6
	binaryClientSize   = 6
)

var le = binary.LittleEndian

// appendBinaryFrame écrit la frame binaire du tick courant à la suite de dst, sans allocation
// si dst a la capacité suffisante (buffer de connexion recyclé, cf. frameBufPool).
func appendBinaryFrame(dst []byte, s *SimStats) []byte {
	size := binaryHeaderSize + 4*len(bars) + binaryPuddleSize*len(vomitPuddles) + binaryClientSize*len(clients)
	n := len(dst)
	if cap(dst)-n < size {
		dst = append(dst, make([]byte, size)...)[:n]
	}
	b := dst[n : n+size]

	b[0] = BinaryFrameVersion
	b[1] = uint8(min(s.TPS, 255))
	le.PutUint16(b[2:], uint16(len(bars)))
	le.PutUint32(b[4:], uint32(s.Tick))
	le.PutUint32(b[8:], math.Float32bits(float32(s.TickDurationMs)))
	le.PutUint16(b[12:], uint16(s.SoberCount))
	le.PutUint16(b[14:], uint16(s.DrinkingCount))
	le.PutUint16(b[16:], uint16(s.VomitingCount))
	le.PutUint16(b[18:], uint16(len(vomitPuddles)))
	le.PutUint32(b[20:], uint32(s.BeersServed))
	le.PutUint32(b[24:], math.Float32bits(float32(s.AvgAlcohol)))
	le.PutUint32(b[28:], math.Float32bits(float32(s.HeapAllocMB)))
	le.PutUint32(b[32:], math.Float32bits(float32(s.TotalAllocMB)))
	le.PutUint32(b[36:], s.NumGC)
	le.PutUint16(b[40:], uint16(len(clients)))
	le.PutUint16(b[42:], 0)

	off := binaryHeaderSize
	for i := range bars {
		le.PutUint32(b[off:], uint32(bars[i].BeersServed))
		off += 4
	}
	for i := range vomitPuddles {
		p := &vomitPuddles[i]
		le.PutUint16(b[off:], uint16(p.X))
		le.PutUint16(b[off+2:], uint16(p.Y))
		b[off+4] = uint8(p.Radius)
		b[off+5] = uint8(p.ExpireTick - currentTick)
		off += binaryPuddleSize
	}
	for i := range clients {
		c := &clients[i]
		e := b[off : off+binaryClientSize : off+binaryClientSize]
		le.PutUint16(e[0:], uint16(int16(positions[i].X)))
		le.PutUint16(e[2:], uint16(int16(positions[i].Y)))
		e[4] = uint8(c.State)
		e[5] = uint8(math.Min(255, c.Alcohol*50.0))
		off += binaryClientSize
	}
	return dst[:n+size]
}

// initMessageJSON décrit les données statiques envoyées une fois par connexion (chemin froid).
func initMessageJSON(session int64) []byte {
	type barInfo struct {
		ID   int     `json:"id"`
		Name string  `json:"name"`
		X    float64 `json:"x"`
		Y    float64 `json:"y"`
		W    float64 `json:"w"`
		H    float64 `json:"h"`
	}
	msg := struct {
		Type      string    `json:"type"`
		Version   int       `json:"version"`
		Session   int64     `json:"session"`
		WorldSize float64   `json:"world_size"`
		Bars      []barInfo `json:"bars"`
	}{Type: "init", Version: BinaryFrameVersion, Session: session, WorldSize: WorldSize}
	for _, b := range bars {
		msg.Bars = append(msg.Bars, barInfo{b.ID, b.Name, b.X, b.Y, b.W, b.H})
	}
	data, _ := json.Marshal(msg)
	return data
}
