package api

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mr-tim/sensors/internal/readings"
)

//go:embed templates
var templateFS embed.FS

// pageTemplates holds one template set per page: the shared layout, the
// partials, and that page's own blocks.
var pageTemplates = map[string]*template.Template{
	"history": parsePage("history.html"),
	"devices": parsePage("devices.html"),
}

func parsePage(page string) *template.Template {
	return template.Must(template.ParseFS(templateFS,
		"templates/layout.html", "templates/devices_body.html", "templates/"+page))
}

// page is the data passed to the layout.
type page struct {
	Title  string
	Active string // which nav entry to highlight
	Body   any    // page-specific data
}

func renderPage(w http.ResponseWriter, name string, data page) {
	renderTemplate(w, pageTemplates[name], "layout", data)
}

// renderTemplate renders to a buffer first so that a template error can
// still produce a 500 rather than a half-written page.
func renderTemplate(w http.ResponseWriter, t *template.Template, name string, data any) {
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, name, data); err != nil {
		log.Printf("api: rendering %s: %v", name, err)
		http.Error(w, "rendering page failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	buf.WriteTo(w)
}

// isFragmentRequest reports whether htmx wants just the swappable part of
// the page. History-restore requests (back button after a cache miss) need
// the full page.
func isFragmentRequest(r *http.Request) bool {
	return r.Header.Get("HX-Request") == "true" && r.Header.Get("HX-History-Restore-Request") != "true"
}

// fragmentError reports an error to an htmx request by swapping the message
// into the page's #error element.
func fragmentError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("HX-Retarget", "#error")
	w.Header().Set("HX-Reswap", "innerHTML")
	w.WriteHeader(status)
	fmt.Fprint(w, html.EscapeString(msg))
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	renderPage(w, "history", page{Title: "sensors", Active: "history"})
}

type deviceItem struct {
	ID, Name, ProductModel string
	Selected               bool
}

type readingView struct {
	Kind  string
	Value string
	At    time.Time
}

type deviceDetail struct {
	ID, Name, ProductModel string
	RefreshedAt            time.Time
	Readings               []readingView
	Details                string
}

// devicesBody is the data for the devices list and selected-device detail.
type devicesBody struct {
	Devices []deviceItem
	Detail  *deviceDetail
}

var units = map[readings.Kind]string{readings.Temperature: "°C", readings.Humidity: "%"}

// loadDevicesBody loads the device list with selectedID shown in detail,
// or the first device if selectedID is empty. It returns
// readings.ErrNotFound if selectedID doesn't exist.
func (s *Server) loadDevicesBody(r *http.Request, selectedID string) (devicesBody, error) {
	ctx := r.Context()
	var body devicesBody
	devices, err := s.db.ListDevices(ctx)
	if err != nil {
		return body, err
	}
	if selectedID == "" && len(devices) > 0 {
		selectedID = devices[0].DeviceID
	}
	found := selectedID == ""
	for _, d := range devices {
		sel := d.DeviceID == selectedID
		found = found || sel
		body.Devices = append(body.Devices, deviceItem{d.DeviceID, d.Name, d.ProductModel, sel})
	}
	if !found {
		return body, readings.ErrNotFound
	}
	if selectedID == "" {
		return body, nil
	}

	d, err := s.db.GetDevice(ctx, selectedID)
	if err != nil {
		return body, err
	}
	latest, err := s.db.LatestReadings(ctx, selectedID)
	if err != nil {
		return body, err
	}
	var details bytes.Buffer
	if err := json.Indent(&details, d.Details, "", "  "); err != nil {
		details.Reset()
		details.Write(d.Details)
	}
	detail := &deviceDetail{
		ID:           d.DeviceID,
		Name:         d.Name,
		ProductModel: d.ProductModel,
		RefreshedAt:  d.UpdatedAt.UTC(),
		Details:      details.String(),
	}
	for _, l := range latest {
		detail.Readings = append(detail.Readings, readingView{
			Kind:  string(l.Kind),
			Value: fmt.Sprintf("%.1f%s", l.Value, units[l.Kind]),
			At:    l.RecordedAt.UTC(),
		})
	}
	body.Detail = detail
	return body, nil
}

// renderDevices writes the devices page, or just the swappable body for htmx
// requests.
func (s *Server) renderDevices(w http.ResponseWriter, r *http.Request, body devicesBody) {
	w.Header().Add("Vary", "HX-Request")
	if isFragmentRequest(r) {
		renderTemplate(w, pageTemplates["devices"], "devices_body", body)
		return
	}
	renderPage(w, "devices", page{Title: "devices", Active: "devices", Body: body})
}

// handleDevicesPage serves /devices and /devices/{id}.
func (s *Server) handleDevicesPage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	body, err := s.loadDevicesBody(r, id)
	if errors.Is(err, readings.ErrNotFound) {
		s.deviceError(w, r, http.StatusNotFound, "no such device")
		return
	}
	if err != nil {
		log.Printf("api: loading devices page (device %q): %v", id, err)
		s.deviceError(w, r, http.StatusInternalServerError, "querying devices failed")
		return
	}
	s.renderDevices(w, r, body)
}

func (s *Server) deviceError(w http.ResponseWriter, r *http.Request, status int, msg string) {
	if isFragmentRequest(r) {
		fragmentError(w, status, msg)
		return
	}
	http.Error(w, msg, status)
}

// handleRefreshDevices reloads device details from the eWeLink API, then
// re-renders the devices body, keeping the currently open device selected.
func (s *Server) handleRefreshDevices(w http.ResponseWriter, r *http.Request) {
	if !s.refreshing.TryLock() {
		fragmentError(w, http.StatusConflict, "a refresh is already in progress")
		return
	}
	defer s.refreshing.Unlock()

	n, err := s.refresh(r.Context())
	if errors.Is(err, ErrNotLoggedIn) {
		fragmentError(w, http.StatusUnauthorized, "Refresh failed: not logged in (visit /login first)")
		return
	}
	if err != nil {
		log.Printf("api: refreshing devices: %v", err)
		fragmentError(w, http.StatusBadGateway, "Refresh failed: "+err.Error())
		return
	}
	log.Printf("api: refreshed %d devices", n)

	// htmx tells us which page the button was clicked on.
	selected := ""
	if u, err := url.Parse(r.Header.Get("HX-Current-URL")); err == nil {
		selected = strings.TrimPrefix(u.Path, "/devices/")
		if selected == u.Path {
			selected = ""
		}
	}
	body, err := s.loadDevicesBody(r, selected)
	if errors.Is(err, readings.ErrNotFound) {
		body, err = s.loadDevicesBody(r, "")
	}
	if err != nil {
		log.Printf("api: loading devices after refresh: %v", err)
		fragmentError(w, http.StatusInternalServerError, "querying devices failed")
		return
	}
	renderTemplate(w, pageTemplates["devices"], "devices_body", body)
}
