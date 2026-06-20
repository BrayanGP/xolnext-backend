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
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/BrayanGP/nexus-backend/internal/models"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib" // driver "pgx"
	_ "modernc.org/sqlite"             // driver "sqlite"
)

var (
	ErrNotFound   = errors.New("no encontrado")
	ErrEmailTaken = errors.New("el correo ya está registrado")
)

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

// Engine devuelve el motor de base de datos en uso.
func (s *Store) Engine() string {
	if s.postgres {
		return "Postgres"
	}
	return "SQLite"
}

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
);
CREATE TABLE IF NOT EXISTS users (
  id TEXT PRIMARY KEY, email TEXT UNIQUE NOT NULL,
  password_hash TEXT NOT NULL, data TEXT NOT NULL, created_at TEXT
);
CREATE TABLE IF NOT EXISTS password_resets (
  email TEXT PRIMARY KEY, code TEXT NOT NULL, expires_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS history (
  id TEXT PRIMARY KEY, request_id TEXT, data TEXT NOT NULL, created_at TEXT
);
CREATE TABLE IF NOT EXISTS ratings (
  id TEXT PRIMARY KEY, request_id TEXT, rater_user_id TEXT, target_type TEXT,
  target_id TEXT, stars INTEGER, data TEXT NOT NULL, created_at TEXT
);
CREATE TABLE IF NOT EXISTS work_hours (
  id TEXT PRIMARY KEY, request_id TEXT, worker_id TEXT, horas REAL, data TEXT NOT NULL, created_at TEXT
);
CREATE TABLE IF NOT EXISTS complaints (
  id TEXT PRIMARY KEY, author_user_id TEXT, estado TEXT, data TEXT NOT NULL, created_at TEXT
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

// PublicStats devuelve agregados NO sensibles para la pantalla de exploración
// (totales y conteos por oficio/ciudad). Nunca incluye datos personales.
func (s *Store) PublicStats() map[string]any {
	count := func(q string, args ...any) int {
		var n int
		_ = s.queryRow(q, args...).Scan(&n)
		return n
	}
	group := func(q string) []map[string]any {
		rows, err := s.query(q)
		if err != nil {
			return []map[string]any{}
		}
		defer rows.Close()
		out := []map[string]any{}
		for rows.Next() {
			var label string
			var c int
			if rows.Scan(&label, &c) == nil {
				out = append(out, map[string]any{"label": label, "count": c})
			}
		}
		return out
	}
	return map[string]any{
		"trabajadores": count(`SELECT COUNT(*) FROM workers`),
		"empresas":     count(`SELECT COUNT(*) FROM companies`),
		"disponibles":  count(`SELECT COUNT(*) FROM workers WHERE disponibilidad=?`, models.WorkerDisponible),
		"oficios":      group(`SELECT oficio, COUNT(*) c FROM workers WHERE oficio<>'' GROUP BY oficio ORDER BY c DESC LIMIT 8`),
		"ciudades":     group(`SELECT ciudad, COUNT(*) c FROM workers WHERE ciudad<>'' GROUP BY ciudad ORDER BY c DESC LIMIT 6`),
	}
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
	if r.Folio == "" {
		r.Folio = s.uniqueFolio()
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

// uniqueFolio genera un folio garantizando que no exista ya.
func (s *Store) uniqueFolio() string {
	for i := 0; i < 10; i++ {
		f := generateFolio()
		var n int
		if err := s.queryRow(`SELECT COUNT(*) FROM requests WHERE data LIKE ?`,
			`%"`+f+`"%`).Scan(&n); err != nil || n == 0 {
			return f
		}
	}
	return generateFolio()
}

// generateFolio crea un folio legible tipo "NX-7K2P9Q".
func generateFolio() string {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789" // sin caracteres ambiguos
	b := make([]byte, 6)
	for i := range b {
		b[i] = alphabet[rand.IntN(len(alphabet))]
	}
	return "NX-" + string(b)
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
	if c.RespuestaTrabajador == "" {
		c.RespuestaTrabajador = models.RespPendiente
	}
	return s.exec(upsertCandidate, c.ID, c.RequestID, c.WorkerID, marshal(c),
		c.CreatedAt.Format(time.RFC3339))
}

func (s *Store) GetCandidate(id string) (*models.Candidate, error) {
	var data string
	if err := s.queryRow(`SELECT data FROM candidates WHERE id=?`, id).Scan(&data); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	var c models.Candidate
	return &c, json.Unmarshal([]byte(data), &c)
}

// CandidateExists indica si el trabajador ya está en la solicitud.
func (s *Store) CandidateExists(requestID, workerID string) (bool, error) {
	var n int
	err := s.queryRow(
		`SELECT COUNT(*) FROM candidates WHERE request_id=? AND worker_id=?`,
		requestID, workerID).Scan(&n)
	return n > 0, err
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

// CandidatesByWorker devuelve las candidaturas de un trabajador.
func (s *Store) CandidatesByWorker(workerID string) ([]models.Candidate, error) {
	rows, err := s.query(
		`SELECT data FROM candidates WHERE worker_id=? ORDER BY updated_at DESC`, workerID)
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
	seen := map[string]bool{}
	for _, c := range cands {
		if seen[c.WorkerID] {
			continue // evitar candidatos duplicados
		}
		seen[c.WorkerID] = true
		w, err := s.GetWorker(c.WorkerID)
		if err != nil {
			continue
		}
		rs := s.RatingSummary(w.ID)
		out = append(out, models.CandidatePublic{
			CandidateID:     c.ID,
			WorkerID:        w.ID,
			Nombre:          w.NombreCompleto,
			Ciudad:          w.Ciudad,
			Oficio:          w.OficioPrincipal,
			Experiencia:     w.AniosExperiencia,
			Certificaciones: w.Certificaciones,
			Estado:              c.Estado,
			RespuestaTrabajador: c.RespuestaTrabajador,
			Comentario:          c.Comentario,
			Rating:              rs.Average,
			RatingCount:         rs.Count,
			TrabajosConcluidos:  s.WorkerCompletedJobs(w.ID),
			TotalHoras:          s.WorkerTotalHours(w.ID),
			CompetenciasTecnicas:   w.CompetenciasTecnicas,
			CompetenciasPersonales: w.CompetenciasPersonales,
			Licencias:           w.Licencias,
			AniosExperiencia:    w.AniosExperiencia,
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

// ---------------- Users ----------------

func normalizeEmail(email string) string { return strings.ToLower(strings.TrimSpace(email)) }

// CreateUser registra un usuario. Devuelve ErrEmailTaken si el correo ya existe.
func (s *Store) CreateUser(u *models.User) error {
	u.Email = normalizeEmail(u.Email)
	if u.ID == "" {
		u.ID = uuid.NewString()
	}
	if u.CreatedAt.IsZero() {
		u.CreatedAt = time.Now().UTC()
	}
	if _, err := s.GetUserByEmail(u.Email); err == nil {
		return ErrEmailTaken
	}
	// El hash de contraseña se guarda en su propia columna (no en el JSON, que
	// lo omite por seguridad) para poder verificarlo al iniciar sesión.
	return s.exec(
		`INSERT INTO users (id,email,password_hash,data,created_at) VALUES (?,?,?,?,?)`,
		u.ID, u.Email, u.PasswordHash, marshal(u), u.CreatedAt.Format(time.RFC3339))
}

func (s *Store) GetUserByEmail(email string) (*models.User, error) {
	return s.scanUser(`SELECT password_hash, data FROM users WHERE email=?`, normalizeEmail(email))
}

func (s *Store) GetUserByID(id string) (*models.User, error) {
	return s.scanUser(`SELECT password_hash, data FROM users WHERE id=?`, id)
}

func (s *Store) CountUsers() (int, error) {
	var n int
	err := s.queryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

func (s *Store) scanUser(q, arg string) (*models.User, error) {
	var hash, data string
	if err := s.queryRow(q, arg).Scan(&hash, &data); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	var u models.User
	if err := json.Unmarshal([]byte(data), &u); err != nil {
		return nil, err
	}
	u.PasswordHash = hash // proviene de su columna, no del JSON
	return &u, nil
}

// UpdateUserPassword cambia el hash de contraseña de un usuario.
func (s *Store) UpdateUserPassword(id, hash string) error {
	return s.exec(`UPDATE users SET password_hash=? WHERE id=?`, hash, id)
}

// ---------------- Password resets ----------------

const upsertReset = `INSERT INTO password_resets (email,code,expires_at) VALUES (?,?,?)
ON CONFLICT (email) DO UPDATE SET code=excluded.code, expires_at=excluded.expires_at`

// hashCode guarda solo el hash del código (no texto plano).
func hashCode(code string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(code)))
	return hex.EncodeToString(sum[:])
}

func (s *Store) SetReset(email, code string, expires time.Time) error {
	return s.exec(upsertReset, normalizeEmail(email), hashCode(code), expires.Format(time.RFC3339))
}

// VerifyReset valida el código contra el hash guardado y su expiración.
func (s *Store) VerifyReset(email, code string) (bool, error) {
	var stored, exp string
	row := s.queryRow(`SELECT code, expires_at FROM password_resets WHERE email=?`, normalizeEmail(email))
	if err := row.Scan(&stored, &exp); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	expires, _ := time.Parse(time.RFC3339, exp)
	if time.Now().After(expires) {
		return false, nil
	}
	return stored == hashCode(code), nil
}

func (s *Store) DeleteReset(email string) error {
	return s.exec(`DELETE FROM password_resets WHERE email=?`, normalizeEmail(email))
}

// ---------------- Ratings ----------------

const upsertRating = `INSERT INTO ratings (id,request_id,rater_user_id,target_type,target_id,stars,data,created_at)
VALUES (?,?,?,?,?,?,?,?)
ON CONFLICT (id) DO UPDATE SET stars=excluded.stars, data=excluded.data`

// AddRating registra (o actualiza) una calificación. Una por (rater,target,solicitud).
func (s *Store) AddRating(r *models.Rating) error {
	// Reusar id existente si ya calificó a ese objetivo en esa solicitud.
	var existingID string
	_ = s.queryRow(
		`SELECT id FROM ratings WHERE rater_user_id=? AND target_id=? AND request_id=?`,
		r.RaterUserID, r.TargetID, r.RequestID).Scan(&existingID)
	if existingID != "" {
		r.ID = existingID
	} else if r.ID == "" {
		r.ID = uuid.NewString()
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now().UTC()
	}
	return s.exec(upsertRating, r.ID, r.RequestID, r.RaterUserID, r.TargetType,
		r.TargetID, r.Stars, marshal(r), r.CreatedAt.Format(time.RFC3339))
}

// RatingSummary devuelve el promedio y conteo de calificaciones de un objetivo.
func (s *Store) RatingSummary(targetID string) models.RatingSummary {
	var avg sql.NullFloat64
	var count int
	_ = s.queryRow(
		`SELECT AVG(stars), COUNT(*) FROM ratings WHERE target_id=?`, targetID).Scan(&avg, &count)
	out := models.RatingSummary{Count: count}
	if avg.Valid {
		out.Average = float64(int(avg.Float64*10+0.5)) / 10 // 1 decimal
	}
	return out
}

// ---------------- Horas trabajadas ----------------

func (s *Store) AddWorkHours(requestID, workerID string, horas float64, nota string) error {
	id := uuid.NewString()
	data, _ := json.Marshal(map[string]any{
		"id": id, "requestId": requestID, "workerId": workerID,
		"horas": horas, "nota": nota, "createdAt": time.Now().UTC().Format(time.RFC3339),
	})
	return s.exec(
		`INSERT INTO work_hours (id,request_id,worker_id,horas,data,created_at) VALUES (?,?,?,?,?,?)`,
		id, requestID, workerID, horas, string(data), time.Now().UTC().Format(time.RFC3339))
}

// WorkerTotalHours suma todas las horas registradas de un trabajador.
func (s *Store) WorkerTotalHours(workerID string) float64 {
	var total sql.NullFloat64
	_ = s.queryRow(`SELECT SUM(horas) FROM work_hours WHERE worker_id=?`, workerID).Scan(&total)
	if total.Valid {
		return total.Float64
	}
	return 0
}

// WorkHours devuelve las entradas de horas de una solicitud y el total.
func (s *Store) WorkHours(requestID string) ([]map[string]any, float64, error) {
	rows, err := s.query(
		`SELECT data FROM work_hours WHERE request_id=? ORDER BY created_at DESC`, requestID)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []map[string]any{}
	var total float64
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, 0, err
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(data), &m); err != nil {
			return nil, 0, err
		}
		if h, ok := m["horas"].(float64); ok {
			total += h
		}
		out = append(out, m)
	}
	return out, total, rows.Err()
}

// ---------------- Historial / auditoría ----------------

func (s *Store) AddHistory(h *models.HistoryEntry) error {
	if h.ID == "" {
		h.ID = uuid.NewString()
	}
	if h.CreatedAt.IsZero() {
		h.CreatedAt = time.Now().UTC()
	}
	return s.exec(
		`INSERT INTO history (id,request_id,data,created_at) VALUES (?,?,?,?)`,
		h.ID, h.RequestID, marshal(h), h.CreatedAt.Format(time.RFC3339))
}

func (s *Store) ListHistory(requestID string) ([]models.HistoryEntry, error) {
	rows, err := s.query(
		`SELECT data FROM history WHERE request_id=? ORDER BY created_at DESC`, requestID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.HistoryEntry{}
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var h models.HistoryEntry
		if err := json.Unmarshal([]byte(data), &h); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// ---------------- Notificaciones: estado de lectura ----------------

// MarkNotificationRead marca una notificación como leída (persistente).
func (s *Store) MarkNotificationRead(id string) error {
	var data string
	if err := s.queryRow(`SELECT data FROM notifications WHERE id=?`, id).Scan(&data); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	var n models.Notification
	if err := json.Unmarshal([]byte(data), &n); err != nil {
		return err
	}
	n.Leida = true
	return s.exec(`UPDATE notifications SET data=? WHERE id=?`, marshal(&n), id)
}

// MarkAllNotificationsRead marca como leídas todas las notificaciones de una
// audiencia. Persiste en la base de datos para que no reaparezcan al refrescar.
func (s *Store) MarkAllNotificationsRead(audience string) error {
	rows, err := s.query(`SELECT id, data FROM notifications WHERE audience=?`, audience)
	if err != nil {
		return err
	}
	type item struct{ id, data string }
	var items []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.id, &it.data); err != nil {
			rows.Close()
			return err
		}
		items = append(items, it)
	}
	rows.Close()
	for _, it := range items {
		var n models.Notification
		if json.Unmarshal([]byte(it.data), &n) == nil && !n.Leida {
			n.Leida = true
			if err := s.exec(`UPDATE notifications SET data=? WHERE id=?`, marshal(&n), it.id); err != nil {
				return err
			}
		}
	}
	return nil
}

// ---------------- Borrados administrativos ----------------

// DeleteCandidate quita un candidato de una solicitud.
func (s *Store) DeleteCandidate(id string) error {
	return s.exec(`DELETE FROM candidates WHERE id=?`, id)
}

// DeleteWorker elimina un trabajador y sus datos asociados (admin).
func (s *Store) DeleteWorker(id string) error {
	_ = s.exec(`DELETE FROM candidates WHERE worker_id=?`, id)
	_ = s.exec(`DELETE FROM work_hours WHERE worker_id=?`, id)
	_ = s.exec(`DELETE FROM ratings WHERE target_id=?`, id)
	_ = s.exec(`DELETE FROM users WHERE data LIKE ?`, `%"workerId":"`+id+`"%`)
	return s.exec(`DELETE FROM workers WHERE id=?`, id)
}

// DeleteCompany elimina una empresa y sus solicitudes (admin).
func (s *Store) DeleteCompany(id string) error {
	// Borrar candidatos/horas/historial de las solicitudes de la empresa.
	reqs, _ := s.ListRequests("")
	for _, rq := range reqs {
		if rq.CompanyID == id {
			_ = s.exec(`DELETE FROM candidates WHERE request_id=?`, rq.ID)
			_ = s.exec(`DELETE FROM work_hours WHERE request_id=?`, rq.ID)
			_ = s.exec(`DELETE FROM history WHERE request_id=?`, rq.ID)
			_ = s.exec(`DELETE FROM requests WHERE id=?`, rq.ID)
		}
	}
	_ = s.exec(`DELETE FROM ratings WHERE target_id=?`, id)
	_ = s.exec(`DELETE FROM users WHERE data LIKE ?`, `%"companyId":"`+id+`"%`)
	return s.exec(`DELETE FROM companies WHERE id=?`, id)
}

// RequestsByCompany devuelve las solicitudes de una empresa (historial).
func (s *Store) RequestsByCompany(companyID string) ([]models.Request, error) {
	all, err := s.ListRequests("")
	if err != nil {
		return nil, err
	}
	out := []models.Request{}
	for _, rq := range all {
		if rq.CompanyID == companyID {
			out = append(out, rq)
		}
	}
	return out, nil
}

// WorkerCompletedJobs cuenta las candidaturas marcadas como realizadas.
func (s *Store) WorkerCompletedJobs(workerID string) int {
	cands, _ := s.CandidatesByWorker(workerID)
	n := 0
	for _, c := range cands {
		if c.Estado == models.CandRealizado {
			n++
		}
	}
	return n
}

// RatingComments devuelve los comentarios de las calificaciones de un objetivo
// (para que alimenten la reputación pública del trabajador/empresa).
func (s *Store) RatingComments(targetID string) []map[string]any {
	rows, err := s.query(
		`SELECT data FROM ratings WHERE target_id=? ORDER BY created_at DESC`, targetID)
	if err != nil {
		return []map[string]any{}
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var data string
		if rows.Scan(&data) != nil {
			continue
		}
		var rt models.Rating
		if json.Unmarshal([]byte(data), &rt) == nil && rt.Comentario != "" {
			out = append(out, map[string]any{
				"stars": rt.Stars, "comentario": rt.Comentario, "rol": rt.RaterRole,
			})
		}
	}
	return out
}

// ---------------- Quejas y aclaraciones ----------------

const upsertComplaint = `INSERT INTO complaints (id,author_user_id,estado,data,created_at) VALUES (?,?,?,?,?)
ON CONFLICT (id) DO UPDATE SET estado=excluded.estado, data=excluded.data`

func (s *Store) CreateComplaint(c *models.Complaint) error {
	if c.ID == "" {
		c.ID = uuid.NewString()
	}
	now := time.Now().UTC()
	if c.CreatedAt.IsZero() {
		c.CreatedAt = now
	}
	c.UpdatedAt = now
	if c.Estado == "" {
		c.Estado = models.ComplaintAbierta
	}
	return s.exec(upsertComplaint, c.ID, c.AuthorUserID, c.Estado, marshal(c),
		c.CreatedAt.Format(time.RFC3339))
}

func (s *Store) GetComplaint(id string) (*models.Complaint, error) {
	var data string
	if err := s.queryRow(`SELECT data FROM complaints WHERE id=?`, id).Scan(&data); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	var c models.Complaint
	return &c, json.Unmarshal([]byte(data), &c)
}

// ListComplaints: si authorUserID == "" devuelve todas (admin); si no, las del autor.
func (s *Store) ListComplaints(authorUserID string) ([]models.Complaint, error) {
	q := `SELECT data FROM complaints`
	var args []any
	if authorUserID != "" {
		q += ` WHERE author_user_id=?`
		args = append(args, authorUserID)
	}
	q += ` ORDER BY created_at DESC`
	rows, err := s.query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.Complaint{}
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var c models.Complaint
		if err := json.Unmarshal([]byte(data), &c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
