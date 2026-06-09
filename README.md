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

### Autenticación (JWT)
- `POST /api/auth/register` — body `{ email, password, role: "worker"|"company", nombre }` → crea cuenta + perfil y devuelve `{ token, user }`
- `POST /api/auth/login` — body `{ email, password }` → `{ token, user }`
- `GET /api/auth/me` — usuario actual (requiere `Authorization: Bearer <token>`)

Todos los endpoints siguientes requieren token. Autorización: el **directorio de
trabajadores y el panel** son solo de `admin`; cada trabajador/empresa solo puede
ver y editar **lo suyo**. Define `JWT_SECRET` en producción. Cuenta admin inicial
sembrada: **admin@nexus.app / admin123** (cámbiala).

### Trabajadores
- `POST /api/workers` — registro de trabajador
- `GET /api/workers?ciudad=&oficio=&disponibilidad=` — listado con filtros (admin)
- `GET /api/workers/{id}`
- `PATCH /api/workers/{id}/status` — body `{ "estado": "confirmado" }`
- `POST /api/workers/{id}/photo` — subir foto de perfil (multipart, campo `file`)
- `POST /api/workers/{id}/certificates` — subir certificado como archivo (multipart, campo `file`)

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
- `GET /api/requests/{id}/candidates.pdf` — exporta la lista de candidatos en PDF
  (solo datos públicos).

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

## Despliegue en Railway

El repo incluye `Dockerfile` y `railway.json` (healthcheck en `/api/health`).

1. **Crea el servicio** en Railway apuntando a este repo (detecta el Dockerfile).
2. **Postgres:** agrega un plugin Postgres en el mismo proyecto. Railway expone
   `DATABASE_URL`; referénciala en el servicio del backend
   (`DATABASE_URL=${{Postgres.DATABASE_URL}}`). Sin ella, usa SQLite.
3. **Volume:** monta un Volume en el servicio con ruta `/data` (el Dockerfile ya
   usa `UPLOAD_DIR=/data/uploads`) para que fotos y certificados persistan.
4. **PUBLIC_BASE_URL:** ponla con la URL pública del backend
   (`https://<servicio>.up.railway.app`) para que los links de archivos sean válidos.
5. *(Opcional)* **S3:** `STORAGE_BACKEND=s3` + `S3_*` para usar un bucket en vez del Volume.
6. *(Opcional)* **Correo:** `SMTP_*` para enviar notificaciones por correo.

Todas las variables están documentadas en [`.env.example`](.env.example).

> El backend genera datos de demostración solo si la base está vacía.
