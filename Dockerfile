# ---- build ----
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Binario estático (todas las dependencias son puro-Go: modernc, pgx, minio, fpdf).
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /nexus .

# ---- runtime ----
FROM alpine:3.20
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 nexus
WORKDIR /app
COPY --from=build /nexus /app/nexus

# Por defecto guarda archivos en /data/uploads (monta un Volume de Railway ahí).
ENV PORT=8080 \
    UPLOAD_DIR=/data/uploads
RUN mkdir -p /data/uploads && chown -R nexus /data
USER nexus

EXPOSE 8080
ENTRYPOINT ["/app/nexus"]
