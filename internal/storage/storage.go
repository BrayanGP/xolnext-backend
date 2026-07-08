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

	"github.com/BrayanGP/xolnext-backend/internal/config"
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
	// minio.New espera solo el host; aceptamos también la URL completa.
	endpoint := cfg.S3Endpoint
	secure := cfg.S3UseSSL
	if strings.HasPrefix(endpoint, "https://") {
		endpoint = strings.TrimPrefix(endpoint, "https://")
		secure = true
	} else if strings.HasPrefix(endpoint, "http://") {
		endpoint = strings.TrimPrefix(endpoint, "http://")
		secure = false
	}
	endpoint = strings.TrimRight(endpoint, "/")
	client, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.S3AccessKey, cfg.S3SecretKey, ""),
		Secure: secure,
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
	// Devolvemos una URL servida por el propio backend (proxy). Así los archivos
	// son accesibles aunque el bucket sea privado, y la URL es estable.
	return s.cfg.PublicBaseURL + "/files/" + name, nil
}

// FileServer transmite los objetos del bucket a través del backend, de modo que
// no se requiere que el bucket sea de lectura pública.
func (s *s3Storage) FileServer() (string, http.Handler, bool) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/files/")
		if key == "" {
			http.NotFound(w, r)
			return
		}
		obj, err := s.client.GetObject(r.Context(), s.bucket, key, minio.GetObjectOptions{})
		if err != nil {
			http.Error(w, "no encontrado", http.StatusNotFound)
			return
		}
		defer obj.Close()
		info, err := obj.Stat()
		if err != nil {
			http.Error(w, "no encontrado", http.StatusNotFound)
			return
		}
		if info.ContentType != "" {
			w.Header().Set("Content-Type", info.ContentType)
		}
		w.Header().Set("Cache-Control", "public, max-age=86400")
		_, _ = io.Copy(w, obj)
	})
	return "/files/", h, true
}
