// Package storage abstrae el guardado de archivos (foto de perfil, certificados).
//
// Dos implementaciones con la misma interfaz:
//   - Local: guarda en disco (idealmente un Volume de Railway montado en
//     UPLOAD_DIR) y sirve los archivos vía el propio backend en /files/.
//   - S3: cualquier almacenamiento compatible con S3 (Cloudflare R2, MinIO,
//     AWS…) usando minio-go. Se activa con STORAGE_BACKEND=s3.
package storage

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BrayanGP/nexus-backend/internal/config"
	"github.com/google/uuid"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// Storage guarda un archivo y devuelve su URL pública.
type Storage interface {
	// Save guarda el contenido y devuelve la URL pública para acceder a él.
	Save(ctx context.Context, filename, contentType string, r io.Reader, size int64) (string, error)
	// FileServer devuelve un handler para servir archivos (solo backend local).
	FileServer() (prefix string, h http.Handler, ok bool)
}

// New construye el Storage según la configuración.
func New(cfg config.Config) (Storage, error) {
	if cfg.StorageBackend == "s3" {
		return newS3(cfg)
	}
	return newLocal(cfg)
}

// safeName genera un nombre único conservando la extensión original.
func safeName(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	return uuid.NewString() + ext
}

// ---------------- Local ----------------

type localStorage struct {
	dir     string
	baseURL string
}

func newLocal(cfg config.Config) (*localStorage, error) {
	if err := os.MkdirAll(cfg.UploadDir, 0o755); err != nil {
		return nil, fmt.Errorf("creando UPLOAD_DIR: %w", err)
	}
	return &localStorage{dir: cfg.UploadDir, baseURL: cfg.PublicBaseURL}, nil
}

func (l *localStorage) Save(_ context.Context, filename, _ string, r io.Reader, _ int64) (string, error) {
	name := safeName(filename)
	dst := filepath.Join(l.dir, name)
	f, err := os.Create(dst)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := io.Copy(f, r); err != nil {
		return "", err
	}
	return l.baseURL + "/files/" + name, nil
}

func (l *localStorage) FileServer() (string, http.Handler, bool) {
	return "/files/", http.StripPrefix("/files/", http.FileServer(http.Dir(l.dir))), true
}

// ---------------- S3 / compatible ----------------

type s3Storage struct {
	client *minio.Client
	bucket string
	cfg    config.Config
}

func newS3(cfg config.Config) (*s3Storage, error) {
	if cfg.S3Endpoint == "" || cfg.S3Bucket == "" {
		return nil, fmt.Errorf("S3_ENDPOINT y S3_BUCKET son obligatorios para STORAGE_BACKEND=s3")
	}
	client, err := minio.New(cfg.S3Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.S3AccessKey, cfg.S3SecretKey, ""),
		Secure: cfg.S3UseSSL,
		Region: cfg.S3Region,
	})
	if err != nil {
		return nil, err
	}
	return &s3Storage{client: client, bucket: cfg.S3Bucket, cfg: cfg}, nil
}

func (s *s3Storage) Save(ctx context.Context, filename, contentType string, r io.Reader, size int64) (string, error) {
	name := safeName(filename)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_, err := s.client.PutObject(ctx, s.bucket, name, r, size,
		minio.PutObjectOptions{ContentType: contentType})
	if err != nil {
		return "", err
	}
	// URL pública: si configuraste PUBLIC_BASE_URL para el bucket, úsala; si no,
	// se asume acceso por endpoint/bucket/name.
	scheme := "https"
	if !s.cfg.S3UseSSL {
		scheme = "http"
	}
	return fmt.Sprintf("%s://%s/%s/%s", scheme, s.cfg.S3Endpoint, s.bucket, name), nil
}

func (s *s3Storage) FileServer() (string, http.Handler, bool) {
	return "", nil, false // S3 sirve los archivos directamente
}
