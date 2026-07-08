// Package config centraliza la configuración por variables de entorno.
//
// Pensado para Railway: en producción Railway inyecta DATABASE_URL, PORT y las
// variables del Volume/S3/SMTP que definas. En local, sin nada configurado,
// usa SQLite y almacenamiento en disco, por lo que arranca sin dependencias.
package config

import (
	"os"
	"strings"
)

type Config struct {
	Addr        string // dirección de escucha, ej ":8080"
	DatabaseURL string // vacío => SQLite local; postgres://... => Postgres
	SQLitePath  string // ruta del archivo SQLite cuando no hay DATABASE_URL
	JWTSecret   string // clave para firmar los tokens de sesión

	// Almacenamiento de archivos (foto de perfil, certificados).
	StorageBackend string // "local" (default) | "s3"
	UploadDir      string // carpeta local (idealmente un Volume de Railway)
	PublicBaseURL  string // URL pública del backend, para construir links de archivos

	// S3 / compatible (Cloudflare R2, MinIO…). Solo si StorageBackend=s3.
	S3Endpoint  string
	S3Region    string
	S3Bucket    string
	S3AccessKey string
	S3SecretKey string
	S3UseSSL    bool

	// Correo (opcional). Preferimos la Web API de SendGrid (HTTPS), porque
	// muchos PaaS (Railway incluido) bloquean los puertos SMTP salientes.
	SendGridKey string // SENDGRID_API_KEY → usa la Web API por HTTPS
	MailFrom    string // remitente verificado, ej "XolNext <brayangp20@gmail.com>"

	// SMTP como alternativa si no hay SENDGRID_API_KEY.
	SMTPHost string
	SMTPPort string
	SMTPUser string
	SMTPPass string
	SMTPFrom string
}

func Load() Config {
	c := Config{
		Addr:           ":" + env("PORT", "8080"),
		DatabaseURL:    os.Getenv("DATABASE_URL"),
		SQLitePath:     env("XOLNEXT_DB", "xolnext.db"),
		JWTSecret:      os.Getenv("JWT_SECRET"),
		StorageBackend: env("STORAGE_BACKEND", "local"),
		UploadDir:      env("UPLOAD_DIR", "uploads"),
		PublicBaseURL:  strings.TrimRight(os.Getenv("PUBLIC_BASE_URL"), "/"),
		S3Endpoint:     os.Getenv("S3_ENDPOINT"),
		S3Region:       env("S3_REGION", "auto"),
		S3Bucket:       os.Getenv("S3_BUCKET"),
		S3AccessKey:    os.Getenv("S3_ACCESS_KEY"),
		S3SecretKey:    os.Getenv("S3_SECRET_KEY"),
		S3UseSSL:       env("S3_USE_SSL", "true") != "false",
		SendGridKey:    os.Getenv("SENDGRID_API_KEY"),
		SMTPHost:       os.Getenv("SMTP_HOST"),
		SMTPPort:       env("SMTP_PORT", "587"),
		SMTPUser:       os.Getenv("SMTP_USER"),
		SMTPPass:       os.Getenv("SMTP_PASS"),
		SMTPFrom:       env("SMTP_FROM", "XolNext <no-reply@nexus.app>"),
	}
	// El remitente: MAIL_FROM si existe, si no SMTP_FROM.
	c.MailFrom = env("MAIL_FROM", c.SMTPFrom)

	// Robustez: si configuraron SendGrid por SMTP (smtp.sendgrid.net, user
	// "apikey", pass = API key) pero NO definieron SENDGRID_API_KEY, usamos esa
	// misma key con la Web API (HTTPS), porque Railway bloquea los puertos SMTP.
	if c.SendGridKey == "" && strings.Contains(strings.ToLower(c.SMTPHost), "sendgrid") &&
		strings.HasPrefix(c.SMTPPass, "SG.") {
		c.SendGridKey = c.SMTPPass
	}
	return c
}

// UsePostgres indica si debemos conectar a Postgres (Railway) en vez de SQLite.
func (c Config) UsePostgres() bool {
	return strings.HasPrefix(c.DatabaseURL, "postgres://") ||
		strings.HasPrefix(c.DatabaseURL, "postgresql://")
}

// EmailEnabled indica si hay un proveedor de correo configurado.
func (c Config) EmailEnabled() bool { return c.SendGridKey != "" || c.SMTPHost != "" }

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
