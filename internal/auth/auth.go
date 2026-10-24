// Package auth handles password hashing, sessions and login rate limiting.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"math/big"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	SessionTTL     = 12 * time.Hour
	maxFailures    = 5
	lockoutWindow  = 15 * time.Minute
	globalFailures = 50 // per window, across all IPs
)

func HashPassword(p string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(p), 12)
	return string(b), err
}

func CheckPassword(hash, p string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(p)) == nil
}

// dummyHash makes unknown-user logins take as long as real ones.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("dummy-password"), 12)

func CheckDummy(p string) { _ = bcrypt.CompareHashAndPassword(dummyHash, []byte(p)) }

func ConstantEq(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }

const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnpqrstuvwxyz23456789"

// RandomString returns n characters from an unambiguous alphabet.
func RandomString(n int) string {
	out := make([]byte, n)
	max := big.NewInt(int64(len(alphabet)))
	for i := range out {
		v, err := rand.Int(rand.Reader, max)
		if err != nil {
			panic(err)
		}
		out[i] = alphabet[v.Int64()]
	}
	return string(out)
}

func RandomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

type Session struct {
	User    string
	Expires time.Time
}

type Sessions struct {
	mu sync.Mutex
	m  map[string]*Session
}

func NewSessions() *Sessions { return &Sessions{m: map[string]*Session{}} }

func (s *Sessions) Create(user string) string {
	t := RandomToken()
	s.mu.Lock()
	s.m[t] = &Session{User: user, Expires: time.Now().Add(SessionTTL)}
	s.mu.Unlock()
	return t
}

// Get validates a token and slides its expiry.
func (s *Sessions) Get(t string) (*Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.m[t]
	if !ok {
		return nil, false
	}
	if time.Now().After(sess.Expires) {
		delete(s.m, t)
		return nil, false
	}
	sess.Expires = time.Now().Add(SessionTTL)
	return sess, true
}

func (s *Sessions) Delete(t string) {
	s.mu.Lock()
	delete(s.m, t)
	s.mu.Unlock()
}

// DeleteAllExcept revokes every session but keep (used after password change).
func (s *Sessions) DeleteAllExcept(keep string) {
	s.mu.Lock()
	for k := range s.m {
		if k != keep {
			delete(s.m, k)
		}
	}
	s.mu.Unlock()
}

type Limiter struct {
	mu     sync.Mutex
	fails  map[string][]time.Time
	global []time.Time
}

func NewLimiter() *Limiter { return &Limiter{fails: map[string][]time.Time{}} }

func prune(ts []time.Time, now time.Time) []time.Time {
	out := ts[:0]
	for _, t := range ts {
		if now.Sub(t) < lockoutWindow {
			out = append(out, t)
		}
	}
	return out
}

// Allowed reports whether ip may attempt a login, and if not, how long to wait.
func (l *Limiter) Allowed(ip string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	l.fails[ip] = prune(l.fails[ip], now)
	l.global = prune(l.global, now)
	if f := l.fails[ip]; len(f) >= maxFailures {
		return false, lockoutWindow - now.Sub(f[0])
	}
	if len(l.global) >= globalFailures {
		return false, lockoutWindow - now.Sub(l.global[0])
	}
	return true, 0
}

func (l *Limiter) Fail(ip string) {
	l.mu.Lock()
	now := time.Now()
	l.fails[ip] = append(l.fails[ip], now)
	l.global = append(l.global, now)
	l.mu.Unlock()
}

func (l *Limiter) Reset(ip string) {
	l.mu.Lock()
	delete(l.fails, ip)
	l.mu.Unlock()
}
