// Package mailer envía notificaciones por correo.
//
// Es OPCIONAL. Prefiere la Web API de SendGrid (HTTPS, puerto 443) porque
// muchos PaaS (Railway incluido) bloquean los puertos SMTP salientes. Si no hay
// SENDGRID_API_KEY pero sí SMTP_*, usa SMTP. Si no hay nada, no envía (no falla).
package mailer

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/smtp"
	"strings"
	"time"

	"github.com/BrayanGP/nexus-backend/internal/config"
)

type Mailer struct {
	cfg    config.Config
	client *http.Client
}

func New(cfg config.Config) *Mailer {
	return &Mailer{cfg: cfg, client: &http.Client{Timeout: 15 * time.Second}}
}

// Send envía un correo de texto plano. Elige el proveedor disponible.
func (m *Mailer) Send(to, subject, body string) error {
	if to == "" {
		return nil
	}
	switch {
	case m.cfg.SendGridKey != "":
		return m.sendViaSendGrid(to, subject, body)
	case m.cfg.SMTPHost != "":
		return m.sendViaSMTP(to, subject, body)
	default:
		log.Printf("[mail:skip] para=%q asunto=%q (sin proveedor de correo)", to, subject)
		return nil
	}
}

// sendViaSendGrid usa la Web API v3 de SendGrid (HTTPS).
func (m *Mailer) sendViaSendGrid(to, subject, body string) error {
	payload := map[string]any{
		"personalizations": []any{
			map[string]any{"to": []any{map[string]string{"email": to}}},
		},
		"from": map[string]string{
			"email": fromAddress(m.cfg.MailFrom),
			"name":  fromName(m.cfg.MailFrom),
		},
		"subject": subject,
		"content": []any{map[string]string{"type": "text/plain", "value": body}},
	}
	b, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost,
		"https://api.sendgrid.com/v3/mail/send", bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+m.cfg.SendGridKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := m.client.Do(req)
	if err != nil {
		log.Printf("[mail:error] sendgrid: %v", err)
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(resp.Body)
		body := strings.TrimSpace(string(msg))
		log.Printf("[mail:error] sendgrid status=%d body=%s", resp.StatusCode, body)
		return fmt.Errorf("SendGrid respondió %d: %s", resp.StatusCode, body)
	}
	log.Printf("[mail:ok] (sendgrid) para=%q asunto=%q", to, subject)
	return nil
}

// Provider indica qué proveedor de correo está activo.
func (m *Mailer) Provider() string {
	switch {
	case m.cfg.SendGridKey != "":
		return "sendgrid"
	case m.cfg.SMTPHost != "":
		return "smtp"
	default:
		return "none"
	}
}

// FromAddress es el remitente configurado (debe estar verificado en SendGrid).
func (m *Mailer) FromAddress() string { return fromAddress(m.cfg.MailFrom) }

// SendTest envía un correo de prueba y devuelve (ok, detalle) para diagnóstico.
func (m *Mailer) SendTest(to string) (bool, string) {
	if m.Provider() == "none" {
		return false, "Sin proveedor de correo configurado (define SENDGRID_API_KEY o SMTP_*)."
	}
	err := m.Send(to, "neXus · Prueba de correo",
		"Este es un correo de prueba de neXus. Si lo recibes, el envío funciona correctamente.")
	if err != nil {
		return false, err.Error()
	}
	return true, fmt.Sprintf("Aceptado: enviado a %s desde %q vía %s.", to, m.FromAddress(), m.Provider())
}

// sendViaSMTP envía por SMTP (alternativa).
func (m *Mailer) sendViaSMTP(to, subject, body string) error {
	addr := m.cfg.SMTPHost + ":" + m.cfg.SMTPPort
	msg := buildMessage(m.cfg.MailFrom, to, subject, body)
	var auth smtp.Auth
	if m.cfg.SMTPUser != "" {
		auth = smtp.PlainAuth("", m.cfg.SMTPUser, m.cfg.SMTPPass, m.cfg.SMTPHost)
	}
	if err := smtp.SendMail(addr, auth, fromAddress(m.cfg.MailFrom), []string{to}, msg); err != nil {
		log.Printf("[mail:error] smtp: %v", err)
		return err
	}
	log.Printf("[mail:ok] (smtp) para=%q asunto=%q", to, subject)
	return nil
}

func buildMessage(from, to, subject, body string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", to)
	fmt.Fprintf(&b, "Subject: %s\r\n", subject)
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n\r\n")
	b.WriteString(body)
	return []byte(b.String())
}

// fromAddress extrae "correo@dominio" de un From tipo "neXus <correo@dominio>".
func fromAddress(from string) string {
	if i := strings.LastIndex(from, "<"); i >= 0 {
		if j := strings.LastIndex(from, ">"); j > i {
			return strings.TrimSpace(from[i+1 : j])
		}
	}
	return strings.TrimSpace(from)
}

// fromName extrae el nombre visible (lo previo a "<"); si no hay, usa "neXus".
func fromName(from string) string {
	if i := strings.Index(from, "<"); i > 0 {
		return strings.TrimSpace(from[:i])
	}
	return "neXus"
}
