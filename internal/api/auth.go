package api

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"

	"github.com/BrayanGP/nexus-backend/internal/auth"
	"github.com/BrayanGP/nexus-backend/internal/models"
	"github.com/BrayanGP/nexus-backend/internal/store"
)

type ctxKey string

const userCtxKey ctxKey = "user"

// AuthUser es el usuario autenticado que viaja en el contexto de la petición.
type AuthUser struct {
	ID        string
	Role      string
	WorkerID  string
	CompanyID string
}

func (u *AuthUser) IsAdmin() bool { return u.Role == models.RoleAdmin }

func (s *Server) currentUser(r *http.Request) *AuthUser {
	u, _ := r.Context().Value(userCtxKey).(*AuthUser)
	return u
}

// withAuth exige un token válido. Si falta o es inválido responde 401.
func (s *Server) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tokenStr := bearerToken(r)
		if tokenStr == "" {
			writeErr(w, http.StatusUnauthorized, "falta el token de sesión")
			return
		}
		claims, err := auth.ParseToken(tokenStr, s.secret)
		if err != nil {
			writeErr(w, http.StatusUnauthorized, "sesión inválida o expirada")
			return
		}
		u := &AuthUser{
			ID:        claims.Subject,
			Role:      claims.Role,
			WorkerID:  claims.WorkerID,
			CompanyID: claims.CompanyID,
		}
		ctx := context.WithValue(r.Context(), userCtxKey, u)
		next(w, r.WithContext(ctx))
	}
}

// requireRole exige que el usuario autenticado tenga uno de los roles dados.
func (s *Server) requireRole(role string, next http.HandlerFunc) http.HandlerFunc {
	return s.withAuth(func(w http.ResponseWriter, r *http.Request) {
		if s.currentUser(r).Role != role {
			writeErr(w, http.StatusForbidden, "no autorizado")
			return
		}
		next(w, r)
	})
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(strings.ToLower(h), "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	// Fallback para EventSource (SSE), que no permite cabeceras personalizadas.
	return r.URL.Query().Get("token")
}

// ---------------- handlers ----------------

type registerReq struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Role     string `json:"role"`
	Nombre   string `json:"nombre"`
}

type authResp struct {
	Token string       `json:"token"`
	User  *models.User `json:"user"`
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	var req registerReq
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "JSON inválido")
		return
	}
	req.Email = strings.TrimSpace(req.Email)
	if req.Email == "" || len(req.Password) < 6 || req.Nombre == "" {
		writeErr(w, http.StatusBadRequest, "email, nombre y contraseña (mín. 6) son obligatorios")
		return
	}
	// El admin no se auto-registra; solo trabajador o empresa.
	if req.Role != models.RoleWorker && req.Role != models.RoleCompany {
		writeErr(w, http.StatusBadRequest, "rol inválido (worker o company)")
		return
	}

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	user := &models.User{Email: req.Email, PasswordHash: hash, Role: req.Role}

	// Crear el perfil asociado.
	switch req.Role {
	case models.RoleWorker:
		wk := &models.Worker{
			NombreCompleto: req.Nombre, Correo: req.Email,
			Disponibilidad: models.WorkerDisponible, Estado: models.WorkerDisponible,
		}
		if err := s.store.CreateWorker(wk); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		user.WorkerID = wk.ID
	case models.RoleCompany:
		co := &models.Company{
			NombreEmpresa: req.Nombre, PersonaContacto: req.Nombre, Correo: req.Email,
		}
		if err := s.store.CreateCompany(co); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		user.CompanyID = co.ID
	}

	if err := s.store.CreateUser(user); err != nil {
		if errors.Is(err, store.ErrEmailTaken) {
			writeErr(w, http.StatusConflict, "el correo ya está registrado")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.issueToken(w, user, http.StatusCreated)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "JSON inválido")
		return
	}
	user, err := s.store.GetUserByEmail(req.Email)
	if err != nil || !auth.CheckPassword(user.PasswordHash, req.Password) {
		writeErr(w, http.StatusUnauthorized, "correo o contraseña incorrectos")
		return
	}
	s.issueToken(w, user, http.StatusOK)
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	user, err := s.store.GetUserByID(s.currentUser(r).ID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "usuario no encontrado")
		return
	}
	writeJSON(w, http.StatusOK, user)
}

// forgotPassword genera un código de 6 dígitos y lo envía por correo.
// Siempre responde 200 para no revelar si el correo existe.
func (s *Server) forgotPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string `json:"email"`
	}
	if err := decode(r, &req); err != nil || req.Email == "" {
		writeErr(w, http.StatusBadRequest, "email requerido")
		return
	}
	ok := map[string]string{"message": "Si el correo existe, te enviamos un código."}
	user, err := s.store.GetUserByEmail(req.Email)
	if err != nil {
		writeJSON(w, http.StatusOK, ok) // no revelar inexistencia
		return
	}
	code := fmt.Sprintf("%06d", rand.IntN(1000000))
	_ = s.store.SetReset(user.Email, code, time.Now().Add(15*time.Minute))
	go s.mailer.Send(user.Email, "neXus · Código de recuperación",
		"Tu código para restablecer la contraseña es: "+code+
			"\n\nVence en 15 minutos. Si no lo solicitaste, ignora este correo.\n\n— neXus")
	writeJSON(w, http.StatusOK, ok)
}

// resetPassword valida el código y cambia la contraseña.
func (s *Server) resetPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Code     string `json:"code"`
		Password string `json:"password"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "datos inválidos")
		return
	}
	if len(req.Password) < 6 {
		writeErr(w, http.StatusBadRequest, "la contraseña debe tener al menos 6 caracteres")
		return
	}
	code, expires, err := s.store.GetReset(req.Email)
	if err != nil || code != strings.TrimSpace(req.Code) {
		writeErr(w, http.StatusBadRequest, "código inválido")
		return
	}
	if time.Now().After(expires) {
		writeErr(w, http.StatusBadRequest, "el código expiró, solicita uno nuevo")
		return
	}
	user, err := s.store.GetUserByEmail(req.Email)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "código inválido")
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.store.UpdateUserPassword(user.ID, hash); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.store.DeleteReset(req.Email)
	writeJSON(w, http.StatusOK, map[string]string{"message": "Contraseña actualizada. Inicia sesión."})
}

func (s *Server) issueToken(w http.ResponseWriter, user *models.User, status int) {
	token, err := auth.GenerateToken(user, s.secret)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, status, authResp{Token: token, User: user})
}
