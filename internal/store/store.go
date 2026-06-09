// Package store implementa la persistencia en SQLite usando el driver puro-Go
// modernc.org/sqlite (no requiere cgo ni un compilador de C en Windows).
package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/BrayanGP/nexus-backend/internal/models"
	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("no encontrado")

type Store struct {
	db *sql.DB
}

// Open abre (o crea) la base de datos SQLite en path y aplica el esquema.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // SQLite: un solo escritor evita "database is locked"
	s := &Store{db: db}
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
	_, err := s.db.Exec(schema)
	return err
}

func marshal(v any) string { b, _ := json.Marshal(v); return string(b) }

// ---------------- Workers ----------------

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
	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO workers (id,data,ciudad,oficio,disponibilidad,estado,updated_at)
		 VALUES (?,?,?,?,?,?,?)`,
		w.ID, marshal(w), w.Ciudad, w.OficioPrincipal, w.Disponibilidad, w.Estado,
		w.UpdatedAt.Format(time.RFC3339))
	return err
}

// ListWorkers permite filtrar por ciudad, oficio y disponibilidad (panel admin).
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
	return queryWorkers(s.db, q, args...)
}

func (s *Store) GetWorker(id string) (*models.Worker, error) {
	ws, err := queryWorkers(s.db, `SELECT data FROM workers WHERE id=?`, id)
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

func queryWorkers(db *sql.DB, q string, args ...any) ([]models.Worker, error) {
	rows, err := db.Query(q, args...)
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

func (s *Store) CreateCompany(c *models.Company) error {
	if c.ID == "" {
		c.ID = uuid.NewString()
	}
	now := time.Now().UTC()
	if c.CreatedAt.IsZero() {
		c.CreatedAt = now
	}
	c.UpdatedAt = now
	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO companies (id,data,updated_at) VALUES (?,?,?)`,
		c.ID, marshal(c), c.UpdatedAt.Format(time.RFC3339))
	return err
}

func (s *Store) GetCompany(id string) (*models.Company, error) {
	row := s.db.QueryRow(`SELECT data FROM companies WHERE id=?`, id)
	var data string
	if err := row.Scan(&data); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	var c models.Company
	return &c, json.Unmarshal([]byte(data), &c)
}

func (s *Store) ListCompanies() ([]models.Company, error) {
	rows, err := s.db.Query(`SELECT data FROM companies ORDER BY updated_at DESC`)
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
	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO requests (id,data,estado,updated_at) VALUES (?,?,?,?)`,
		r.ID, marshal(r), r.Estado, r.UpdatedAt.Format(time.RFC3339))
	return err
}

func (s *Store) ListRequests(estado string) ([]models.Request, error) {
	q := `SELECT data FROM requests`
	var args []any
	if estado != "" {
		q += ` WHERE estado=?`
		args = append(args, estado)
	}
	q += ` ORDER BY updated_at DESC`
	rows, err := s.db.Query(q, args...)
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
	row := s.db.QueryRow(`SELECT data FROM requests WHERE id=?`, id)
	var data string
	if err := row.Scan(&data); err != nil {
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
	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO candidates (id,request_id,worker_id,data,updated_at) VALUES (?,?,?,?,?)`,
		c.ID, c.RequestID, c.WorkerID, marshal(c), c.CreatedAt.Format(time.RFC3339))
	return err
}

func (s *Store) ListCandidates(requestID string) ([]models.Candidate, error) {
	rows, err := s.db.Query(`SELECT data FROM candidates WHERE request_id=? ORDER BY updated_at`, requestID)
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

// PublicCandidates devuelve la lista que VE la empresa: solo campos permitidos
// (nombre, ciudad, oficio, experiencia, certificaciones, estado). Sin datos
// privados (teléfono, correo, dirección).
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

func (s *Store) CreateNotification(n *models.Notification) error {
	if n.ID == "" {
		n.ID = uuid.NewString()
	}
	if n.CreatedAt.IsZero() {
		n.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO notifications (id,audience,data,created_at) VALUES (?,?,?,?)`,
		n.ID, n.Audience, marshal(n), n.CreatedAt.Format(time.RFC3339))
	return err
}

// ListNotifications devuelve notificaciones para una audiencia concreta más las
// dirigidas a "all".
func (s *Store) ListNotifications(audience string) ([]models.Notification, error) {
	rows, err := s.db.Query(
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
