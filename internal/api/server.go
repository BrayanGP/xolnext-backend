// Package api expone la API REST de XolNext y el stream SSE de notificaciones.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"mime/multipart"
	"net/http"
	"sync"
	"time"

	"github.com/BrayanGP/xolnext-backend/internal/config"
	"github.com/BrayanGP/xolnext-backend/internal/mailer"
	"github.com/BrayanGP/xolnext-backend/internal/models"
	"github.com/BrayanGP/xolnext-backend/internal/notify"
	"github.com/BrayanGP/xolnext-backend/internal/pdfexport"
	"github.com/BrayanGP/xolnext-backend/internal/storage"
	"github.com/BrayanGP/xolnext-backend/internal/store"
)

type Server struct {
	store   *store.Store
	hub     *notify.Hub
	storage storage.Storage
	mailer  *mailer.Mailer
	cfg     config.Config
	secret  string

	// Caché en memoria de respuestas calientes (patrón de reciclado de llamadas).
	// El store es un singleton; esta caché evita recalcular agregados costosos
	// como las estadísticas públicas en cada visita a la pantalla de explorar.
	cacheMu sync.RWMutex
	memo    map[string]memoEntry
}

type memoEntry struct {
	value   any
	expires time.Time
}

func New(st *store.Store, hub *notify.Hub, fs storage.Storage, m *mailer.Mailer, cfg config.Config) *Server {
	return &Server{store: st, hub: hub, storage: fs, mailer: m, cfg: cfg, secret: cfg.JWTSecret,
		memo: map[string]memoEntry{}}
}

// memoGet devuelve un valor cacheado si sigue vigente.
func (s *Server) memoGet(key string) (any, bool) {
	s.cacheMu.RLock()
	defer s.cacheMu.RUnlock()
	e, ok := s.memo[key]
	if !ok || time.Now().After(e.expires) {
		return nil, false
	}
	return e.value, true
}

func (s *Server) memoSet(key string, value any, ttl time.Duration) {
	s.cacheMu.Lock()
	s.memo[key] = memoEntry{value: value, expires: time.Now().Add(ttl)}
	s.cacheMu.Unlock()
}

// memoInvalidate borra una clave (tras una escritura que la afecta).
func (s *Server) memoInvalidate(key string) {
	s.cacheMu.Lock()
	delete(s.memo, key)
	s.cacheMu.Unlock()
}

// Routes construye el http.Handler con todas las rutas y el middleware CORS.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("GET /api/legal", s.legal)
	mux.HandleFunc("GET /api/public/stats", s.publicStats) // exploración sin login

	// Autenticación (públicas)
	mux.HandleFunc("POST /api/auth/register", s.register)
	mux.HandleFunc("POST /api/auth/login", s.login)
	mux.HandleFunc("POST /api/auth/forgot", s.forgotPassword)
	mux.HandleFunc("POST /api/auth/reset", s.resetPassword)
	mux.HandleFunc("GET /api/auth/me", s.withAuth(s.me))

	admin := models.RoleAdmin

	// Diagnóstico (solo admin)
	mux.HandleFunc("GET /api/admin/diagnostics", s.requireRole(admin, s.diagnostics))
	mux.HandleFunc("POST /api/admin/mail-test", s.requireRole(admin, s.mailTest))

	// Trabajadores
	mux.HandleFunc("GET /api/workers", s.requireRole(admin, s.listWorkers)) // directorio: solo admin
	mux.HandleFunc("GET /api/workers/{id}", s.withAuth(s.getWorker))        // dueño o admin
	mux.HandleFunc("PATCH /api/workers/{id}/status", s.withAuth(s.updateWorkerStatus))
	mux.HandleFunc("POST /api/workers/{id}/photo", s.withAuth(s.uploadPhoto))
	mux.HandleFunc("POST /api/workers/{id}/certificates", s.withAuth(s.uploadCertificate))
	mux.HandleFunc("PUT /api/workers/{id}", s.withAuth(s.updateWorker)) // dueño o admin

	// Empresas
	mux.HandleFunc("GET /api/companies", s.requireRole(admin, s.listCompanies)) // solo admin
	mux.HandleFunc("GET /api/companies/{id}", s.withAuth(s.getCompany))          // dueño o admin
	mux.HandleFunc("PUT /api/companies/{id}", s.withAuth(s.updateCompany))       // dueño o admin
	mux.HandleFunc("DELETE /api/companies/{id}", s.requireRole(admin, s.deleteCompany))
	mux.HandleFunc("DELETE /api/workers/{id}", s.requireRole(admin, s.deleteWorker))
	mux.HandleFunc("GET /api/companies/{id}/requests", s.withAuth(s.companyRequests)) // historial
	mux.HandleFunc("GET /api/companies/{id}/performance", s.withAuth(s.companyPerformance))

	// Solicitudes
	mux.HandleFunc("POST /api/requests", s.requireRole(models.RoleCompany, s.createRequest))
	mux.HandleFunc("GET /api/requests", s.withAuth(s.listRequests)) // admin: todas / empresa: las suyas
	mux.HandleFunc("GET /api/requests/{id}", s.withAuth(s.getRequest))
	mux.HandleFunc("PATCH /api/requests/{id}/status", s.withAuth(s.updateRequestStatus))

	// Candidatos
	mux.HandleFunc("POST /api/requests/{id}/candidates", s.requireRole(admin, s.addCandidate))
	mux.HandleFunc("GET /api/requests/{id}/candidates", s.requireRole(admin, s.listCandidates))
	mux.HandleFunc("GET /api/requests/{id}/candidates/public", s.withAuth(s.publicCandidates))
	mux.HandleFunc("PATCH /api/requests/{id}/candidates/{cid}", s.withAuth(s.updateCandidate))
	mux.HandleFunc("DELETE /api/requests/{id}/candidates/{cid}", s.requireRole(admin, s.deleteCandidate))
	mux.HandleFunc("GET /api/requests/{id}/candidates.pdf", s.withAuth(s.candidatesPDF))
	mux.HandleFunc("GET /api/requests/{id}/history", s.withAuth(s.requestHistory))

	// Oportunidades del trabajador (solicitudes donde es candidato)
	mux.HandleFunc("GET /api/me/opportunities", s.requireRole(models.RoleWorker, s.myOpportunities))
	mux.HandleFunc("PATCH /api/me/opportunities/{cid}", s.requireRole(models.RoleWorker, s.respondOpportunity))

	// Desempeño del trabajador (rating + horas + comentarios)
	mux.HandleFunc("GET /api/workers/{id}/performance", s.withAuth(s.workerPerformance))

	// Reputación / calificaciones
	mux.HandleFunc("POST /api/ratings", s.withAuth(s.addRating))
	mux.HandleFunc("GET /api/workers/{id}/rating", s.withAuth(s.workerRating))
	mux.HandleFunc("GET /api/companies/{id}/rating", s.withAuth(s.companyRating))

	// Control de horas
	mux.HandleFunc("POST /api/requests/{id}/hours", s.withAuth(s.addHours))
	mux.HandleFunc("GET /api/requests/{id}/hours", s.withAuth(s.listHours))

	// Plan / suscripción de la empresa
	mux.HandleFunc("GET /api/me/plan", s.requireRole(models.RoleCompany, s.myPlan))

	// Notificaciones directas (la audiencia se deriva del token)
	mux.HandleFunc("POST /api/notifications", s.requireRole(admin, s.createNotification))
	mux.HandleFunc("GET /api/notifications", s.withAuth(s.listNotifications))
	mux.HandleFunc("POST /api/notifications/read-all", s.withAuth(s.markAllRead))
	mux.HandleFunc("POST /api/notifications/{id}/read", s.withAuth(s.markNotificationRead))
	mux.HandleFunc("GET /api/stream", s.withAuth(s.stream)) // SSE

	// Quejas y aclaraciones
	mux.HandleFunc("POST /api/complaints", s.withAuth(s.createComplaint))
	mux.HandleFunc("GET /api/complaints", s.withAuth(s.listComplaints))
	mux.HandleFunc("PATCH /api/complaints/{id}", s.requireRole(admin, s.updateComplaint))

	// Servir archivos subidos (solo backend de almacenamiento local/Volume).
	if prefix, h, ok := s.storage.FileServer(); ok {
		mux.Handle(prefix, h)
	}

	return cors(logging(mux))
}

// ---------------- middleware ----------------

func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PATCH,PUT,DELETE,OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s (%s)", r.Method, r.URL.Path, time.Since(start))
	})
}

// ---------------- helpers ----------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func decode(r *http.Request, v any) error {
	return json.NewDecoder(r.Body).Decode(v)
}

// ---------------- handlers ----------------

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "service": "xolnext-backend", "time": time.Now()})
}

func (s *Server) legal(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"disclaimer": models.DisclaimerLegal})
}

// publicStats devuelve estadísticas agregadas (sin datos personales) para la
// pantalla de exploración pública.
func (s *Server) publicStats(w http.ResponseWriter, r *http.Request) {
	if v, ok := s.memoGet("publicStats"); ok {
		writeJSON(w, http.StatusOK, v)
		return
	}
	stats := s.store.PublicStats()
	s.memoSet("publicStats", stats, 60*time.Second)
	writeJSON(w, http.StatusOK, stats)
}

// diagnostics expone configuración no sensible para diagnosticar el entorno.
func (s *Server) diagnostics(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"dbEngine":          s.store.Engine(),
		"emailProvider":     s.mailer.Provider(),
		"mailFrom":          s.mailer.FromAddress(),
		"sendgridConfigured": s.cfg.SendGridKey != "",
		"storageBackend":    s.cfg.StorageBackend,
		"publicBaseURL":     s.cfg.PublicBaseURL,
	})
}

// mailTest envía un correo de prueba síncrono y devuelve el resultado exacto.
func (s *Server) mailTest(w http.ResponseWriter, r *http.Request) {
	var body struct {
		To string `json:"to"`
	}
	if err := decode(r, &body); err != nil || body.To == "" {
		writeErr(w, http.StatusBadRequest, "campo 'to' requerido")
		return
	}
	ok, detail := s.mailer.SendTest(body.To)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       ok,
		"detail":   detail,
		"provider": s.mailer.Provider(),
		"from":     s.mailer.FromAddress(),
	})
}

// updateWorker actualiza el perfil de un trabajador. Solo el dueño o un admin.
func (s *Server) updateWorker(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	u := s.currentUser(r)
	if !u.IsAdmin() && u.WorkerID != id {
		writeErr(w, http.StatusForbidden, "solo puedes editar tu propio perfil")
		return
	}
	existing, err := s.store.GetWorker(id)
	if err != nil {
		s.handleStoreErr(w, err)
		return
	}
	var wk models.Worker
	if err := decode(r, &wk); err != nil {
		writeErr(w, http.StatusBadRequest, "JSON inválido")
		return
	}
	wk.ID = id
	wk.CreatedAt = existing.CreatedAt
	// La foto y los certificados se gestionan SOLO por sus endpoints de subida.
	// Una edición de perfil nunca debe borrarlos: preservamos los existentes.
	wk.FotoURL = existing.FotoURL
	wk.CertificadosArchivos = existing.CertificadosArchivos
	if wk.Disponibilidad == "" {
		wk.Disponibilidad = models.WorkerDisponible
	}
	// El estado operativo (invitado/confirmado/asignado) lo gestiona el admin.
	// Una edición de perfil del trabajador no debe resetearlo: solo refleja la
	// disponibilidad cuando el estado actual NO es operativo.
	if !u.IsAdmin() {
		switch existing.Estado {
		case models.WorkerInvitado, models.WorkerConfirmado, models.WorkerAsignado:
			wk.Estado = existing.Estado
		default:
			wk.Estado = wk.Disponibilidad
		}
	}
	if err := s.store.CreateWorker(&wk); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, wk)
}

func (s *Server) listWorkers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ws, err := s.store.ListWorkers(q.Get("ciudad"), q.Get("oficio"), q.Get("disponibilidad"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ws)
}

func (s *Server) getWorker(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	u := s.currentUser(r)
	if !u.IsAdmin() && u.WorkerID != id {
		writeErr(w, http.StatusForbidden, "no autorizado")
		return
	}
	wk, err := s.store.GetWorker(id)
	if err != nil {
		s.handleStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, wk)
}

func (s *Server) updateWorkerStatus(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	u := s.currentUser(r)
	var body struct {
		Estado string `json:"estado"`
	}
	if err := decode(r, &body); err != nil || body.Estado == "" {
		writeErr(w, http.StatusBadRequest, "estado requerido")
		return
	}
	// El dueño solo puede cambiar su disponibilidad; los estados operativos
	// (invitado/confirmado/asignado) son potestad del admin.
	ownStates := body.Estado == models.WorkerDisponible || body.Estado == models.WorkerNoDisponible
	if !u.IsAdmin() && !(u.WorkerID == id && ownStates) {
		writeErr(w, http.StatusForbidden, "no autorizado para este cambio de estado")
		return
	}
	wk, err := s.store.UpdateWorkerStatus(id, body.Estado)
	if err != nil {
		s.handleStoreErr(w, err)
		return
	}
	s.hub.Broadcast(s.persistNotif("worker:"+wk.ID, "Estado actualizado",
		"Tu estado ahora es: "+body.Estado, "estado"))
	writeJSON(w, http.StatusOK, wk)
}

// updateCompany actualiza los datos de una empresa. Solo el dueño o un admin.
func (s *Server) updateCompany(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	u := s.currentUser(r)
	if !u.IsAdmin() && u.CompanyID != id {
		writeErr(w, http.StatusForbidden, "solo puedes editar tu propia empresa")
		return
	}
	existing, err := s.store.GetCompany(id)
	if err != nil {
		s.handleStoreErr(w, err)
		return
	}
	var c models.Company
	if err := decode(r, &c); err != nil {
		writeErr(w, http.StatusBadRequest, "JSON inválido")
		return
	}
	c.ID = id
	c.CreatedAt = existing.CreatedAt
	if err := s.store.CreateCompany(&c); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) getCompany(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	u := s.currentUser(r)
	if !u.IsAdmin() && u.CompanyID != id {
		writeErr(w, http.StatusForbidden, "no autorizado")
		return
	}
	c, err := s.store.GetCompany(id)
	if err != nil {
		s.handleStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) listCompanies(w http.ResponseWriter, r *http.Request) {
	cs, err := s.store.ListCompanies()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, cs)
}

func (s *Server) createRequest(w http.ResponseWriter, r *http.Request) {
	var rq models.Request
	if err := decode(r, &rq); err != nil {
		writeErr(w, http.StatusBadRequest, "JSON inválido")
		return
	}
	if rq.TipoTrabajador == "" {
		writeErr(w, http.StatusBadRequest, "tipoTrabajador es obligatorio")
		return
	}
	// La solicitud pertenece a la empresa autenticada (no se confía en el body).
	rq.CompanyID = s.currentUser(r).CompanyID

	// Gating por plan: límite de solicitudes activas.
	plan := models.PlanFree
	if co, err := s.store.GetCompany(rq.CompanyID); err == nil && co.Plan != "" {
		plan = co.Plan
	}
	if limit := models.PlanLimit(plan); limit > 0 && s.activeRequests(rq.CompanyID) >= limit {
		writeJSON(w, http.StatusPaymentRequired, map[string]any{
			"error": fmt.Sprintf("Alcanzaste el límite de %d solicitudes activas del plan %s. "+
				"Cierra alguna o sube de plan.", limit, plan),
			"plan": plan, "limit": limit,
		})
		return
	}

	rq.Estado = models.RequestNueva
	if err := s.store.CreateRequest(&rq); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.notifyAdmin("Nueva solicitud "+rq.Folio, fmt.Sprintf("%s · %d x %s en %s",
		rq.Folio, rq.CantidadTrabajadores, rq.TipoTrabajador, rq.CiudadZona), "info")
	writeJSON(w, http.StatusCreated, rq)
}

func (s *Server) listRequests(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(r)
	rs, err := s.store.ListRequests(r.URL.Query().Get("estado"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// La empresa solo ve sus propias solicitudes; el admin las ve todas.
	if !u.IsAdmin() {
		filtered := make([]models.Request, 0, len(rs))
		for _, rq := range rs {
			if rq.CompanyID == u.CompanyID {
				filtered = append(filtered, rq)
			}
		}
		rs = filtered
	}
	writeJSON(w, http.StatusOK, rs)
}

func (s *Server) getRequest(w http.ResponseWriter, r *http.Request) {
	rq, err := s.store.GetRequest(r.PathValue("id"))
	if err != nil {
		s.handleStoreErr(w, err)
		return
	}
	u := s.currentUser(r)
	allowed := u.IsAdmin() || rq.CompanyID == u.CompanyID
	// Un trabajador candidato puede ver TODA la oportunidad antes de aceptar
	// (punto clave del MVP: transparencia total trabajador/empresa/XolNext).
	if !allowed && u.Role == models.RoleWorker && u.WorkerID != "" {
		if exists, _ := s.store.CandidateExists(rq.ID, u.WorkerID); exists {
			allowed = true
		}
	}
	if !allowed {
		writeErr(w, http.StatusForbidden, "no autorizado")
		return
	}
	writeJSON(w, http.StatusOK, rq)
}

func (s *Server) updateRequestStatus(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	u := s.currentUser(r)
	var body struct {
		Estado string `json:"estado"`
	}
	if err := decode(r, &body); err != nil || body.Estado == "" {
		writeErr(w, http.StatusBadRequest, "estado requerido")
		return
	}
	// El admin puede fijar cualquier estado. La empresa dueña solo puede
	// cancelar o cerrar sus propias solicitudes.
	if !u.IsAdmin() {
		existing, err := s.store.GetRequest(id)
		if err != nil {
			s.handleStoreErr(w, err)
			return
		}
		ownAction := body.Estado == models.RequestCancelada ||
			body.Estado == models.RequestCerrada ||
			body.Estado == models.RequestPausada ||
			body.Estado == models.RequestArchivada
		if existing.CompanyID != u.CompanyID || !ownAction {
			writeErr(w, http.StatusForbidden, "no autorizado para este cambio de estado")
			return
		}
	}
	rq, err := s.store.UpdateRequestStatus(id, body.Estado)
	if err != nil {
		s.handleStoreErr(w, err)
		return
	}
	s.recordHistory(id, "Estado de la solicitud: "+body.Estado, s.actorLabel(r))
	// Avisar a la empresa cuando hay candidatos enviados.
	if body.Estado == models.RequestCandidatosEnvia && rq.CompanyID != "" {
		s.hub.Broadcast(s.persistNotifFull("company:"+rq.CompanyID, "Candidatos enviados · "+rq.Folio,
			"XolNext te envió candidatos para tu solicitud "+rq.Folio+" ("+rq.TipoTrabajador+").",
			"candidatos", models.PrioImportante, rq.ID, rq.Folio))
		if co, err := s.store.GetCompany(rq.CompanyID); err == nil {
			go s.mailer.Send(co.Correo, "XolNext · Candidatos enviados ("+rq.Folio+")",
				"Hola "+co.PersonaContacto+",\n\nXolNext preparó una lista de candidatos para tu solicitud "+
					rq.Folio+" ("+rq.TipoTrabajador+"). Ingresa a la app para revisarla.\n\n— XolNext")
		}
	}
	writeJSON(w, http.StatusOK, rq)
}

func (s *Server) addCandidate(w http.ResponseWriter, r *http.Request) {
	requestID := r.PathValue("id")
	var c models.Candidate
	if err := decode(r, &c); err != nil || c.WorkerID == "" {
		writeErr(w, http.StatusBadRequest, "workerId requerido")
		return
	}
	c.RequestID = requestID
	// Evitar agregar el mismo trabajador dos veces a la solicitud.
	if exists, _ := s.store.CandidateExists(requestID, c.WorkerID); exists {
		writeErr(w, http.StatusConflict, "este trabajador ya está agregado a la solicitud")
		return
	}
	if err := s.store.AddCandidate(&c); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Notificar al trabajador que fue invitado a una oportunidad.
	folio := ""
	if rq, err := s.store.GetRequest(requestID); err == nil {
		folio = rq.Folio
	}
	wkNombre := c.WorkerID
	if wk, err := s.store.GetWorker(c.WorkerID); err == nil {
		wkNombre = wk.NombreCompleto
		go s.mailer.Send(wk.Correo, "XolNext · Nueva oportunidad",
			"Hola "+wk.NombreCompleto+",\n\nFuiste incluido como candidato en una solicitud de personal en XolNext. "+
				"Mantente disponible; el equipo de XolNext dará seguimiento.\n\n— XolNext")
	}
	s.hub.Broadcast(s.persistNotifFull("worker:"+c.WorkerID, "Nueva oportunidad",
		"Fuiste incluido como candidato en la solicitud "+folio+".",
		"oportunidad", models.PrioImportante, requestID, folio))
	s.recordHistory(requestID, "Agregó candidato: "+wkNombre, s.actorLabel(r))
	writeJSON(w, http.StatusCreated, c)
}

func (s *Server) listCandidates(w http.ResponseWriter, r *http.Request) {
	cs, err := s.store.ListCandidates(r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, cs)
}

// canAccessRequest indica si el usuario puede ver una solicitud (admin o dueña).
func (s *Server) canAccessRequest(r *http.Request, requestID string) bool {
	u := s.currentUser(r)
	if u.IsAdmin() {
		return true
	}
	rq, err := s.store.GetRequest(requestID)
	return err == nil && rq.CompanyID == u.CompanyID
}

// publicCandidates devuelve SOLO los datos que la empresa puede ver.
func (s *Server) publicCandidates(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.canAccessRequest(r, id) {
		writeErr(w, http.StatusForbidden, "no autorizado")
		return
	}
	cs, err := s.store.PublicCandidates(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, cs)
}

// myOpportunities devuelve las solicitudes donde el trabajador es candidato
// (solo datos no sensibles de la solicitud + su estado como candidato).
func (s *Server) myOpportunities(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(r)
	type opportunity struct {
		CandidateID    string `json:"candidateId"`
		RequestID      string `json:"requestId"`
		CompanyID      string `json:"companyId"`
		Folio          string `json:"folio"`
		TipoTrabajador string `json:"tipoTrabajador"`
		CiudadZona     string `json:"ciudadZona"`
		FechaInicio    string `json:"fechaInicio"`
		HoraInicio     string `json:"horaInicio"`
		PagoEstimado   float64 `json:"pagoEstimadoHora"`
		RequestEstado  string `json:"requestEstado"`
		MiEstado       string `json:"miEstado"`
		MiRespuesta    string `json:"miRespuesta"`
	}
	out := []opportunity{}
	if u.WorkerID == "" {
		writeJSON(w, http.StatusOK, out)
		return
	}
	cands, err := s.store.CandidatesByWorker(u.WorkerID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, c := range cands {
		rq, err := s.store.GetRequest(c.RequestID)
		if err != nil {
			continue
		}
		out = append(out, opportunity{
			CandidateID: c.ID, RequestID: rq.ID, CompanyID: rq.CompanyID,
			Folio: rq.Folio, TipoTrabajador: rq.TipoTrabajador,
			CiudadZona: rq.CiudadZona, FechaInicio: rq.FechaInicio,
			HoraInicio: rq.HoraInicio, PagoEstimado: rq.PagoEstimadoHora,
			RequestEstado: rq.Estado, MiEstado: c.Estado,
			MiRespuesta: c.RespuestaTrabajador,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// respondOpportunity: el trabajador confirma o declina su candidatura.
func (s *Server) respondOpportunity(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(r)
	c, err := s.store.GetCandidate(r.PathValue("cid"))
	if err != nil {
		s.handleStoreErr(w, err)
		return
	}
	if c.WorkerID != u.WorkerID {
		writeErr(w, http.StatusForbidden, "no autorizado")
		return
	}
	var body struct {
		Respuesta string `json:"respuesta"`
	}
	if err := decode(r, &body); err != nil ||
		(body.Respuesta != models.RespConfirmada && body.Respuesta != models.RespDeclinada) {
		writeErr(w, http.StatusBadRequest, "respuesta inválida (confirmada/declinada)")
		return
	}
	c.RespuestaTrabajador = body.Respuesta
	if err := s.store.AddCandidate(c); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Avisar al admin y a la empresa dueña.
	folio, companyID := "", ""
	if rq, err := s.store.GetRequest(c.RequestID); err == nil {
		folio, companyID = rq.Folio, rq.CompanyID
	}
	wkNombre := ""
	if wk, err := s.store.GetWorker(c.WorkerID); err == nil {
		wkNombre = wk.NombreCompleto
	}
	msg := wkNombre + " " + (map[string]string{
		models.RespConfirmada: "confirmó", models.RespDeclinada: "declinó",
	}[body.Respuesta]) + " la oportunidad " + folio + "."
	s.notifyAdmin("Respuesta de candidato", msg, "estado")
	if companyID != "" {
		s.hub.Broadcast(s.persistNotifFull("company:"+companyID, "Respuesta de candidato",
			msg, "estado", models.PrioInformativo, c.RequestID, folio))
	}
	s.recordHistory(c.RequestID, msg, s.actorLabel(r))
	writeJSON(w, http.StatusOK, c)
}

// workerPerformance devuelve el desempeño del trabajador: rating, horas y
// comentarios. Lo ve el admin, la empresa dueña de una solicitud, o el propio.
func (s *Server) workerPerformance(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	u := s.currentUser(r)
	if !u.IsAdmin() && u.WorkerID != id && u.Role != models.RoleCompany {
		writeErr(w, http.StatusForbidden, "no autorizado")
		return
	}
	rs := s.store.RatingSummary(id)
	cands, _ := s.store.CandidatesByWorker(id)
	comentarios := []map[string]any{}
	for _, c := range cands {
		if c.Comentario != "" {
			comentarios = append(comentarios, map[string]any{
				"comentario": c.Comentario, "estado": c.Estado,
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"rating":             rs.Average,
		"ratingCount":        rs.Count,
		"totalHoras":         s.store.WorkerTotalHours(id),
		"trabajosConcluidos": s.store.WorkerCompletedJobs(id),
		"comentarios":        comentarios,
		// Comentarios provenientes de las CALIFICACIONES (alimentan la
		// reputación pública del trabajador — antes no se reflejaban).
		"comentariosCalificacion": s.store.RatingComments(id),
	})
}

// updateCandidate permite a admin o a la empresa dueña aceptar/rechazar y
// comentar un candidato.
func (s *Server) updateCandidate(w http.ResponseWriter, r *http.Request) {
	requestID := r.PathValue("id")
	cid := r.PathValue("cid")
	if !s.canAccessRequest(r, requestID) {
		writeErr(w, http.StatusForbidden, "no autorizado")
		return
	}
	c, err := s.store.GetCandidate(cid)
	if err != nil {
		s.handleStoreErr(w, err)
		return
	}
	if c.RequestID != requestID {
		writeErr(w, http.StatusNotFound, "candidato no encontrado")
		return
	}
	var body struct {
		Estado     string `json:"estado"`
		Comentario string `json:"comentario"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "JSON inválido")
		return
	}
	if body.Estado != "" {
		c.Estado = body.Estado
	}
	if body.Comentario != "" {
		c.Comentario = body.Comentario
	}
	if err := s.store.AddCandidate(c); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	actor := s.actorLabel(r)
	s.recordHistory(requestID, "Candidato marcado: "+c.Estado, actor)
	// Avisar al trabajador si fue aceptado o rechazado.
	if body.Estado == models.CandAceptado || body.Estado == models.CandRechazado {
		folio := ""
		if rq, err := s.store.GetRequest(requestID); err == nil {
			folio = rq.Folio
		}
		prio := models.PrioImportante
		s.hub.Broadcast(s.persistNotifFull("worker:"+c.WorkerID,
			"Actualización de candidatura", "Tu candidatura en "+folio+" fue: "+c.Estado,
			"estado", prio, requestID, folio))
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) requestHistory(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.canAccessRequest(r, id) {
		writeErr(w, http.StatusForbidden, "no autorizado")
		return
	}
	h, err := s.store.ListHistory(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, h)
}

func (s *Server) actorLabel(r *http.Request) string {
	u := s.currentUser(r)
	if usr, err := s.store.GetUserByID(u.ID); err == nil {
		return usr.Email
	}
	return u.Role
}

func (s *Server) recordHistory(requestID, accion, actor string) {
	_ = s.store.AddHistory(&models.HistoryEntry{
		RequestID: requestID, Accion: accion, Actor: actor,
	})
}

func (s *Server) createNotification(w http.ResponseWriter, r *http.Request) {
	var n models.Notification
	if err := decode(r, &n); err != nil || n.Audience == "" {
		writeErr(w, http.StatusBadRequest, "audience requerida")
		return
	}
	if err := s.store.CreateNotification(&n); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.hub.Broadcast(n)
	writeJSON(w, http.StatusCreated, n)
}

// audienceForUser determina a qué notificaciones tiene derecho el usuario.
func audienceForUser(u *AuthUser) string {
	switch u.Role {
	case models.RoleWorker:
		return "worker:" + u.WorkerID
	case models.RoleCompany:
		return "company:" + u.CompanyID
	default:
		return "admin"
	}
}

func (s *Server) listNotifications(w http.ResponseWriter, r *http.Request) {
	aud := audienceForUser(s.currentUser(r))
	ns, err := s.store.ListNotifications(aud)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ns)
}

// stream abre un canal SSE con la audiencia derivada del token.
func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	aud := audienceForUser(s.currentUser(r))
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming no soportado")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch, unsub := s.hub.Subscribe(aud)
	defer unsub()

	fmt.Fprintf(w, ": conectado a XolNext stream (%s)\n\n", aud)
	flusher.Flush()

	ctx := r.Context()
	keepalive := time.NewTicker(25 * time.Second)
	defer keepalive.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case n := <-ch:
			fmt.Fprintf(w, "event: notification\ndata: %s\n\n", notify.Encode(n))
			flusher.Flush()
		case <-keepalive.C:
			fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

// ---------------- archivos: foto y certificados ----------------

const maxUpload = 10 << 20 // 10 MB

// uploadPhoto recibe la foto de perfil del trabajador (campo form "file").
func (s *Server) uploadPhoto(w http.ResponseWriter, r *http.Request) {
	wk, file, header, ok := s.readWorkerUpload(w, r)
	if !ok {
		return
	}
	defer file.Close()
	url, err := s.storage.Save(r.Context(), header.Filename, header.Header.Get("Content-Type"), file, header.Size)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	wk.FotoURL = url
	if err := s.store.CreateWorker(wk); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, wk)
}

// uploadCertificate agrega un archivo de certificación al trabajador.
func (s *Server) uploadCertificate(w http.ResponseWriter, r *http.Request) {
	wk, file, header, ok := s.readWorkerUpload(w, r)
	if !ok {
		return
	}
	defer file.Close()
	url, err := s.storage.Save(r.Context(), header.Filename, header.Header.Get("Content-Type"), file, header.Size)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	wk.CertificadosArchivos = append(wk.CertificadosArchivos, url)
	if err := s.store.CreateWorker(wk); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, wk)
}

// readWorkerUpload valida el trabajador y extrae el archivo del form multipart.
// Solo el dueño del perfil o un admin pueden subir archivos.
func (s *Server) readWorkerUpload(w http.ResponseWriter, r *http.Request) (*models.Worker, multipart.File, *multipart.FileHeader, bool) {
	id := r.PathValue("id")
	u := s.currentUser(r)
	if !u.IsAdmin() && u.WorkerID != id {
		writeErr(w, http.StatusForbidden, "solo puedes subir archivos a tu propio perfil")
		return nil, nil, nil, false
	}
	wk, err := s.store.GetWorker(id)
	if err != nil {
		s.handleStoreErr(w, err)
		return nil, nil, nil, false
	}
	if err := r.ParseMultipartForm(maxUpload); err != nil {
		writeErr(w, http.StatusBadRequest, "archivo inválido o demasiado grande (máx 10MB)")
		return nil, nil, nil, false
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "falta el campo 'file'")
		return nil, nil, nil, false
	}
	return wk, file, header, true
}

// candidatesPDF genera y devuelve el PDF de la lista de candidatos (datos públicos).
func (s *Server) candidatesPDF(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.canAccessRequest(r, id) {
		writeErr(w, http.StatusForbidden, "no autorizado")
		return
	}
	req, err := s.store.GetRequest(id)
	if err != nil {
		s.handleStoreErr(w, err)
		return
	}
	cands, err := s.store.PublicCandidates(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	pdf, err := pdfexport.CandidateList(req, cands)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.recordHistory(id, "Descargó el PDF de candidatos", s.actorLabel(r))
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="candidatos-%s.pdf"`, id))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(pdf)
}

// ---------------- reputación / calificaciones ----------------

func (s *Server) addRating(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(r)
	var body struct {
		RequestID  string `json:"requestId"`
		TargetType string `json:"targetType"`
		TargetID   string `json:"targetId"`
		Stars      int    `json:"stars"`
		Comentario string `json:"comentario"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "JSON inválido")
		return
	}
	if body.Stars < 1 || body.Stars > 5 {
		writeErr(w, http.StatusBadRequest, "stars debe ser de 1 a 5")
		return
	}
	if (body.TargetType != "worker" && body.TargetType != "company") || body.TargetID == "" {
		writeErr(w, http.StatusBadRequest, "targetType/targetId inválidos")
		return
	}
	rating := &models.Rating{
		RequestID: body.RequestID, RaterUserID: u.ID, RaterRole: u.Role,
		TargetType: body.TargetType, TargetID: body.TargetID,
		Stars: body.Stars, Comentario: body.Comentario,
	}
	if err := s.store.AddRating(rating); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, rating)
}

func (s *Server) workerRating(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.store.RatingSummary(r.PathValue("id")))
}

func (s *Server) companyRating(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.store.RatingSummary(r.PathValue("id")))
}

// ---------------- control de horas ----------------

func (s *Server) addHours(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.canAccessRequest(r, id) {
		writeErr(w, http.StatusForbidden, "no autorizado")
		return
	}
	var body struct {
		WorkerID string  `json:"workerId"`
		Horas    float64 `json:"horas"`
		Nota     string  `json:"nota"`
	}
	if err := decode(r, &body); err != nil || body.WorkerID == "" || body.Horas <= 0 {
		writeErr(w, http.StatusBadRequest, "workerId y horas (>0) son obligatorios")
		return
	}
	if err := s.store.AddWorkHours(id, body.WorkerID, body.Horas, body.Nota); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.recordHistory(id, fmt.Sprintf("Registró %.1f h de trabajo", body.Horas), s.actorLabel(r))
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true})
}

func (s *Server) listHours(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.canAccessRequest(r, id) {
		writeErr(w, http.StatusForbidden, "no autorizado")
		return
	}
	entries, total, err := s.store.WorkHours(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entradas": entries, "total": total})
}

// ---------------- plan / suscripción ----------------

func isOpenRequest(estado string) bool {
	switch estado {
	case models.RequestNueva, models.RequestEnRevision, models.RequestEnProceso,
		models.RequestCandidatosEnvia, models.RequestPausada:
		return true
	}
	return false
}

func (s *Server) activeRequests(companyID string) int {
	reqs, _ := s.store.ListRequests("")
	n := 0
	for _, rq := range reqs {
		if rq.CompanyID == companyID && isOpenRequest(rq.Estado) {
			n++
		}
	}
	return n
}

func (s *Server) myPlan(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(r)
	plan := models.PlanFree
	if co, err := s.store.GetCompany(u.CompanyID); err == nil && co.Plan != "" {
		plan = co.Plan
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"plan":           plan,
		"limit":          models.PlanLimit(plan),
		"activeRequests": s.activeRequests(u.CompanyID),
	})
}

// ---------------- borrados administrativos (Ola C) ----------------

func (s *Server) deleteCandidate(w http.ResponseWriter, r *http.Request) {
	requestID := r.PathValue("id")
	cid := r.PathValue("cid")
	c, err := s.store.GetCandidate(cid)
	if err != nil {
		s.handleStoreErr(w, err)
		return
	}
	if c.RequestID != requestID {
		writeErr(w, http.StatusNotFound, "candidato no encontrado")
		return
	}
	if err := s.store.DeleteCandidate(cid); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	wkNombre := c.WorkerID
	if wk, err := s.store.GetWorker(c.WorkerID); err == nil {
		wkNombre = wk.NombreCompleto
	}
	s.recordHistory(requestID, "Quitó candidato: "+wkNombre, s.actorLabel(r))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) deleteWorker(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.store.GetWorker(id); err != nil {
		s.handleStoreErr(w, err)
		return
	}
	if err := s.store.DeleteWorker(id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.memoInvalidate("publicStats")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) deleteCompany(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.store.GetCompany(id); err != nil {
		s.handleStoreErr(w, err)
		return
	}
	if err := s.store.DeleteCompany(id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.memoInvalidate("publicStats")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// companyRequests devuelve el historial de solicitudes de una empresa
// (admin, o la propia empresa dueña).
func (s *Server) companyRequests(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	u := s.currentUser(r)
	if !u.IsAdmin() && u.CompanyID != id {
		writeErr(w, http.StatusForbidden, "no autorizado")
		return
	}
	reqs, err := s.store.RequestsByCompany(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, reqs)
}

// companyPerformance: vista rápida del desempeño de la empresa (Ola F).
func (s *Server) companyPerformance(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	u := s.currentUser(r)
	if !u.IsAdmin() && u.CompanyID != id {
		writeErr(w, http.StatusForbidden, "no autorizado")
		return
	}
	rs := s.store.RatingSummary(id)
	reqs, _ := s.store.RequestsByCompany(id)
	contratos := 0
	trabajadores := 0
	for _, rq := range reqs {
		if rq.Estado == models.RequestCerrada {
			contratos++
		}
		cands, _ := s.store.ListCandidates(rq.ID)
		for _, c := range cands {
			if c.Estado == models.CandAceptado || c.Estado == models.CandRealizado {
				trabajadores++
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"rating":                rs.Average,
		"ratingCount":           rs.Count,
		"solicitudes":           len(reqs),
		"contratosConcluidos":   contratos,
		"trabajadoresContratados": trabajadores,
		"comentarios":           s.store.RatingComments(id),
	})
}

// ---------------- notificaciones: marcar leídas (Ola B) ----------------

func (s *Server) markAllRead(w http.ResponseWriter, r *http.Request) {
	aud := audienceForUser(s.currentUser(r))
	if err := s.store.MarkAllNotificationsRead(aud); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) markNotificationRead(w http.ResponseWriter, r *http.Request) {
	if err := s.store.MarkNotificationRead(r.PathValue("id")); err != nil {
		s.handleStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------------- quejas y aclaraciones (Ola G) ----------------

func (s *Server) createComplaint(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(r)
	var body struct {
		RequestID string `json:"requestId"`
		Folio     string `json:"folio"`
		Categoria string `json:"categoria"`
		Asunto    string `json:"asunto"`
		Mensaje   string `json:"mensaje"`
	}
	if err := decode(r, &body); err != nil || body.Asunto == "" || body.Mensaje == "" {
		writeErr(w, http.StatusBadRequest, "asunto y mensaje son obligatorios")
		return
	}
	nombre := s.actorLabel(r)
	c := &models.Complaint{
		AuthorUserID: u.ID, AuthorRole: u.Role, AuthorNombre: nombre,
		RequestID: body.RequestID, Folio: body.Folio, Categoria: body.Categoria,
		Asunto: body.Asunto, Mensaje: body.Mensaje, Estado: models.ComplaintAbierta,
	}
	if err := s.store.CreateComplaint(c); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.notifyAdmin("Nueva queja/aclaración", body.Asunto+" — "+nombre, "info")
	writeJSON(w, http.StatusCreated, c)
}

func (s *Server) listComplaints(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(r)
	author := u.ID
	if u.IsAdmin() {
		author = "" // el admin ve todas
	}
	cs, err := s.store.ListComplaints(author)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, cs)
}

func (s *Server) updateComplaint(w http.ResponseWriter, r *http.Request) {
	c, err := s.store.GetComplaint(r.PathValue("id"))
	if err != nil {
		s.handleStoreErr(w, err)
		return
	}
	var body struct {
		Estado         string `json:"estado"`
		RespuestaAdmin string `json:"respuestaAdmin"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "JSON inválido")
		return
	}
	if body.Estado != "" {
		c.Estado = body.Estado
	}
	if body.RespuestaAdmin != "" {
		c.RespuestaAdmin = body.RespuestaAdmin
	}
	if err := s.store.CreateComplaint(c); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Avisar al autor de la queja.
	s.hub.Broadcast(s.persistNotif(audienceForRole(c.AuthorRole, c.AuthorUserID, s),
		"Actualización de tu queja", c.Asunto+": "+c.Estado, "estado"))
	writeJSON(w, http.StatusOK, c)
}

// audienceForRole resuelve la audiencia de notificación del autor de una queja.
func audienceForRole(role, userID string, s *Server) string {
	if usr, err := s.store.GetUserByID(userID); err == nil {
		switch role {
		case models.RoleWorker:
			return "worker:" + usr.WorkerID
		case models.RoleCompany:
			return "company:" + usr.CompanyID
		}
	}
	return "admin"
}

// ---------------- utilidades internas ----------------

func (s *Server) handleStoreErr(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "no encontrado")
		return
	}
	writeErr(w, http.StatusInternalServerError, err.Error())
}

// persistNotif guarda una notificación simple (prioridad informativa).
func (s *Server) persistNotif(audience, titulo, cuerpo, tipo string) models.Notification {
	return s.persistNotifFull(audience, titulo, cuerpo, tipo, defaultPriority(tipo), "", "")
}

// persistNotifFull guarda una notificación con prioridad y referencia a solicitud.
func (s *Server) persistNotifFull(audience, titulo, cuerpo, tipo, prioridad, requestID, folio string) models.Notification {
	n := models.Notification{
		Audience: audience, Titulo: titulo, Cuerpo: cuerpo, Tipo: tipo,
		Prioridad: prioridad, RequestID: requestID, Folio: folio,
	}
	_ = s.store.CreateNotification(&n)
	return n
}

// defaultPriority asigna una prioridad según el tipo de notificación.
func defaultPriority(tipo string) string {
	switch tipo {
	case "candidatos", "oportunidad":
		return models.PrioImportante
	case "estado":
		return models.PrioInformativo
	default:
		return models.PrioInformativo
	}
}

func (s *Server) notifyAdmin(titulo, cuerpo, tipo string) {
	s.hub.Broadcast(s.persistNotif("admin", titulo, cuerpo, tipo))
}
