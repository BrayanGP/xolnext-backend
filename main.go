// Command nexus-backend es el servidor de la plataforma neXus.
//
// Expone una API REST + stream SSE para conectar trabajadores, empresas y un
// panel de administración interno.
//
// Configuración por entorno (pensado para Railway):
//   - DATABASE_URL: si es postgres://… usa Postgres; si no, SQLite local.
//   - PORT: puerto de escucha (Railway lo inyecta).
//   - UPLOAD_DIR / Volume: dónde guardar fotos y certificados.
//   - STORAGE_BACKEND=s3 + S3_*: usar bucket S3/R2/MinIO en vez del disco.
//   - SMTP_*: notificaciones por correo (opcional).
package main

import (
	"crypto/rand"
	"encoding/hex"
	"log"
	"net/http"
	"time"

	"github.com/BrayanGP/nexus-backend/internal/api"
	"github.com/BrayanGP/nexus-backend/internal/auth"
	"github.com/BrayanGP/nexus-backend/internal/config"
	"github.com/BrayanGP/nexus-backend/internal/mailer"
	"github.com/BrayanGP/nexus-backend/internal/models"
	"github.com/BrayanGP/nexus-backend/internal/notify"
	"github.com/BrayanGP/nexus-backend/internal/storage"
	"github.com/BrayanGP/nexus-backend/internal/store"
)

func main() {
	cfg := config.Load()

	if cfg.JWTSecret == "" {
		// Sin JWT_SECRET configurado generamos uno temporal: las sesiones no
		// sobreviven a un reinicio. En producción define JWT_SECRET.
		cfg.JWTSecret = randomSecret()
		log.Println("ADVERTENCIA: JWT_SECRET no configurado; usando uno temporal (las sesiones se invalidan al reiniciar).")
	}

	st, err := store.Open(cfg.DatabaseURL, cfg.SQLitePath)
	if err != nil {
		log.Fatalf("no se pudo abrir la base de datos: %v", err)
	}
	defer st.Close()

	fs, err := storage.New(cfg)
	if err != nil {
		log.Fatalf("no se pudo inicializar el almacenamiento: %v", err)
	}

	seed(st)

	hub := notify.NewHub()
	mail := mailer.New(cfg)
	srv := api.New(st, hub, fs, mail, cfg.JWTSecret)

	httpSrv := &http.Server{
		Addr:         cfg.Addr,
		Handler:      srv.Routes(),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 0, // 0: necesario para el stream SSE de larga duración
		IdleTimeout:  60 * time.Second,
	}

	engine := "SQLite"
	if cfg.UsePostgres() {
		engine = "Postgres"
	}
	log.Printf("neXus backend escuchando en %s (db: %s, correo: %v)", cfg.Addr, engine, cfg.EmailEnabled())
	if err := httpSrv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

func randomSecret() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// mkUser crea un usuario con contraseña en claro (solo para semilla).
func mkUser(st *store.Store, email, password, role, workerID, companyID string) {
	hash, err := auth.HashPassword(password)
	if err != nil {
		return
	}
	_ = st.CreateUser(&models.User{
		Email: email, PasswordHash: hash, Role: role,
		WorkerID: workerID, CompanyID: companyID,
	})
}

// seed siembra datos y cuentas de demostración (idempotente).
func seed(st *store.Store) {
	seedDemoData(st)
	ensureAdmin(st)
}

// ensureAdmin garantiza que exista la cuenta de administrador.
func ensureAdmin(st *store.Store) {
	if _, err := st.GetUserByEmail("admin@nexus.app"); err == nil {
		return // ya existe
	}
	mkUser(st, "admin@nexus.app", "admin123", models.RoleAdmin, "", "")
	log.Println("Cuenta admin creada: admin@nexus.app / admin123 (cámbiala).")
}

func seedDemoData(st *store.Store) {
	workers, _ := st.ListWorkers("", "", "")
	if len(workers) > 0 {
		return // ya hay datos
	}
	log.Println("Sembrando datos de demostración...")

	carlos := &models.Worker{NombreCompleto: "Carlos Méndez", Telefono: "+1 416 555 0101",
		Correo: "carlos@example.com", Ciudad: "Toronto", Pais: "Canadá",
		Idiomas: []string{"Español", "Inglés"}, OficioPrincipal: "Electricista", AniosExperiencia: 8,
		Habilidades: []string{"Instalaciones", "Mantenimiento"}, Certificaciones: []string{"Red Seal"},
		Licencias: []string{"G2"}, Disponibilidad: models.WorkerDisponible}
	demoWorkers := []*models.Worker{
		carlos,
		{NombreCompleto: "María López", Telefono: "+1 416 555 0102", Correo: "maria@example.com",
			Ciudad: "Toronto", Pais: "Canadá", Idiomas: []string{"Español"},
			OficioPrincipal: "Limpieza", AniosExperiencia: 5,
			Habilidades: []string{"Limpieza industrial"}, Disponibilidad: models.WorkerDisponible},
		{NombreCompleto: "José Ramírez", Telefono: "+1 905 555 0103", Correo: "jose@example.com",
			Ciudad: "Mississauga", Pais: "Canadá", Idiomas: []string{"Español", "Inglés"},
			OficioPrincipal: "Construcción", AniosExperiencia: 12,
			Habilidades: []string{"Albañilería", "Demolición"}, Certificaciones: []string{"WHMIS"},
			Disponibilidad: models.WorkerNoDisponible},
	}
	for _, w := range demoWorkers {
		_ = st.CreateWorker(w)
	}

	demoCompany := &models.Company{
		NombreEmpresa: "Constructora Maple", PersonaContacto: "Ana Torres",
		Telefono: "+1 416 555 0200", Correo: "empresa@maple.example", Ciudad: "Toronto",
		TipoIndustria: "Construcción", Descripcion: "Proyectos residenciales", MetodoPago: "Transferencia",
	}
	_ = st.CreateCompany(demoCompany)

	demoRequest := &models.Request{
		CompanyID: demoCompany.ID, TipoTrabajador: "Electricista", CantidadTrabajadores: 2,
		CiudadZona: "Toronto", FechaInicio: "2026-06-15", HoraInicio: "08:00",
		DuracionEstimada: "2 semanas", PagoEstimadoHora: 32.5,
		CertificacionesRequeridas: []string{"Red Seal"},
		DescripcionTrabajo:        "Instalación eléctrica en obra residencial",
		Comentarios:               "Traer herramienta propia", Estado: models.RequestNueva,
	}
	_ = st.CreateRequest(demoRequest)

	// Cuentas de demostración para iniciar sesión.
	mkUser(st, "carlos@example.com", "demo1234", models.RoleWorker, carlos.ID, "")
	mkUser(st, "empresa@maple.example", "demo1234", models.RoleCompany, "", demoCompany.ID)
}
