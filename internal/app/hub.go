package app

import "sync"

// hub wakes long-polling inbox readers whenever a notification is written.
type hub struct {
	mu      sync.Mutex
	waiters map[int64]chan struct{}
}

func newHub() *hub { return &hub{waiters: map[int64]chan struct{}{}} }

// wait returns a channel that closes on the next notify for principalID.
func (h *hub) wait(principalID int64) <-chan struct{} {
	h.mu.Lock()
	defer h.mu.Unlock()
	ch, ok := h.waiters[principalID]
	if !ok {
		ch = make(chan struct{})
		h.waiters[principalID] = ch
	}
	return ch
}

func (h *hub) notify(principalID int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if ch, ok := h.waiters[principalID]; ok {
		close(ch)
		delete(h.waiters, principalID)
	}
}
