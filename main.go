// Command nexus-backend es el servidor de la plataforma neXus.
//
// Expone una API REST + stream SSE para conectar trabajadores, empresas y un
// panel de administración interno. Usa SQLite (modernc, puro-Go) y no requiere
// dependencias externas en tiempo de ejecución.
package main

import (
	"log"
	"net/http"
	"os"
	"time"

	"github.com/BrayanGP/nexus-backend/internal/api"
	"github.com/BrayanGP/nexus-backend/internal/models"
	"github.com/BrayanGP/nexus-backend/internal/notify"
	"github.com/BrayanGP/nexus-backend/internal/store"
)

func main() {
	addr := getenv("NEXUS_ADDR", ":8080")
	dbPath := getenv("NEXUS_DB", "nexus.db")

	st, err := store.Open(dbPath)
	if err != nil {
		log.Fatalf("no se pudo abrir la base de datos: %v", err)
	}
	defer st.Close()

	seed(st)

	hub := notify.NewHub()
	srv := api.New(st, hub)

	httpSrv := &http.Server{
		Addr:         addr,
		Handler:      srv.Routes(),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 0, // 0: necesario para el stream SSE de larga duración
		IdleTimeout:  60 * time.Second,
	}

	log.Printf("neXus backend escuchando en %s (db: %s)", addr, dbPath)
	if err := httpSrv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// seed crea datos de demostración la primera vez (idempotente por contenido).
func seed(st *store.Store) {
	workers, _ := st.ListWorkers("", "", "")
	if len(workers) > 0 {
		return // ya hay datos
	}
	log.Println("Sembrando datos de demostración...")

	demoWorkers := []*models.Worker{
		{NombreCompleto: "Carlos Méndez", Telefono: "+1 416 555 0101", Correo: "carlos@example.com",
			Ciudad: "Toronto", Pais: "Canadá", Idiomas: []string{"Español", "Inglés"},
			OficioPrincipal: "Electricista", AniosExperiencia: 8,
			Habilidades: []string{"Instalaciones", "Mantenimiento"}, Certificaciones: []string{"Red Seal"},
			Licencias: []string{"G2"}, Disponibilidad: models.WorkerDisponible},
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
		Telefono: "+1 416 555 0200", Correo: "contacto@maple.example", Ciudad: "Toronto",
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
}
