// Package auth maneja el hash de contraseñas (bcrypt) y los tokens JWT.
package auth

import (
	"errors"
	"time"

	"github.com/BrayanGP/nexus-backend/internal/models"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

var ErrInvalidToken = errors.New("token inválido")

// HashPassword devuelve el hash bcrypt de una contraseña.
func HashPassword(pw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	return string(b), err
}

// CheckPassword verifica una contraseña contra su hash.
func CheckPassword(hash, pw string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}

// Claims son los datos que viajan dentro del JWT.
type Claims struct {
	Role      string `json:"role"`
	WorkerID  string `json:"wid,omitempty"`
	CompanyID string `json:"cid,omitempty"`
	jwt.RegisteredClaims
}

// GenerateToken crea un JWT firmado para el usuario (válido 30 días).
func GenerateToken(u *models.User, secret string) (string, error) {
	claims := Claims{
		Role:      u.Role,
		WorkerID:  u.WorkerID,
		CompanyID: u.CompanyID,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   u.ID,
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(30 * 24 * time.Hour)),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
}

// ParseToken valida un JWT y devuelve sus claims.
func ParseToken(tokenStr, secret string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &Claims{}, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, ErrInvalidToken
		}
		return []byte(secret), nil
	})
	if err != nil {
		return nil, err
	}
	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, ErrInvalidToken
	}
	return claims, nil
}
