# neXus — Backend

API del proyecto **neXus**: una plataforma que conecta trabajadores de oficios
(construcción, demolición, manufactura, limpieza, logística…) con empresas y
contratistas, con un administrador interno que filtra trabajadores y arma listas
de candidatos.

> ⚖️ neXus funciona como **plataforma de conexión**. No actúa como empleador, no
> procesa pagos, no garantiza contratación y no administra nómina. Los acuerdos
> laborales, pagos y condiciones finales son responsabilidad directa entre
> empresa y trabajador.

## Stack

- **Go** (stdlib `net/http`, router por patrones de Go 1.22+)
- **SQLite** vía [`modernc.org/sqlite`](https://pkg.go.dev/modernc.org/sqlite) — driver **puro-Go**, no requiere cgo ni compilador de C (ideal en Windows).
- **SSE (Server-Sent Events)** para notificaciones directas en tiempo real (lógica tipo mensajería, **sin** la API de WhatsApp).

## Ejecutar

```bash
go mod tidy
go run .
```

Variables de entorno opcionales:

| Variable     | Default     | Descripción                       |
|--------------|-------------|-----------------------------------|
| `NEXUS_ADDR` | `:8080`     | Dirección de escucha              |
| `NEXUS_DB`   | `nexus.db`  | Ruta del archivo SQLite           |

Al primer arranque se siembran datos de demostración (3 trabajadores, 1 empresa
y 1 solicitud) para poder probar el panel de administración de inmediato.

## Endpoints

### Salud y legal
- `GET /api/health` — estado del servicio
- `GET /api/legal` — texto del aviso legal obligatorio

### Trabajadores
- `POST /api/workers` — registro de trabajador
- `GET /api/workers?ciudad=&oficio=&disponibilidad=` — listado con filtros (admin)
- `GET /api/workers/{id}`
- `PATCH /api/workers/{id}/status` — body `{ "estado": "confirmado" }`

### Empresas
- `POST /api/companies` — registro de empresa
- `GET /api/companies`

### Solicitudes de personal
- `POST /api/requests`
- `GET /api/requests?estado=`
- `GET /api/requests/{id}`
- `PATCH /api/requests/{id}/status` — body `{ "estado": "candidatos_enviados" }`

### Candidatos
- `POST /api/requests/{id}/candidates` — el admin agrega un trabajador a la lista
- `GET /api/requests/{id}/candidates` — vista interna (admin)
- `GET /api/requests/{id}/candidates/public` — **vista de la empresa**: solo
  nombre, ciudad, oficio, experiencia, certificaciones y estado. Sin teléfono,
  correo ni dirección.

### Notificaciones directas
- `POST /api/notifications` — body `{ "audience": "worker:<id>", "titulo": "...", "cuerpo": "...", "tipo": "oportunidad" }`
- `GET /api/notifications?audience=admin`
- `GET /api/stream?audience=worker:<id>` — **stream SSE** en vivo

## Estados

**Trabajador:** `disponible`, `no_disponible`, `invitado`, `confirmado`, `asignado`

**Solicitud:** `nueva`, `en_revision`, `en_proceso`, `candidatos_enviados`, `cerrada`, `cancelada`

## Privacidad

La empresa **nunca** ve el directorio completo de trabajadores. Solo recibe la
lista que el administrador confirma y envía, y únicamente con los campos públicos
(ver `/candidates/public`).
