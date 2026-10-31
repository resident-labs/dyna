package model

import (
	"net/http"
	"sync"
)

// Drain holds new model calls while admitted calls finish.
type Drain struct {
	mu       sync.Mutex
	draining bool
	active   int
	zero     chan struct{}
}

func NewDrain() *Drain { return &Drain{zero: make(chan struct{})} }

func (d *Drain) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		if d.draining {
			d.mu.Unlock()
			<-r.Context().Done()
			return
		}
		d.active++
		d.mu.Unlock()
		defer func() {
			d.mu.Lock()
			d.active--
			if d.draining && d.active == 0 {
				close(d.zero)
			}
			d.mu.Unlock()
		}()
		next.ServeHTTP(w, r)
	})
}

// Begin prevents new calls and returns a channel closed when admitted calls finish.
func (d *Drain) Begin() <-chan struct{} {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.draining {
		d.draining = true
		if d.active == 0 {
			close(d.zero)
		}
	}
	return d.zero
}
