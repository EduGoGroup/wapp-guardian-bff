package web

import (
	"sync"

	"github.com/wApp/wapp-guardian-bff/internal/apiclient"
)

// refreshGroup es un single-flight casero que serializa los refresh por clave (refresh token).
type refreshGroup struct {
	mu    sync.Mutex
	calls map[string]*refreshCall
}

// refreshCall es una ejecución en curso: los que esperan bloquean en done y leen res/err.
type refreshCall struct {
	done chan struct{}
	res  *apiclient.AuthResult
	err  error
}

func newRefreshGroup() *refreshGroup {
	return &refreshGroup{calls: make(map[string]*refreshCall)}
}

// do ejecuta fn una sola vez por key; los llamadores concurrentes con la misma key esperan y reciben el mismo resultado.
func (g *refreshGroup) do(key string, fn func() (*apiclient.AuthResult, error)) (*apiclient.AuthResult, error) {
	g.mu.Lock()
	if call, ok := g.calls[key]; ok {
		g.mu.Unlock()
		<-call.done
		return call.res, call.err
	}
	call := &refreshCall{done: make(chan struct{})}
	g.calls[key] = call
	g.mu.Unlock()

	call.res, call.err = fn()

	g.mu.Lock()
	delete(g.calls, key)
	g.mu.Unlock()
	close(call.done)

	return call.res, call.err
}
