// Package api expone la API REST de neXus y el stream SSE de notificaciones.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/BrayanGP/nexus-backend/internal/models"
	"github.com/BrayanGP/nexus-backend/internal/notify"
	"github.com/BrayanGP/nexus-backend/internal/store"
)

type Server struct {
	store *store.Store
	hub   *notify.Hub
}

func New(st *store.Store, hub *notify.Hub) *Server {
	return &Server{store: st, hub: hub}
}

// Routes construye el http.Handler con todas las rutas y el middleware CORS.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("GET /api/legal", s.legal)

	// Trabajadores
	mux.HandleFunc("POST /api/workers", s.createWorker)
	mux.HandleFunc("GET /api/workers", s.listWorkers) // admin (filtros)
	mux.HandleFunc("GET /api/workers/{id}", s.getWorker)
	mux.HandleFunc("PATCH /api/workers/{id}/status", s.updateWorkerStatus)

	// Empresas
	mux.HandleFunc("POST /api/companies", s.createCompany)
	mux.HandleFunc("GET /api/companies", s.listCompanies)

	// Solicitudes
	mux.HandleFunc("POST /api/requests", s.createRequest)
	mux.HandleFunc("GET /api/requests", s.listRequests) // admin
	mux.HandleFunc("GET /api/requests/{id}", s.getRequest)
	mux.HandleFunc("PATCH /api/requests/{id}/status", s.updateRequestStatus)

	// Candidatos
	mux.HandleFunc("POST /api/requests/{id}/candidates", s.addCandidate) // admin arma lista
	mux.HandleFunc("GET /api/requests/{id}/candidates", s.listCandidates)         // admin (interno)
	mux.HandleFunc("GET /api/requests/{id}/candidates/public", s.publicCandidates) // empresa (limitado)

	// Notificaciones directas
	mux.HandleFunc("POST /api/notifications", s.createNotification)
	mux.HandleFunc("GET /api/notifications", s.listNotifications)
	mux.HandleFunc("GET /api/stream", s.stream) // SSE

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

func (s *Server) createWorker(w http.ResponseWriter, r *http.Request) {
	var wk models.Worker
	if err := decode(r, &wk); err != nil {
		writeErr(w, http.StatusBadRequest, "JSON inválido")
		return
	}
	if wk.NombreCompleto == "" || wk.OficioPrincipal == "" {
		writeErr(w, http.StatusBadRequest, "nombreCompleto y oficioPrincipal son obligatorios")
		return
	}
	if wk.Disponibilidad == "" {
		wk.Disponibilidad = models.WorkerDisponible
	}
	if err := s.store.CreateWorker(&wk); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.notifyAdmin("Nuevo trabajador", fmt.Sprintf("%s (%s) se registró", wk.NombreCompleto, wk.OficioPrincipal), "info")
	writeJSON(w, http.StatusCreated, wk)
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
	wk, err := s.store.GetWorker(r.PathValue("id"))
	if err != nil {
		s.handleStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, wk)
}

func (s *Server) updateWorkerStatus(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Estado string `json:"estado"`
	}
	if err := decode(r, &body); err != nil || body.Estado == "" {
		writeErr(w, http.StatusBadRequest, "estado requerido")
		return
	}
	wk, err := s.store.UpdateWorkerStatus(r.PathValue("id"), body.Estado)
	if err != nil {
		s.handleStoreErr(w, err)
		return
	}
	s.hub.Broadcast(s.persistNotif("worker:"+wk.ID, "Estado actualizado",
		"Tu estado ahora es: "+body.Estado, "estado"))
	writeJSON(w, http.StatusOK, wk)
}

func (s *Server) createCompany(w http.ResponseWriter, r *http.Request) {
	var c models.Company
	if err := decode(r, &c); err != nil {
		writeErr(w, http.StatusBadRequest, "JSON inválido")
		return
	}
	if c.NombreEmpresa == "" || c.PersonaContacto == "" {
		writeErr(w, http.StatusBadRequest, "nombreEmpresa y personaContacto son obligatorios")
		return
	}
	if err := s.store.CreateCompany(&c); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.notifyAdmin("Nueva empresa", c.NombreEmpresa+" se registró", "info")
	writeJSON(w, http.StatusCreated, c)
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
	rq.Estado = models.RequestNueva
	if err := s.store.CreateRequest(&rq); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.notifyAdmin("Nueva solicitud", fmt.Sprintf("Solicitud de %d x %s en %s",
		rq.CantidadTrabajadores, rq.TipoTrabajador, rq.CiudadZona), "info")
	writeJSON(w, http.StatusCreated, rq)
}

func (s *Server) listRequests(w http.ResponseWriter, r *http.Request) {
	rs, err := s.store.ListRequests(r.URL.Query().Get("estado"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rs)
}

func (s *Server) getRequest(w http.ResponseWriter, r *http.Request) {
	rq, err := s.store.GetRequest(r.PathValue("id"))
	if err != nil {
		s.handleStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rq)
}

func (s *Server) updateRequestStatus(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Estado string `json:"estado"`
	}
	if err := decode(r, &body); err != nil || body.Estado == "" {
		writeErr(w, http.StatusBadRequest, "estado requerido")
		return
	}
	rq, err := s.store.UpdateRequestStatus(r.PathValue("id"), body.Estado)
	if err != nil {
		s.handleStoreErr(w, err)
		return
	}
	// Avisar a la empresa cuando hay candidatos enviados.
	if body.Estado == models.RequestCandidatosEnvia && rq.CompanyID != "" {
		s.hub.Broadcast(s.persistNotif("company:"+rq.CompanyID, "Candidatos enviados",
			"neXus te envió una lista de candidatos para tu solicitud.", "candidatos"))
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
	if err := s.store.AddCandidate(&c); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Notificar al trabajador que fue invitado a una oportunidad.
	s.hub.Broadcast(s.persistNotif("worker:"+c.WorkerID, "Nueva oportunidad",
		"Fuiste incluido como candidato en una solicitud.", "oportunidad"))
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

// publicCandidates devuelve SOLO los datos que la empresa puede ver.
func (s *Server) publicCandidates(w http.ResponseWriter, r *http.Request) {
	cs, err := s.store.PublicCandidates(r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, cs)
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

func (s *Server) listNotifications(w http.ResponseWriter, r *http.Request) {
	aud := r.URL.Query().Get("audience")
	if aud == "" {
		aud = "admin"
	}
	ns, err := s.store.ListNotifications(aud)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ns)
}

// stream abre un canal SSE: GET /api/stream?audience=worker:<id>
func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	aud := r.URL.Query().Get("audience")
	if aud == "" {
		aud = "admin"
	}
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

// ---------------- utilidades internas ----------------

func (s *Server) handleStoreErr(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "no encontrado")
		return
	}
	writeErr(w, http.StatusInternalServerError, err.Error())
}

// persistNotif guarda la notificación y la devuelve para difundirla.
func (s *Server) persistNotif(audience, titulo, cuerpo, tipo string) models.Notification {
	n := models.Notification{Audience: audience, Titulo: titulo, Cuerpo: cuerpo, Tipo: tipo}
	_ = s.store.CreateNotification(&n)
	return n
}

func (s *Server) notifyAdmin(titulo, cuerpo, tipo string) {
	s.hub.Broadcast(s.persistNotif("admin", titulo, cuerpo, tipo))
}
