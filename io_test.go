package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// -----------------------------------------------------------------------------
// O10 : frame binaire
// -----------------------------------------------------------------------------

// TestBinaryFrameMatchesState décode la frame binaire et la compare aux valeurs de la frame JSON
// (mêmes conversions int16 / uint8) : le contenu affiché par le navigateur est identique.
func TestBinaryFrameMatchesState(t *testing.T) {
	scenario(RushWarmupTicks).restore()
	updateSimulation()
	stats := getSimulationStats(TargetTPS)
	b := appendBinaryFrame(nil, &stats)

	want := binaryHeaderSize + 4*len(bars) + binaryPuddleSize*len(vomitPuddles) + binaryClientSize*len(clients)
	if len(b) != want {
		t.Fatalf("taille %d, attendu %d", len(b), want)
	}
	le := binary.LittleEndian
	if b[0] != BinaryFrameVersion || le.Uint32(b[4:]) != uint32(currentTick) || int(le.Uint16(b[40:])) != len(clients) {
		t.Fatalf("en-tête incohérent")
	}
	if got := le.Uint32(b[20:]); got != uint32(stats.BeersServed) {
		t.Fatalf("bières %d, attendu %d", got, stats.BeersServed)
	}
	off := binaryHeaderSize + 4*len(bars) + binaryPuddleSize*len(vomitPuddles)
	for i := range clients {
		e := b[off+i*binaryClientSize:]
		x, y := int16(le.Uint16(e[0:])), int16(le.Uint16(e[2:]))
		if x != int16(positions[i].X) || y != int16(positions[i].Y) || e[4] != uint8(clients[i].State) || e[5] != uint8(math.Min(255, clients[i].Alcohol*50.0)) {
			t.Fatalf("client %d mal encodé", i)
		}
	}
	t.Logf("frame binaire : %d octets pour %d clients et %d flaques (JSON : %d octets)", len(b), len(clients), len(vomitPuddles), len(appendFrame(nil, &stats)))
}

// BenchmarkBuildFrameBinary mesure télémétrie + encodage binaire, à comparer à BenchmarkBuildFrame (JSON).
func BenchmarkBuildFrameBinary(b *testing.B) {
	scenario(RushWarmupTicks).restore()
	buf := make([]byte, 0, FrameBufferCapacity)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		stats := getSimulationStats(TargetTPS)
		buf = appendBinaryFrame(buf[:0], &stats)
	}
	b.ReportMetric(float64(len(buf)), "bytes/frame")
}

// -----------------------------------------------------------------------------
// O11 : persistance SQLite
// -----------------------------------------------------------------------------

const (
	seedSessions         = 50
	seedRowsPerSession   = 4000 // ~1 h de simulation par session, à une ligne par seconde
	historyQueryRows     = 300  // 5 minutes d'historique demandées
	insertRowsPerBenchOp = 100
)

func sampleRow(session, tick int64) TelemetryRow {
	return TelemetryRow{SessionID: session, Tick: tick, RecordedAt: tick, TPS: 30, TickMs: 0.6, Sober: 6000, Drinking: 3800, Vomiting: 200, Puddles: 430, Beers: tick * 28, AvgAlcohol: 3.2}
}

func newTestStore(tb testing.TB, withIndex bool) *TelemetryStore {
	tb.Helper()
	s, err := openTelemetryStore(filepath.Join(tb.TempDir(), "t.db"), withIndex)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { s.Close() })
	return s
}

// seedStore insère seedSessions x seedRowsPerSession lignes (200 000) par lots.
func seedStore(tb testing.TB, s *TelemetryStore) {
	tb.Helper()
	batch := make([]TelemetryRow, 0, seedRowsPerSession)
	for sess := int64(1); sess <= seedSessions; sess++ {
		batch = batch[:0]
		for k := int64(0); k < seedRowsPerSession; k++ {
			batch = append(batch, sampleRow(sess, k*30))
		}
		if err := s.insertBatch(batch); err != nil {
			tb.Fatal(err)
		}
	}
}

func queryPlan(tb testing.TB, s *TelemetryStore) string {
	rows, err := s.db.Query("EXPLAIN QUERY PLAN "+historyQuerySQL, 25, 30000, 30000+historyQueryRows*30, HistoryMaxRows)
	if err != nil {
		tb.Fatal(err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, notused int
		var detail string
		rows.Scan(&id, &parent, &notused, &detail)
		plan = append(plan, detail)
	}
	return strings.Join(plan, " | ")
}

// TestHistoryQueryPlan vérifie que l'index transforme le parcours complet en recherche par index.
func TestHistoryQueryPlan(t *testing.T) {
	for _, withIndex := range []bool{false, true} {
		s := newTestStore(t, withIndex)
		plan := queryPlan(t, s)
		t.Logf("index=%v : %s", withIndex, plan)
		if withIndex != strings.Contains(plan, "USING INDEX idx_telemetry_session_tick") {
			t.Fatalf("plan inattendu (index=%v) : %s", withIndex, plan)
		}
	}
}

// TestTelemetryStoreRoundTrip : écriture asynchrone par lots, vidage à l'arrêt, relecture par l'API.
func TestTelemetryStoreRoundTrip(t *testing.T) {
	s := newTestStore(t, true)
	ctx, cancel := context.WithCancel(context.Background())
	s.Start(ctx)
	for k := int64(0); k < 120; k++ {
		s.Record(sampleRow(7, k*30))
	}
	cancel()
	s.wg.Wait() // Le contexte annulé force l'écriture du dernier lot

	rec := httptest.NewRecorder()
	s.historyHandler(true)(rec, httptest.NewRequest("GET", "/api/history?session=7&from=0&to=1500", nil))
	if rec.Code != http.StatusOK || strings.Count(rec.Body.String(), `"tick":`) != 51 {
		t.Fatalf("réponse inattendue (%d) : %.200s", rec.Code, rec.Body.String())
	}
}

// BenchmarkTelemetryInsert : 100 lignes par opération, en autocommit (1 commit par ligne) ou en un lot.
func BenchmarkTelemetryInsert(b *testing.B) {
	rows := make([]TelemetryRow, insertRowsPerBenchOp)
	for _, mode := range []string{"autocommit", "batch"} {
		b.Run("mode="+mode, func(b *testing.B) {
			s := newTestStore(b, true)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				for k := range rows {
					rows[k] = sampleRow(1, int64(i*insertRowsPerBenchOp+k))
				}
				var err error
				if mode == "batch" {
					err = s.insertBatch(rows)
				} else {
					err = s.insertAutocommit(rows)
				}
				if err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*insertRowsPerBenchOp), "ns/row")
		})
	}
}

// BenchmarkHistoryQuery : 5 minutes d'historique d'une session parmi 200 000 lignes.
func BenchmarkHistoryQuery(b *testing.B) {
	for _, withIndex := range []bool{false, true} {
		b.Run(fmt.Sprintf("index=%v", withIndex), func(b *testing.B) {
			s := newTestStore(b, withIndex)
			seedStore(b, s)
			buf := make([]byte, 0, 64<<10)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				var err error
				if buf, err = s.appendHistoryJSON(buf[:0], 25, 30000, 30000+historyQueryRows*30-1); err != nil {
					b.Fatal(err)
				}
			}
			if n := strings.Count(string(buf), `"tick":`); n != historyQueryRows {
				b.Fatalf("%d lignes, attendu %d", n, historyQueryRows)
			}
		})
	}
}

// discardWriter est un http.ResponseWriter minimal, pour ne mesurer que le handler.
type discardWriter struct{ h http.Header }

func (d *discardWriter) Header() http.Header         { return d.h }
func (d *discardWriter) Write(p []byte) (int, error) { return len(p), nil }
func (d *discardWriter) WriteHeader(int)             {}

// BenchmarkHistoryHandler : buffer de réponse recyclé par sync.Pool ou alloué à chaque requête.
func BenchmarkHistoryHandler(b *testing.B) {
	s := newTestStore(b, true)
	seedStore(b, s)
	req := httptest.NewRequest("GET", fmt.Sprintf("/api/history?session=25&from=0&to=%d", 1000*30), nil)
	for _, usePool := range []bool{false, true} {
		b.Run(fmt.Sprintf("pool=%v", usePool), func(b *testing.B) {
			h := s.historyHandler(usePool)
			w := &discardWriter{h: http.Header{}}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				h(w, req)
			}
		})
	}
}

// TestSeedLoadTestDB prépare une base de 200 000 lignes pour le test de charge HTTP (Vegeta).
// Ignoré sauf si BARMASH_SEED_DB désigne le fichier à créer ; BARMASH_DB_NOINDEX=1 crée la table sans index.
func TestSeedLoadTestDB(t *testing.T) {
	path := os.Getenv("BARMASH_SEED_DB")
	if path == "" {
		t.Skip("BARMASH_SEED_DB non défini")
	}
	s, err := openTelemetryStore(path, os.Getenv("BARMASH_DB_NOINDEX") == "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedStore(t, s)
	t.Logf("%s : %d lignes, plan : %s", path, seedSessions*seedRowsPerSession, queryPlan(t, s))
}
