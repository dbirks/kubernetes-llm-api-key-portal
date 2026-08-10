package auth

import (
	"encoding/json"
	"net/http"
	"time"
)

// flashTTL is short: a flash is meant to survive exactly one redirect.
const flashTTL = 2 * time.Minute

// Flash is a one-shot message shown after a redirect.
//
// Sealing it in a cookie keeps the portal stateless. The alternative, a
// server-side flash store, would be the only piece of shared mutable state in
// the whole service.
type Flash struct {
	Kind    string `json:"k"` // "success", "error" or "info"
	Message string `json:"m"`
}

// Flash kinds.
const (
	FlashSuccess = "success"
	FlashError   = "error"
	FlashInfo    = "info"
)

// SetFlash queues a message for the next rendered page.
func (s *Sealer) SetFlash(w http.ResponseWriter, kind, message string) {
	payload, err := json.Marshal(Flash{Kind: kind, Message: message})
	if err != nil {
		return
	}
	sealed, err := s.seal(payload)
	if err != nil {
		return
	}
	http.SetCookie(w, s.cookie(s.name(flashCookieName, flashCookieNameDev), sealed, flashTTL))
}

// TakeFlash reads and immediately clears any pending message.
func (s *Sealer) TakeFlash(w http.ResponseWriter, r *http.Request) []Flash {
	name := s.name(flashCookieName, flashCookieNameDev)
	c, err := r.Cookie(name)
	if err != nil {
		return nil
	}
	s.expire(w, name)

	payload, err := s.open(c.Value)
	if err != nil {
		return nil
	}
	var f Flash
	if err := json.Unmarshal(payload, &f); err != nil || f.Message == "" {
		return nil
	}
	return []Flash{f}
}
