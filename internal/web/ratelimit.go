package web

import (
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"

	"github.com/wApp/wapp-guardian-bff/internal/config"
)

// keyedRateLimiter es un rate-limit en memoria por clave (IP o user_id), construido sobre el token
// bucket de golang.org/x/time/rate. Cada clave tiene su propio bucket; un barrido periódico desaloja los
// buckets inactivos para que el mapa no crezca sin límite.
//
// NOTA (INV-6, V1): el estado es por instancia (in-memory), sin broker (ni Redis ni RabbitMQ). Con varias
// réplicas detrás de un balanceador el límite efectivo se multiplica por el nº de instancias. Para un
// límite global multi-instancia haría falta un backend compartido; se documenta como deuda y NO se
// implementa en V1 (mismo compromiso que edugo-messaging-web, copia-adaptado).
//
// wApp NO reusa el ratelimiter de edugo-shared (INV-5): wapp-shared no expone un módulo de rate-limit,
// así que se usa golang.org/x/time/rate directamente (el mismo token-bucket que el cloud ya emplea).
type keyedRateLimiter struct {
	mu       sync.Mutex
	buckets  map[string]*bucketEntry
	rps      rate.Limit
	burst    int
	ttl      time.Duration // tiempo de inactividad tras el cual se desaloja una clave.
	stopOnce sync.Once
	stop     chan struct{}
}

type bucketEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// newKeyedRateLimiter crea el limitador y arranca el barrido de buckets inactivos.
func newKeyedRateLimiter(rps, burst float64) *keyedRateLimiter {
	k := &keyedRateLimiter{
		buckets: make(map[string]*bucketEntry),
		rps:     rate.Limit(rps),
		burst:   int(burst),
		ttl:     10 * time.Minute,
		stop:    make(chan struct{}),
	}
	go k.sweepLoop()
	return k
}

// allow consume un token del bucket de la clave (creándolo si no existe). Devuelve false si la clave
// agotó su ráfaga.
func (k *keyedRateLimiter) allow(key string) bool {
	k.mu.Lock()
	entry, ok := k.buckets[key]
	if !ok {
		entry = &bucketEntry{limiter: rate.NewLimiter(k.rps, k.burst)}
		k.buckets[key] = entry
	}
	entry.lastSeen = time.Now()
	k.mu.Unlock()

	return entry.limiter.Allow()
}

// sweepLoop desaloja periódicamente las claves inactivas (más viejas que ttl).
func (k *keyedRateLimiter) sweepLoop() {
	ticker := time.NewTicker(k.ttl)
	defer ticker.Stop()
	for {
		select {
		case <-k.stop:
			return
		case <-ticker.C:
			cutoff := time.Now().Add(-k.ttl)
			k.mu.Lock()
			for key, entry := range k.buckets {
				if entry.lastSeen.Before(cutoff) {
					delete(k.buckets, key)
				}
			}
			k.mu.Unlock()
		}
	}
}

// close detiene el barrido (idempotente). Útil en tests para no dejar goroutines vivas.
func (k *keyedRateLimiter) close() {
	k.stopOnce.Do(func() { close(k.stop) })
}

// RateLimitMiddleware limita las peticiones por IP del cliente y, si hay sesión, por user_id (la clave
// más específica gana). Al exceder el límite responde 429 con Retry-After y un mensaje en español.
//
// El *keyedRateLimiter se devuelve junto al middleware para que el dueño (newRouterWithLimiter) pueda
// cerrarlo; el llamador puede ignorarlo si no le hace falta el ciclo de vida.
func RateLimitMiddleware(cfg *config.Config) (gin.HandlerFunc, *keyedRateLimiter) {
	limiter := newKeyedRateLimiter(cfg.RateLimitRPS, cfg.RateLimitBurst)

	handler := func(c *gin.Context) {
		key := rateLimitKey(c)
		if !limiter.allow(key) {
			// No se loguea la clave cruda (puede ser un user_id): solo método+ruta para diagnóstico.
			slog.Warn("petición rechazada por rate-limit",
				"method", c.Request.Method, "path", c.Request.URL.Path)
			c.Header("Retry-After", "1")
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"error": "Demasiadas solicitudes. Espera un momento e inténtalo de nuevo.",
			})
			return
		}
		c.Next()
	}

	return handler, limiter
}

// rateLimitKey elige la clave de limitación: el user_id de la sesión si lo hay (prefijo "u:"), si no la
// IP del cliente (prefijo "ip:"). Los prefijos evitan colisiones entre espacios de claves. En T1 aún no
// hay autenticación, así que en la práctica la clave es la IP; el gancho por user_id lo alimentará T2.
func rateLimitKey(c *gin.Context) string {
	if uid := userIDFromCtx(c); uid != "" {
		return "u:" + uid
	}
	return "ip:" + c.ClientIP()
}
