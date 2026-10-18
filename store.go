package main

import (
	"context"
	"database/sql"
	"log"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite" // Driver SQLite en Go pur (pas de cgo)
)

// -----------------------------------------------------------------------------
// O11 : Historique de Télémétrie dans SQLite
//
// Une ligne par seconde de simulation est persistée. Trois choix mesurés :
//   - écriture asynchrone : le tick pousse dans un canal borné, une goroutine unique
//     écrit ; un disque lent ne bloque jamais la boucle à 30 TPS (ligne perdue si plein) ;
//   - insertions groupées dans une transaction avec requête préparée, au lieu d'un
//     autocommit (et d'un fsync) par ligne ;
//   - index composite (session_id, tick) pour les requêtes par plage de ticks
//     (EXPLAIN QUERY PLAN : SCAN -> SEARCH, cf. TestHistoryQueryPlan).
// -----------------------------------------------------------------------------

const (
	DBPath          = "data/barmash.db"
	telemetryBuffer = 256             // Lignes en attente avant abandon (≈ 4 min à 1 ligne/s)
	flushEvery      = 5 * time.Second // Une transaction groupée toutes les 5 s
	HistoryMaxRows  = 10000
)

const telemetrySchema = `
CREATE TABLE IF NOT EXISTS telemetry (
	id          INTEGER PRIMARY KEY,
	session_id  INTEGER NOT NULL,
	tick        INTEGER NOT NULL,
	recorded_at INTEGER NOT NULL,
	tps         INTEGER NOT NULL,
	tick_ms     REAL    NOT NULL,
	sober       INTEGER NOT NULL,
	drinking    INTEGER NOT NULL,
	vomiting    INTEGER NOT NULL,
	puddles     INTEGER NOT NULL,
	beers       INTEGER NOT NULL,
	avg_alcohol REAL    NOT NULL
);`

const telemetryIndex = `CREATE INDEX IF NOT EXISTS idx_telemetry_session_tick ON telemetry(session_id, tick);`

const insertTelemetrySQL = `INSERT INTO telemetry
	(session_id, tick, recorded_at, tps, tick_ms, sober, drinking, vomiting, puddles, beers, avg_alcohol)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

const historyQuerySQL = `SELECT tick, tps, tick_ms, sober, drinking, vomiting, puddles, beers, avg_alcohol
	FROM telemetry WHERE session_id = ? AND tick BETWEEN ? AND ? ORDER BY tick LIMIT ?`

// TelemetryRow est une seconde de simulation (valeurs scalaires : aucun pointeur).
type TelemetryRow struct {
	SessionID  int64
	Tick       int64
	RecordedAt int64
	TPS        int64
	TickMs     float64
	Sober      int64
	Drinking   int64
	Vomiting   int64
	Puddles    int64
	Beers      int64
	AvgAlcohol float64
}

func telemetryFromStats(session int64, s *SimStats) TelemetryRow {
	return TelemetryRow{
		SessionID: session, Tick: s.Tick, RecordedAt: time.Now().UnixMilli(), TPS: int64(s.TPS),
		TickMs: s.TickDurationMs, Sober: int64(s.SoberCount), Drinking: int64(s.DrinkingCount),
		Vomiting: int64(s.VomitingCount), Puddles: int64(s.ActivePuddles), Beers: int64(s.BeersServed),
		AvgAlcohol: s.AvgAlcohol,
	}
}

// TelemetryStore possède la connexion et l'unique goroutine d'écriture.
type TelemetryStore struct {
	db      *sql.DB
	query   *sql.Stmt
	rows    chan TelemetryRow
	dropped atomic.Int64 // Lignes abandonnées (canal plein)
	wg      sync.WaitGroup
}

// openTelemetryStore ouvre (ou crée) la base. withIndex=false sert uniquement au benchmark.
func openTelemetryStore(path string, withIndex bool) (*TelemetryStore, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	stmts := []string{telemetrySchema}
	if withIndex {
		stmts = append(stmts, telemetryIndex)
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			db.Close()
			return nil, err
		}
	}
	q, err := db.Prepare(historyQuerySQL)
	if err != nil {
		db.Close()
		return nil, err
	}
	return &TelemetryStore{db: db, query: q, rows: make(chan TelemetryRow, telemetryBuffer)}, nil
}

// Record est appelé depuis la boucle de tick : il ne bloque jamais.
func (s *TelemetryStore) Record(r TelemetryRow) {
	select {
	case s.rows <- r:
	default:
		s.dropped.Add(1) // Disque saturé : on préfère perdre une ligne d'historique que des ticks
	}
}

// Start lance l'unique goroutine d'écriture : lots jusqu'à l'annulation du contexte, puis vidage.
func (s *TelemetryStore) Start(ctx context.Context) {
	s.wg.Add(1)
	go s.run(ctx)
}

func (s *TelemetryStore) run(ctx context.Context) {
	defer s.wg.Done()
	batch := make([]TelemetryRow, 0, telemetryBuffer)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		if err := s.insertBatch(batch); err != nil {
			log.Printf("[SQLite] écriture de %d lignes impossible : %v", len(batch), err)
		}
		batch = batch[:0]
	}
	ticker := time.NewTicker(flushEvery)
	defer ticker.Stop()
	for {
		select {
		case r := <-s.rows:
			if batch = append(batch, r); len(batch) == cap(batch) {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-ctx.Done():
			for len(s.rows) > 0 {
				batch = append(batch, <-s.rows)
			}
			flush()
			return
		}
	}
}

// insertBatch insère toutes les lignes dans une seule transaction (un seul commit sur disque).
func (s *TelemetryStore) insertBatch(rows []TelemetryRow) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare(insertTelemetrySQL)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer stmt.Close()
	for i := range rows {
		r := &rows[i]
		if _, err := stmt.Exec(r.SessionID, r.Tick, r.RecordedAt, r.TPS, r.TickMs, r.Sober, r.Drinking, r.Vomiting, r.Puddles, r.Beers, r.AvgAlcohol); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// insertAutocommit est la version naïve (une transaction et un commit par ligne), gardée pour la mesure.
func (s *TelemetryStore) insertAutocommit(rows []TelemetryRow) error {
	for i := range rows {
		r := &rows[i]
		if _, err := s.db.Exec(insertTelemetrySQL, r.SessionID, r.Tick, r.RecordedAt, r.TPS, r.TickMs, r.Sober, r.Drinking, r.Vomiting, r.Puddles, r.Beers, r.AvgAlcohol); err != nil {
			return err
		}
	}
	return nil
}

func (s *TelemetryStore) Close() error {
	s.wg.Wait()
	s.query.Close()
	return s.db.Close()
}

// -----------------------------------------------------------------------------
// GET /api/history?session=<id>&from=<tick>&to=<tick>
// -----------------------------------------------------------------------------

// historyBufPool recycle les buffers de réponse (~80 octets par ligne, jusqu'à 10 000 lignes) :
// sous charge, chaque requête réutilise un buffer déjà dimensionné au lieu de le faire grossir.
var historyBufPool = sync.Pool{New: func() any { b := make([]byte, 0, 64<<10); return &b }}

func (s *TelemetryStore) historyHandler(usePool bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		session, err1 := strconv.ParseInt(q.Get("session"), 10, 64)
		from, err2 := strconv.ParseInt(q.Get("from"), 10, 64)
		to, err3 := strconv.ParseInt(q.Get("to"), 10, 64)
		if err1 != nil || err2 != nil || err3 != nil {
			http.Error(w, "paramètres attendus : session, from, to (entiers)", http.StatusBadRequest)
			return
		}

		var bp *[]byte
		if usePool {
			bp = historyBufPool.Get().(*[]byte)
		} else {
			b := make([]byte, 0, 512)
			bp = &b
		}
		buf, err := s.appendHistoryJSON((*bp)[:0], session, from, to)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		} else {
			w.Header().Set("Content-Type", "application/json")
			w.Write(buf)
		}
		if usePool {
			*bp = buf[:0]
			historyBufPool.Put(bp)
		}
	}
}

// appendHistoryJSON exécute la requête préparée et écrit le tableau JSON par append.
func (s *TelemetryStore) appendHistoryJSON(b []byte, session, from, to int64) ([]byte, error) {
	rows, err := s.query.Query(session, from, to, HistoryMaxRows)
	if err != nil {
		return b, err
	}
	defer rows.Close()
	b = append(b, '[')
	var r TelemetryRow
	for n := 0; rows.Next(); n++ {
		if err := rows.Scan(&r.Tick, &r.TPS, &r.TickMs, &r.Sober, &r.Drinking, &r.Vomiting, &r.Puddles, &r.Beers, &r.AvgAlcohol); err != nil {
			return b, err
		}
		if n > 0 {
			b = append(b, ',')
		}
		b = strconv.AppendInt(append(b, `{"tick":`...), r.Tick, 10)
		b = strconv.AppendInt(append(b, `,"tps":`...), r.TPS, 10)
		b = appendJSONFloat(append(b, `,"tick_ms":`...), r.TickMs)
		b = strconv.AppendInt(append(b, `,"sober":`...), r.Sober, 10)
		b = strconv.AppendInt(append(b, `,"drinking":`...), r.Drinking, 10)
		b = strconv.AppendInt(append(b, `,"vomiting":`...), r.Vomiting, 10)
		b = strconv.AppendInt(append(b, `,"puddles":`...), r.Puddles, 10)
		b = strconv.AppendInt(append(b, `,"beers":`...), r.Beers, 10)
		b = appendJSONFloat(append(b, `,"avg_alcohol":`...), r.AvgAlcohol)
		b = append(b, '}')
	}
	return append(b, ']'), rows.Err()
}
