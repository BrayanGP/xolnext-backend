// Package store implementa la persistencia.
//
// Soporta dos motores con el mismo código:
//   - SQLite (driver puro-Go modernc.org/sqlite) para desarrollo local.
//   - Postgres (driver pgx) para producción en Railway (vía DATABASE_URL).
//
// El esquema guarda cada entidad como JSON en una columna TEXT, lo que mantiene
// el SQL portátil entre ambos motores. Los upserts usan `ON CONFLICT ... DO
// UPDATE` (soportado por SQLite >= 3.24 y por Postgres).
package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/BrayanGP/nexus-backend/internal/models"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib" // driver "pgx"
	_ "modernc.org/sqlite"             // driver "sqlite"
)

var ErrNotFound = errors.New("no encontrado")

type Store struct {
	db       *sql.DB
	postgres bool
}

// Open abre la base de datos. Si databaseURL es un DSN de Postgres se usa
// Postgres; en cualquier otro caso se usa SQLite en sqlitePath.
func Open(databaseURL, sqlitePath string) (*Store, error) {
	postgres := strings.HasPrefix(databaseURL, "postgres://") ||
		strings.HasPrefix(databaseURL, "postgresql://")

	var (
		db  *sql.DB
		err error
	)
	if postgres {
		db, err = sql.Open("pgx", databaseURL)
	} else {
		db, err = sql.Open("sqlite", sqlitePath)
	}
	if err != nil {
		return nil, err
	}
	if !postgres {
		db.SetMaxOpenConns(1) // SQLite: un solo escritor evita "database is locked"
	}
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("conectando a la base de datos: %w", err)
	}

	s := &Store{db: db, postgres: postgres}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	const schema = `
CREATE TABLE IF NOT EXISTS workers (
  id TEXT PRIMARY KEY, data TEXT NOT NULL,
  ciudad TEXT, oficio TEXT, disponibilidad TEXT, estado TEXT,
  updated_at TEXT
);
CREATE TABLE IF NOT EXISTS companies (
  id TEXT PRIMARY KEY, data TEXT NOT NULL, updated_at TEXT
);
CREATE TABLE IF NOT EXISTS requests (
  id TEXT PRIMARY KEY, data TEXT NOT NULL, estado TEXT, updated_at TEXT
);
CREATE TABLE IF NOT EXISTS candidates (
  id TEXT PRIMARY KEY, request_id TEXT, worker_id TEXT, data TEXT NOT NULL, updated_at TEXT
);
CREATE TABLE IF NOT EXISTS notifications (
  id TEXT PRIMARY KEY, audience TEXT, data TEXT NOT NULL, created_at TEXT
);`
	// Ambos motores aceptan ejecutar varias sentencias separadas por ';' una a una.
	for _, stmt := range strings.Split(schema, ";") {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("migración (%.40s...): %w", stmt, err)
		}
	}
	return nil
}

// rebind convierte los placeholders '?' a '$1, $2...' cuando usamos Postgres.
func (s *Store) rebind(query string) string {
	if !s.postgres {
		return query
	}
	var b strings.Builder
	n := 0
	for _, r := range query {
		if r == '?' {
			n++
			b.WriteString("$")
			b.WriteString(fmt.Sprint(n))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func (s *Store) exec(query string, args ...any) error {
	_, err := s.db.Exec(s.rebind(query), args...)
	return err
}

func (s *Store) query(query string, args ...any) (*sql.Rows, error) {
	return s.db.Query(s.rebind(query), args...)
}

func (s *Store) queryRow(query string, args ...any) *sql.Row {
	return s.db.QueryRow(s.rebind(query), args...)
}

func marshal(v any) string { b, _ := json.Marshal(v); return string(b) }

// ---------------- Workers ----------------

const upsertWorker = `INSERT INTO workers (id,data,ciudad,oficio,disponibilidad,estado,updated_at)
VALUES (?,?,?,?,?,?,?)
ON CONFLICT (id) DO UPDATE SET
  data=excluded.data, ciudad=excluded.ciudad, oficio=excluded.oficio,
  disponibilidad=excluded.disponibilidad, estado=excluded.estado, updated_at=excluded.updated_at`

func (s *Store) CreateWorker(w *models.Worker) error {
	if w.ID == "" {
		w.ID = uuid.NewString()
	}
	now := time.Now().UTC()
	if w.CreatedAt.IsZero() {
		w.CreatedAt = now
	}
	w.UpdatedAt = now
	if w.Estado == "" {
		w.Estado = w.Disponibilidad
	}
	return s.exec(upsertWorker, w.ID, marshal(w), w.Ciudad, w.OficioPrincipal,
		w.Disponibilidad, w.Estado, w.UpdatedAt.Format(time.RFC3339))
}

func (s *Store) ListWorkers(ciudad, oficio, disponibilidad string) ([]models.Worker, error) {
	q := `SELECT data FROM workers WHERE 1=1`
	var args []any
	if ciudad != "" {
		q += ` AND lower(ciudad)=lower(?)`
		args = append(args, ciudad)
	}
	if oficio != "" {
		q += ` AND lower(oficio)=lower(?)`
		args = append(args, oficio)
	}
	if disponibilidad != "" {
		q += ` AND disponibilidad=?`
		args = append(args, disponibilidad)
	}
	q += ` ORDER BY updated_at DESC`
	return s.scanWorkers(q, args...)
}

func (s *Store) GetWorker(id string) (*models.Worker, error) {
	ws, err := s.scanWorkers(`SELECT data FROM workers WHERE id=?`, id)
	if err != nil {
		return nil, err
	}
	if len(ws) == 0 {
		return nil, ErrNotFound
	}
	return &ws[0], nil
}

func (s *Store) UpdateWorkerStatus(id, estado string) (*models.Worker, error) {
	w, err := s.GetWorker(id)
	if err != nil {
		return nil, err
	}
	w.Estado = estado
	if estado == models.WorkerDisponible || estado == models.WorkerNoDisponible {
		w.Disponibilidad = estado
	}
	if err := s.CreateWorker(w); err != nil {
		return nil, err
	}
	return w, nil
}

func (s *Store) scanWorkers(q string, args ...any) ([]models.Worker, error) {
	rows, err := s.query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.Worker{}
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var w models.Worker
		if err := json.Unmarshal([]byte(data), &w); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// ---------------- Companies ----------------

const upsertCompany = `INSERT INTO companies (id,data,updated_at) VALUES (?,?,?)
ON CONFLICT (id) DO UPDATE SET data=excluded.data, updated_at=excluded.updated_at`

func (s *Store) CreateCompany(c *models.Company) error {
	if c.ID == "" {
		c.ID = uuid.NewString()
	}
	now := time.Now().UTC()
	if c.CreatedAt.IsZero() {
		c.CreatedAt = now
	}
	c.UpdatedAt = now
	return s.exec(upsertCompany, c.ID, marshal(c), c.UpdatedAt.Format(time.RFC3339))
}

func (s *Store) GetCompany(id string) (*models.Company, error) {
	var data string
	if err := s.queryRow(`SELECT data FROM companies WHERE id=?`, id).Scan(&data); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	var c models.Company
	return &c, json.Unmarshal([]byte(data), &c)
}

func (s *Store) ListCompanies() ([]models.Company, error) {
	rows, err := s.query(`SELECT data FROM companies ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.Company{}
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var c models.Company
		if err := json.Unmarshal([]byte(data), &c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ---------------- Requests ----------------

const upsertRequest = `INSERT INTO requests (id,data,estado,updated_at) VALUES (?,?,?,?)
ON CONFLICT (id) DO UPDATE SET data=excluded.data, estado=excluded.estado, updated_at=excluded.updated_at`

func (s *Store) CreateRequest(r *models.Request) error {
	if r.ID == "" {
		r.ID = uuid.NewString()
	}
	now := time.Now().UTC()
	if r.CreatedAt.IsZero() {
		r.CreatedAt = now
	}
	r.UpdatedAt = now
	if r.Estado == "" {
		r.Estado = models.RequestNueva
	}
	return s.exec(upsertRequest, r.ID, marshal(r), r.Estado, r.UpdatedAt.Format(time.RFC3339))
}

func (s *Store) ListRequests(estado string) ([]models.Request, error) {
	q := `SELECT data FROM requests`
	var args []any
	if estado != "" {
		q += ` WHERE estado=?`
		args = append(args, estado)
	}
	q += ` ORDER BY updated_at DESC`
	rows, err := s.query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.Request{}
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var r models.Request
		if err := json.Unmarshal([]byte(data), &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) GetRequest(id string) (*models.Request, error) {
	var data string
	if err := s.queryRow(`SELECT data FROM requests WHERE id=?`, id).Scan(&data); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	var r models.Request
	return &r, json.Unmarshal([]byte(data), &r)
}

func (s *Store) UpdateRequestStatus(id, estado string) (*models.Request, error) {
	r, err := s.GetRequest(id)
	if err != nil {
		return nil, err
	}
	r.Estado = estado
	if err := s.CreateRequest(r); err != nil {
		return nil, err
	}
	return r, nil
}

// ---------------- Candidates ----------------

const upsertCandidate = `INSERT INTO candidates (id,request_id,worker_id,data,updated_at) VALUES (?,?,?,?,?)
ON CONFLICT (id) DO UPDATE SET request_id=excluded.request_id, worker_id=excluded.worker_id,
  data=excluded.data, updated_at=excluded.updated_at`

func (s *Store) AddCandidate(c *models.Candidate) error {
	if c.ID == "" {
		c.ID = uuid.NewString()
	}
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now().UTC()
	}
	if c.Estado == "" {
		c.Estado = "pendiente"
	}
	return s.exec(upsertCandidate, c.ID, c.RequestID, c.WorkerID, marshal(c),
		c.CreatedAt.Format(time.RFC3339))
}

func (s *Store) ListCandidates(requestID string) ([]models.Candidate, error) {
	rows, err := s.query(`SELECT data FROM candidates WHERE request_id=? ORDER BY updated_at`, requestID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.Candidate{}
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var c models.Candidate
		if err := json.Unmarshal([]byte(data), &c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// PublicCandidates devuelve la lista que VE la empresa: solo campos permitidos.
func (s *Store) PublicCandidates(requestID string) ([]models.CandidatePublic, error) {
	cands, err := s.ListCandidates(requestID)
	if err != nil {
		return nil, err
	}
	out := []models.CandidatePublic{}
	for _, c := range cands {
		w, err := s.GetWorker(c.WorkerID)
		if err != nil {
			continue
		}
		out = append(out, models.CandidatePublic{
			CandidateID:     c.ID,
			WorkerID:        w.ID,
			Nombre:          w.NombreCompleto,
			Ciudad:          w.Ciudad,
			Oficio:          w.OficioPrincipal,
			Experiencia:     w.AniosExperiencia,
			Certificaciones: w.Certificaciones,
			Estado:          c.Estado,
		})
	}
	return out, nil
}

// ---------------- Notifications ----------------

const upsertNotification = `INSERT INTO notifications (id,audience,data,created_at) VALUES (?,?,?,?)
ON CONFLICT (id) DO UPDATE SET audience=excluded.audience, data=excluded.data, created_at=excluded.created_at`

func (s *Store) CreateNotification(n *models.Notification) error {
	if n.ID == "" {
		n.ID = uuid.NewString()
	}
	if n.CreatedAt.IsZero() {
		n.CreatedAt = time.Now().UTC()
	}
	return s.exec(upsertNotification, n.ID, n.Audience, marshal(n),
		n.CreatedAt.Format(time.RFC3339))
}

func (s *Store) ListNotifications(audience string) ([]models.Notification, error) {
	rows, err := s.query(
		`SELECT data FROM notifications WHERE audience=? OR audience='all' ORDER BY created_at DESC`,
		audience)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.Notification{}
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var n models.Notification
		if err := json.Unmarshal([]byte(data), &n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}
