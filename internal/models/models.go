// Package models define las entidades del dominio de neXus.
//
// neXus conecta trabajadores y empresas de oficios. El backend NO actúa como
// empleador ni procesa pagos: solo gestiona la conexión entre ambas partes,
// con un administrador interno que filtra trabajadores y arma listas de
// candidatos (ver requerimientos del MVP).
package models

import "time"

// Estados del trabajador (requerimientos: "Estados necesarios").
const (
	WorkerDisponible   = "disponible"
	WorkerNoDisponible = "no_disponible"
	WorkerInvitado     = "invitado"
	WorkerConfirmado   = "confirmado"
	WorkerAsignado     = "asignado"
)

// Estados de la solicitud de personal.
const (
	RequestNueva            = "nueva"
	RequestEnRevision       = "en_revision"
	RequestEnProceso        = "en_proceso"
	RequestCandidatosEnvia  = "candidatos_enviados"
	RequestCerrada          = "cerrada"
	RequestCancelada        = "cancelada"
)

// Worker es el perfil laboral de un trabajador registrado.
type Worker struct {
	ID                string    `json:"id"`
	NombreCompleto    string    `json:"nombreCompleto"`
	Telefono          string    `json:"telefono"`
	Correo            string    `json:"correo"`
	Ciudad            string    `json:"ciudad"`
	Pais              string    `json:"pais"`
	Idiomas           []string  `json:"idiomas"`
	OficioPrincipal   string    `json:"oficioPrincipal"`
	AniosExperiencia  int       `json:"aniosExperiencia"`
	Habilidades       []string  `json:"habilidades"`
	Certificaciones   []string  `json:"certificaciones"`
	Licencias         []string  `json:"licencias"`
	Disponibilidad    string    `json:"disponibilidad"` // disponible / no_disponible
	FotoURL           string    `json:"fotoUrl,omitempty"`
	Estado            string    `json:"estado"` // estado operativo (invitado/confirmado/...)
	CreatedAt         time.Time `json:"createdAt"`
	UpdatedAt         time.Time `json:"updatedAt"`
}

// Company es una empresa o contratista que solicita personal.
type Company struct {
	ID               string    `json:"id"`
	NombreEmpresa    string    `json:"nombreEmpresa"`
	PersonaContacto  string    `json:"personaContacto"`
	Telefono         string    `json:"telefono"`
	Correo           string    `json:"correo"`
	Ciudad           string    `json:"ciudad"`
	TipoIndustria    string    `json:"tipoIndustria"`
	Descripcion      string    `json:"descripcion"`
	MetodoPago       string    `json:"metodoPago,omitempty"`
	CreatedAt        time.Time `json:"createdAt"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

// Request es una solicitud de personal creada por una empresa.
type Request struct {
	ID                      string    `json:"id"`
	CompanyID               string    `json:"companyId"`
	TipoTrabajador          string    `json:"tipoTrabajador"`
	CantidadTrabajadores    int       `json:"cantidadTrabajadores"`
	CiudadZona              string    `json:"ciudadZona"`
	FechaInicio             string    `json:"fechaInicio"`
	HoraInicio              string    `json:"horaInicio"`
	DuracionEstimada        string    `json:"duracionEstimada"`
	PagoEstimadoHora        float64   `json:"pagoEstimadoHora"`
	CertificacionesRequeridas []string `json:"certificacionesRequeridas"`
	DescripcionTrabajo      string    `json:"descripcionTrabajo"`
	Comentarios             string    `json:"comentarios"`
	Estado                  string    `json:"estado"` // nueva / en_revision / ...
	CreatedAt               time.Time `json:"createdAt"`
	UpdatedAt               time.Time `json:"updatedAt"`
}

// Candidate vincula un trabajador a una solicitud dentro de la lista que el
// administrador arma y envía a la empresa.
//
// Importante (requerimiento de privacidad): la empresa solo ve un subconjunto
// de datos. CandidatePublic representa exactamente lo que la empresa puede ver.
type Candidate struct {
	ID        string    `json:"id"`
	RequestID string    `json:"requestId"`
	WorkerID  string    `json:"workerId"`
	Estado    string    `json:"estado"` // confirmado / pendiente
	CreatedAt time.Time `json:"createdAt"`
}

// CandidatePublic son los únicos datos del trabajador visibles para la empresa.
// NO incluye teléfono, correo ni dirección (datos privados).
type CandidatePublic struct {
	CandidateID     string   `json:"candidateId"`
	WorkerID        string   `json:"workerId"`
	Nombre          string   `json:"nombre"`
	Ciudad          string   `json:"ciudad"`
	Oficio          string   `json:"oficio"`
	Experiencia     int      `json:"experiencia"`
	Certificaciones []string `json:"certificaciones"`
	Estado          string   `json:"estado"` // confirmado / pendiente
}

// Notification es una notificación directa (push interno, NO WhatsApp API).
type Notification struct {
	ID        string    `json:"id"`
	Audience  string    `json:"audience"` // worker:<id> | company:<id> | admin | all
	Titulo    string    `json:"titulo"`
	Cuerpo    string    `json:"cuerpo"`
	Tipo      string    `json:"tipo"` // info / oportunidad / estado / candidatos
	Leida     bool      `json:"leida"`
	CreatedAt time.Time `json:"createdAt"`
}

// DisclaimerLegal es el aviso obligatorio del MVP (requerimiento legal básico).
const DisclaimerLegal = "neXus funciona como plataforma de conexión entre trabajadores y empresas. " +
	"neXus no actúa como empleador, no procesa pagos, no garantiza contratación y no administra nómina. " +
	"Los acuerdos laborales, pagos y condiciones finales son responsabilidad directa entre empresa y trabajador."
