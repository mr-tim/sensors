package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/mr-tim/ewelink/internal/ewelink"
	"github.com/mr-tim/ewelink/internal/token"
)

// Server exposes a small HTTP API for driving the oauth login flow and
// inspecting token state.
type Server struct {
	client *ewelink.Client
	store  *token.Store
	addr   string

	mu    sync.Mutex
	state string
}

func NewServer(client *ewelink.Client, store *token.Store, addr string) *Server {
	return &Server{client: client, store: store, addr: addr}
}

func (s *Server) redirectURL() string {
	return fmt.Sprintf("http://localhost%s/callback", s.addr)
}

func (s *Server) LoginURL() string {
	return fmt.Sprintf("http://localhost%s/login", s.addr)
}

func (s *Server) Run() error {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /login", s.handleLogin)
	mux.HandleFunc("GET /callback", s.handleCallback)
	mux.HandleFunc("GET /status", s.handleStatus)
	log.Printf("api: listening on %s", s.addr)
	return http.ListenAndServe(s.addr, mux)
}

// handleLogin redirects the browser to the eWeLink oauth login page.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	state := hex.EncodeToString(buf)
	s.mu.Lock()
	s.state = state
	s.mu.Unlock()
	http.Redirect(w, r, s.client.LoginURL(s.redirectURL(), state), http.StatusFound)
}

// handleCallback receives the oauth redirect, exchanges the code for
// tokens, and saves them.
func (s *Server) handleCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	code, region, state := q.Get("code"), q.Get("region"), q.Get("state")
	if code == "" || region == "" {
		http.Error(w, "missing code or region", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	expected := s.state
	s.state = ""
	s.mu.Unlock()
	if expected == "" || state != expected {
		http.Error(w, "state mismatch: restart the login flow at /login", http.StatusBadRequest)
		return
	}

	data, err := s.client.GetToken(r.Context(), region, code, s.redirectURL())
	if err != nil {
		http.Error(w, fmt.Sprintf("token exchange failed: %v", err), http.StatusBadGateway)
		return
	}
	tok := token.Token{
		AccessToken:   data.AccessToken,
		AtExpiredTime: data.AtExpiredTime,
		RefreshToken:  data.RefreshToken,
		RtExpiredTime: data.RtExpiredTime,
		Region:        region,
	}
	if err := s.store.Set(tok); err != nil {
		http.Error(w, fmt.Sprintf("saving token: %v", err), http.StatusInternalServerError)
		return
	}
	log.Printf("api: login complete, token saved (expires %s)", time.UnixMilli(tok.AtExpiredTime).Format(time.RFC3339))
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprint(w, "<html><body>Login successful, you can close this tab.</body></html>")
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	type status struct {
		LoggedIn         bool   `json:"loggedIn"`
		Region           string `json:"region,omitempty"`
		AccessExpiresAt  string `json:"accessExpiresAt,omitempty"`
		RefreshExpiresAt string `json:"refreshExpiresAt,omitempty"`
	}
	st := status{}
	if tok := s.store.Get(); tok.Valid() {
		st = status{
			LoggedIn:         true,
			Region:           tok.Region,
			AccessExpiresAt:  time.UnixMilli(tok.AtExpiredTime).Format(time.RFC3339),
			RefreshExpiresAt: time.UnixMilli(tok.RtExpiredTime).Format(time.RFC3339),
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(st)
}
