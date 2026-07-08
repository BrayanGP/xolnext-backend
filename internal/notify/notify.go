// Package notify implementa notificaciones directas en tiempo real mediante
// Server-Sent Events (SSE).
//
// Es la "lógica tipo mensajería" (estilo WhatsApp) pedida en el proyecto, pero
// SIN usar la API de WhatsApp: el servidor empuja eventos directamente a los
// clientes suscritos. Cada cliente se suscribe a una "audiencia"
// (worker:<id>, company:<id>, admin) y recibe en vivo las notificaciones que le
// corresponden, además de las dirigidas a "all".
package notify

import (
	"encoding/json"
	"sync"

	"github.com/BrayanGP/xolnext-backend/internal/models"
)

// Hub mantiene los suscriptores conectados y difunde notificaciones.
type Hub struct {
	mu   sync.RWMutex
	subs map[chan models.Notification]string // canal -> audiencia
}

func NewHub() *Hub {
	return &Hub{subs: make(map[chan models.Notification]string)}
}

// Subscribe registra un cliente para una audiencia y devuelve su canal y una
// función para desuscribirse.
func (h *Hub) Subscribe(audience string) (chan models.Notification, func()) {
	ch := make(chan models.Notification, 16)
	h.mu.Lock()
	h.subs[ch] = audience
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs, ch)
		close(ch)
		h.mu.Unlock()
	}
}

// Broadcast envía la notificación a todos los suscriptores cuya audiencia
// coincida (o si la notificación es para "all").
func (h *Hub) Broadcast(n models.Notification) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for ch, aud := range h.subs {
		if n.Audience == "all" || n.Audience == aud {
			select {
			case ch <- n:
			default: // si el buffer está lleno, no bloqueamos al emisor
			}
		}
	}
}

// Encode serializa una notificación a JSON para el cuerpo del evento SSE.
func Encode(n models.Notification) string {
	b, _ := json.Marshal(n)
	return string(b)
}
