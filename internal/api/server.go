package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/mr-tim/sensors/internal/ewelink"
	"github.com/mr-tim/sensors/internal/readings"
	"github.com/mr-tim/sensors/internal/token"
)

// ErrNotLoggedIn is returned by a RefreshFunc when there is no valid token.
var ErrNotLoggedIn = errors.New("not logged in")

// RefreshFunc reloads device details from the eWeLink API into the
// database, returning how many devices were stored.
type RefreshFunc func(ctx context.Context) (int, error)

// Server exposes a small HTTP API for driving the oauth login flow and
// inspecting token state.
type Server struct {
	client *ewelink.Client
	store  *token.Store
	db     *readings.Store
	addr   string

	refresh    RefreshFunc
	refreshing sync.Mutex

	mu    sync.Mutex
	state string
}

func NewServer(client *ewelink.Client, store *token.Store, db *readings.Store, refresh RefreshFunc, addr string) *Server {
	return &Server{client: client, store: store, db: db, refresh: refresh, addr: addr}
}

func (s *Server) redirectURL() string {
	return fmt.Sprintf("http://localhost%s/callback", s.addr)
}

func (s *Server) LoginURL() string {
	return fmt.Sprintf("http://localhost%s/login", s.addr)
}

func (s *Server) Run() error {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleIndex)
	mux.HandleFunc("GET /devices", s.handleDevicesPage)
	mux.HandleFunc("GET /devices/{id}", s.handleDevicesPage)
	mux.HandleFunc("POST /devices/refresh", s.handleRefreshDevices)
	mux.HandleFunc("GET /api/readings", s.handleReadings)
	mux.HandleFunc("GET /api/events", s.handleEvents)
	mux.HandleFunc("GET /api/devices", s.handleDevices)
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

// handleReadings returns reading history as JSON, for the home page charts.
// The window defaults to the last 24 hours; override with ?hours=N.
func (s *Server) handleReadings(w http.ResponseWriter, r *http.Request) {
	hours := 24
	if v := r.URL.Query().Get("hours"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			http.Error(w, "hours must be a positive integer", http.StatusBadRequest)
			return
		}
		hours = n
	}
	points, err := s.db.History(r.Context(), time.Now().Add(-time.Duration(hours)*time.Hour))
	if err != nil {
		log.Printf("api: reading history: %v", err)
		http.Error(w, "querying readings failed", http.StatusInternalServerError)
		return
	}

	type point struct {
		DeviceID string  `json:"deviceId"`
		Device   string  `json:"device"`
		Kind     string  `json:"kind"`
		Time     int64   `json:"t"`
		Value    float64 `json:"v"`
	}
	out := make([]point, len(points))
	for i, p := range points {
		out[i] = point{p.DeviceID, p.DeviceName, string(p.Kind), p.RecordedAt.UnixMilli(), p.Value}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

func (s *Server) handleDevices(w http.ResponseWriter, r *http.Request) {
	devices, err := s.db.ListDevices(r.Context())
	if err != nil {
		log.Printf("api: listing devices: %v", err)
		http.Error(w, "querying devices failed", http.StatusInternalServerError)
		return
	}
	type device struct {
		ID           string `json:"id"`
		Name         string `json:"name"`
		ProductModel string `json:"productModel"`
	}
	out := make([]device, len(devices))
	for i, d := range devices {
		out[i] = device{d.DeviceID, d.Name, d.ProductModel}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

// handleEvents streams a server-sent "reading" event each time a new
// reading is stored, so pages can refresh without polling.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	flusher.Flush()

	updates, cancel := s.db.Subscribe()
	defer cancel()
	keepAlive := time.NewTicker(30 * time.Second)
	defer keepAlive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-updates:
			fmt.Fprint(w, "event: reading\ndata: {}\n\n")
		case <-keepAlive.C:
			fmt.Fprint(w, ": keep-alive\n\n")
		}
		flusher.Flush()
	}
}
