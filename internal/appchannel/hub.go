package appchannel

import "sync"

// hub fans events out to connected clients. A client that falls behind is
// dropped rather than slowing everyone down; it reconnects and reloads.
type hub struct {
	mu   sync.Mutex
	subs map[*subscriber]struct{}
}

type subscriber struct {
	ch     chan Event
	closed chan struct{}
	once   sync.Once
}

const subscriberBuffer = 256

func newHub() *hub { return &hub{subs: map[*subscriber]struct{}{}} }

func (h *hub) subscribe() *subscriber {
	s := &subscriber{ch: make(chan Event, subscriberBuffer), closed: make(chan struct{})}
	h.mu.Lock()
	h.subs[s] = struct{}{}
	h.mu.Unlock()
	return s
}

func (h *hub) unsubscribe(s *subscriber) {
	h.mu.Lock()
	delete(h.subs, s)
	h.mu.Unlock()
	s.once.Do(func() { close(s.closed) })
}

func (h *hub) publish(e Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.subs {
		select {
		case s.ch <- e:
		default:
			delete(h.subs, s)
			s.once.Do(func() { close(s.closed) })
		}
	}
}

func (h *hub) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}
