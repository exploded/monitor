package handlers

import "sync"

// Hub broadcasts SSE events to connected dashboard clients.
type Hub struct {
	mu      sync.Mutex
	clients []chan string
}

func NewHub() *Hub {
	return &Hub{}
}

// Subscribe returns a channel that receives broadcast events.
func (h *Hub) Subscribe() chan string {
	ch := make(chan string, 16)
	h.mu.Lock()
	h.clients = append(h.clients, ch)
	h.mu.Unlock()
	return ch
}

// Unsubscribe removes a client channel from the broadcast list.
func (h *Hub) Unsubscribe(ch chan string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i, c := range h.clients {
		if c == ch {
			h.clients = append(h.clients[:i], h.clients[i+1:]...)
			return
		}
	}
}

// Broadcast sends data to all connected clients. Drops messages
// for slow clients to avoid blocking the sender.
func (h *Hub) Broadcast(data string) {
	h.mu.Lock()
	clients := make([]chan string, len(h.clients))
	copy(clients, h.clients)
	h.mu.Unlock()

	for _, ch := range clients {
		select {
		case ch <- data:
		default:
		}
	}
}
