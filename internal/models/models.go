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
	RequestPausada          = "pausada"
	RequestCerrada          = "cerrada"
	RequestCancelada        = "cancelada"
	RequestArchivada        = "archivada"
)

// Estados de un candidato dentro de una solicitud.
const (
	CandPendiente = "pendiente"
	CandAceptado  = "aceptado"
	CandRechazado = "rechazado"
	CandRealizado = "realizado"
)

// Prioridades de las notificaciones.
const (
	PrioUrgente     = "urgente"
	PrioImportante  = "importante"
	PrioInformativo = "informativo"
	PrioPromocional = "promocional"
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
	CompetenciasTecnicas  []string `json:"competenciasTecnicas"`  // separadas del oficio principal
	CompetenciasPersonales []string `json:"competenciasPersonales"`
	Certificaciones   []string  `json:"certificaciones"`
	Licencias         []string  `json:"licencias"`
	Disponibilidad    string    `json:"disponibilidad"` // disponible / no_disponible
	FotoURL           string    `json:"fotoUrl,omitempty"`
	CertificadosArchivos []string `json:"certificadosArchivos,omitempty"` // URLs de certificados subidos
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
	Plan             string    `json:"plan"` // free | pro | business
	CreatedAt        time.Time `json:"createdAt"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

// Planes de suscripción y su límite de solicitudes activas (0 = ilimitado).
const (
	PlanFree     = "free"
	PlanPro      = "pro"
	PlanBusiness = "business"
)

// PlanLimit devuelve el máximo de solicitudes activas por plan (0 = ilimitado).
func PlanLimit(plan string) int {
	switch plan {
	case PlanPro:
		return 20
	case PlanBusiness:
		return 0
	default:
		return 3 // free
	}
}

// Request es una solicitud de personal creada por una empresa.
type Request struct {
	ID                      string    `json:"id"`
	Folio                   string    `json:"folio"` // identificador único legible, ej "NX-7K2P9Q"
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
	MetodoPago              string    `json:"metodoPago"`       // efectivo / transferencia / ...
	GeneroRequerido         string    `json:"generoRequerido"`  // opcional: cualquiera / masculino / femenino
	Perfiles                []RequestProfile `json:"perfiles,omitempty"` // varios perfiles en una misma solicitud
	// Información ampliada de la oportunidad (transparencia total: el trabajador
	// debe poder ver TODO antes de aceptar — punto clave del MVP).
	Direccion               string    `json:"direccion"`               // dirección del sitio
	LlegaDirecto            bool      `json:"llegaDirecto"`            // true: llega directo al sitio; false: hay punto de reunión
	PuntoReunion            string    `json:"puntoReunion"`            // lugar de reunión / dónde se recoge al personal
	HoraReunion             string    `json:"horaReunion"`
	PersonaContacto         string    `json:"personaContacto"`
	TelefonoContacto        string    `json:"telefonoContacto"`
	ExperienciaRequerida    string    `json:"experienciaRequerida"`
	Viaticos                string    `json:"viaticos"`                // viáticos: no / incluidos / detalle
	Transporte              string    `json:"transporte"`              // transporte: no / incluido / detalle
	Comida                  string    `json:"comida"`                  // comida: no / incluida / detalle
	EquipoProteccion        string    `json:"equipoProteccion"`        // EPP requerido por la empresa
	HerramientasLleva       string    `json:"herramientasLleva"`       // herramientas que aporta el trabajador
	HerramientasProporciona string    `json:"herramientasProporciona"` // herramientas que da la empresa
	RequisitosAdicionales   string    `json:"requisitosAdicionales"`
	EsConstruccion          bool      `json:"esConstruccion"`          // muestra la nota fija de EPP de obra
	Estado                  string    `json:"estado"` // nueva / en_revision / ...
	CreatedAt               time.Time `json:"createdAt"`
	UpdatedAt               time.Time `json:"updatedAt"`
}

// RequestProfile es un perfil solicitado (tipo + cantidad) dentro de una
// solicitud que puede pedir varios perfiles distintos.
type RequestProfile struct {
	TipoTrabajador string `json:"tipoTrabajador"`
	Cantidad       int    `json:"cantidad"`
}

// Candidate vincula un trabajador a una solicitud dentro de la lista que el
// administrador arma y envía a la empresa.
//
// Importante (requerimiento de privacidad): la empresa solo ve un subconjunto
// de datos. CandidatePublic representa exactamente lo que la empresa puede ver.
type Candidate struct {
	ID                 string    `json:"id"`
	RequestID          string    `json:"requestId"`
	WorkerID           string    `json:"workerId"`
	Estado             string    `json:"estado"`     // decisión de la empresa: pendiente/aceptado/rechazado/realizado
	RespuestaTrabajador string   `json:"respuestaTrabajador"` // del trabajador: pendiente/confirmada/declinada
	Comentario         string    `json:"comentario"` // observaciones de la empresa o admin
	CreatedAt          time.Time `json:"createdAt"`
}

// Respuesta del trabajador a una oportunidad.
const (
	RespPendiente  = "pendiente"
	RespConfirmada = "confirmada"
	RespDeclinada  = "declinada"
)

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
	Estado          string   `json:"estado"`     // pendiente / aceptado / rechazado / realizado
	RespuestaTrabajador string `json:"respuestaTrabajador"` // confirmada/declinada/pendiente
	Comentario      string   `json:"comentario"` // observaciones
	Rating          float64  `json:"rating"`     // promedio de calificaciones del trabajador
	RatingCount     int      `json:"ratingCount"`
	TrabajosConcluidos int   `json:"trabajosConcluidos"` // candidaturas marcadas como realizadas
	TotalHoras      float64  `json:"totalHoras"`
	CompetenciasTecnicas  []string `json:"competenciasTecnicas"`
	CompetenciasPersonales []string `json:"competenciasPersonales"`
	Licencias       []string `json:"licencias"`
	AniosExperiencia int     `json:"aniosExperiencia"`
}

// Complaint es una queja o aclaración levantada por un trabajador o empresa.
// Permite a neXus dar seguimiento formal a incidencias (módulo Quejas y
// Aclaraciones del MVP).
type Complaint struct {
	ID            string    `json:"id"`
	AuthorUserID  string    `json:"authorUserId"`
	AuthorRole    string    `json:"authorRole"`
	AuthorNombre  string    `json:"authorNombre"`
	RequestID     string    `json:"requestId,omitempty"`
	Folio         string    `json:"folio,omitempty"`
	Categoria     string    `json:"categoria"` // pago / seguridad / conducta / horario / otro
	Asunto        string    `json:"asunto"`
	Mensaje       string    `json:"mensaje"`
	Estado        string    `json:"estado"` // abierta / en_revision / resuelta
	RespuestaAdmin string   `json:"respuestaAdmin,omitempty"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// Estados de una queja/aclaración.
const (
	ComplaintAbierta  = "abierta"
	ComplaintRevision = "en_revision"
	ComplaintResuelta = "resuelta"
)

// Notification es una notificación directa (push interno, NO WhatsApp API).
type Notification struct {
	ID        string    `json:"id"`
	Audience  string    `json:"audience"` // worker:<id> | company:<id> | admin | all
	Titulo    string    `json:"titulo"`
	Cuerpo    string    `json:"cuerpo"`
	Tipo      string    `json:"tipo"`      // info / oportunidad / estado / candidatos
	Prioridad string    `json:"prioridad"` // urgente / importante / informativo / promocional
	RequestID string    `json:"requestId,omitempty"`
	Folio     string    `json:"folio,omitempty"`
	Leida     bool      `json:"leida"`
	CreatedAt time.Time `json:"createdAt"`
}

// Rating es una calificación (1-5) que un usuario da a un trabajador o empresa.
type Rating struct {
	ID          string    `json:"id"`
	RequestID   string    `json:"requestId"`
	RaterUserID string    `json:"raterUserId"`
	RaterRole   string    `json:"raterRole"`
	TargetType  string    `json:"targetType"` // worker | company
	TargetID    string    `json:"targetId"`
	Stars       int       `json:"stars"` // 1..5
	Comentario  string    `json:"comentario"`
	CreatedAt   time.Time `json:"createdAt"`
}

// RatingSummary es el promedio y conteo de calificaciones de un objetivo.
type RatingSummary struct {
	Average float64 `json:"average"`
	Count   int     `json:"count"`
}

// HistoryEntry registra una acción sobre una solicitud (auditoría).
type HistoryEntry struct {
	ID        string    `json:"id"`
	RequestID string    `json:"requestId"`
	Accion    string    `json:"accion"` // texto legible de la acción
	Actor     string    `json:"actor"`  // quién la hizo (email o rol)
	CreatedAt time.Time `json:"createdAt"`
}

// Roles de usuario.
const (
	RoleWorker  = "worker"
	RoleCompany = "company"
	RoleAdmin   = "admin"
)

// User es una cuenta con la que se inicia sesión. Queda enlazada al perfil de
// trabajador o de empresa según su rol.
type User struct {
	ID           string    `json:"id"`
	Email        string    `json:"email"`
	PasswordHash string    `json:"-"` // nunca se serializa hacia el cliente
	Role         string    `json:"role"`
	WorkerID     string    `json:"workerId,omitempty"`
	CompanyID    string    `json:"companyId,omitempty"`
	CreatedAt    time.Time `json:"createdAt"`
}

// DisclaimerLegal es el aviso obligatorio del MVP (requerimiento legal básico).
const DisclaimerLegal = "neXus funciona como plataforma de conexión entre trabajadores y empresas. " +
	"neXus no actúa como empleador, no procesa pagos, no garantiza contratación y no administra nómina. " +
	"Los acuerdos laborales, pagos y condiciones finales son responsabilidad directa entre empresa y trabajador."
