// Package mailer envía notificaciones por correo vía SMTP.
//
// Es OPCIONAL: si no hay SMTP_HOST configurado, Send no hace nada y no falla,
// para que el backend funcione igual sin proveedor de correo.
package mailer

import (
	"fmt"
	"log"
	"net/smtp"
	"strings"

	"github.com/BrayanGP/nexus-backend/internal/config"
)

type Mailer struct {
	cfg config.Config
}

func New(cfg config.Config) *Mailer { return &Mailer{cfg: cfg} }

// Send envía un correo de texto. Si SMTP no está configurado, lo registra y
// retorna nil (no es un error).
func (m *Mailer) Send(to, subject, body string) error {
	if !m.cfg.EmailEnabled() || to == "" {
		log.Printf("[mail:skip] para=%q asunto=%q (SMTP no configurado)", to, subject)
		return nil
	}
	addr := m.cfg.SMTPHost + ":" + m.cfg.SMTPPort
	msg := buildMessage(m.cfg.SMTPFrom, to, subject, body)

	var auth smtp.Auth
	if m.cfg.SMTPUser != "" {
		auth = smtp.PlainAuth("", m.cfg.SMTPUser, m.cfg.SMTPPass, m.cfg.SMTPHost)
	}
	if err := smtp.SendMail(addr, auth, fromAddress(m.cfg.SMTPFrom), []string{to}, msg); err != nil {
		log.Printf("[mail:error] %v", err)
		return err
	}
	log.Printf("[mail:ok] para=%q asunto=%q", to, subject)
	return nil
}

func buildMessage(from, to, subject, body string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", to)
	fmt.Fprintf(&b, "Subject: %s\r\n", subject)
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	b.WriteString("\r\n")
	b.WriteString(body)
	return []byte(b.String())
}

// fromAddress extrae "correo@dominio" de un From tipo "neXus <correo@dominio>".
func fromAddress(from string) string {
	if i := strings.LastIndex(from, "<"); i >= 0 {
		if j := strings.LastIndex(from, ">"); j > i {
			return from[i+1 : j]
		}
	}
	return from
}
