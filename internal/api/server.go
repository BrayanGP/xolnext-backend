// Package api expone la API REST de neXus y el stream SSE de notificaciones.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"mime/multipart"
	"net/http"
	"time"

	"github.com/BrayanGP/nexus-backend/internal/config"
	"github.com/BrayanGP/nexus-backend/internal/mailer"
	"github.com/BrayanGP/nexus-backend/internal/models"
	"github.com/BrayanGP/nexus-backend/internal/notify"
	"github.com/BrayanGP/nexus-backend/internal/pdfexport"
	"github.com/BrayanGP/nexus-backend/internal/storage"
	"github.com/BrayanGP/nexus-backend/internal/store"
)

type Server struct {
	store   *store.Store
	hub     *notify.Hub
	storage storage.Storage
	mailer  *mailer.Mailer
	cfg     config.Config
	secret  string
}

func New(st *store.Store, hub *notify.Hub, fs storage.Storage, m *mailer.Mailer, cfg config.Config) *Server {
	return &Server{store: st, hub: hub, storage: fs, mailer: m, cfg: cfg, secret: cfg.JWTSecret}
}

// Routes construye el http.Handler con todas las rutas y el middleware CORS.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("GET /api/legal", s.legal)

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
	mux.HandleFunc("GET /api/requests/{id}/candidates.pdf", s.withAuth(s.candidatesPDF))
	mux.HandleFunc("GET /api/requests/{id}/history", s.requireRole(admin, s.requestHistory))

	// Oportunidades del trabajador (solicitudes donde es candidato)
	mux.HandleFunc("GET /api/me/opportunities", s.requireRole(models.RoleWorker, s.myOpportunities))

	// Notificaciones directas (la audiencia se deriva del token)
	mux.HandleFunc("POST /api/notifications", s.requireRole(admin, s.createNotification))
	mux.HandleFunc("GET /api/notifications", s.withAuth(s.listNotifications))
	mux.HandleFunc("GET /api/stream", s.withAuth(s.stream)) // SSE

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
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "service": "nexus-backend", "time": time.Now()})
}

func (s *Server) legal(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"disclaimer": models.DisclaimerLegal})
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
	// Un no-admin no puede asignarse estados operativos (confirmado/asignado…).
	if !u.IsAdmin() {
		wk.Estado = wk.Disponibilidad
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
	if !u.IsAdmin() && rq.CompanyID != u.CompanyID {
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
			"neXus te envió candidatos para tu solicitud "+rq.Folio+" ("+rq.TipoTrabajador+").",
			"candidatos", models.PrioImportante, rq.ID, rq.Folio))
		if co, err := s.store.GetCompany(rq.CompanyID); err == nil {
			go s.mailer.Send(co.Correo, "neXus · Candidatos enviados ("+rq.Folio+")",
				"Hola "+co.PersonaContacto+",\n\nneXus preparó una lista de candidatos para tu solicitud "+
					rq.Folio+" ("+rq.TipoTrabajador+"). Ingresa a la app para revisarla.\n\n— neXus")
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
		go s.mailer.Send(wk.Correo, "neXus · Nueva oportunidad",
			"Hola "+wk.NombreCompleto+",\n\nFuiste incluido como candidato en una solicitud de personal en neXus. "+
				"Mantente disponible; el equipo de neXus dará seguimiento.\n\n— neXus")
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
		Folio          string `json:"folio"`
		TipoTrabajador string `json:"tipoTrabajador"`
		CiudadZona     string `json:"ciudadZona"`
		FechaInicio    string `json:"fechaInicio"`
		RequestEstado  string `json:"requestEstado"`
		MiEstado       string `json:"miEstado"`
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
			Folio: rq.Folio, TipoTrabajador: rq.TipoTrabajador,
			CiudadZona: rq.CiudadZona, FechaInicio: rq.FechaInicio,
			RequestEstado: rq.Estado, MiEstado: c.Estado,
		})
	}
	writeJSON(w, http.StatusOK, out)
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
	h, err := s.store.ListHistory(r.PathValue("id"))
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

	fmt.Fprintf(w, ": conectado a neXus stream (%s)\n\n", aud)
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
