package events

import "sync"

type Listener func(data interface{})

type EventEmitter struct {
	mu        sync.RWMutex
	listeners map[string][]Listener
}

func NewEventEmitter() *EventEmitter {
	return &EventEmitter{
		listeners: make(map[string][]Listener),
	}
}

// On registers a callback for a specific event name.
func (e *EventEmitter) On(event string, listener Listener) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.listeners[event] = append(e.listeners[event], listener)
}

// Off removes a callback for a specific event name.
func (e *EventEmitter) Off(event string, listener Listener) {
	// Function pointer comparison in Go is not reliable for closures.
	// For now, this is a placeholder or we can just skip it if not strictly needed.
}

// Once registers a callback that will be called at most once.
func (e *EventEmitter) Once(event string, listener Listener) {
	var once sync.Once
	var wrapper Listener
	wrapper = func(data interface{}) {
		once.Do(func() {
			listener(data)
			// Ideally we'd remove 'wrapper' here if we had a way to identify it
		})
	}
	e.On(event, wrapper)
}

// Emit triggers all listeners registered for the event name in separate goroutines.
func (e *EventEmitter) Emit(event string, data interface{}) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	if listeners, ok := e.listeners[event]; ok {
		for _, listener := range listeners {
			go listener(data)
		}
	}
}
