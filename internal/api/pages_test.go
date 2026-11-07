package api

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRenderHistoryPage(t *testing.T) {
	w := httptest.NewRecorder()
	renderPage(w, "history", page{Title: "sensors", Active: "history"})
	out := w.Body.String()
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, out)
	}
	for _, want := range []string{`id="temperature"`, `id="range"`, `font-semibold" href="/"`, "new Chart("} {
		if !strings.Contains(out, want) {
			t.Errorf("history page missing %q", want)
		}
	}
}

func testBody() devicesBody {
	return devicesBody{
		Devices: []deviceItem{
			{ID: "a1", Name: "Kitchen <b>", ProductModel: "SNZB-02P", Selected: true},
			{ID: "b2", Name: "Garage"},
		},
		Detail: &deviceDetail{
			ID: "a1", Name: "Kitchen <b>", ProductModel: "SNZB-02P",
			RefreshedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
			Readings:    []readingView{{Kind: "temperature", Value: "21.5°C", At: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}},
			Details:     `{"a": "<script>"}`,
		},
	}
}

func TestRenderDevicesPage(t *testing.T) {
	w := httptest.NewRecorder()
	renderPage(w, "devices", page{Title: "devices", Active: "devices", Body: testBody()})
	out := w.Body.String()
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, out)
	}
	for _, want := range []string{
		`hx-get="/devices/b2"`,
		`hx-post="/devices/refresh"`,
		`data-selected`,
		`Kitchen &lt;b&gt;`, // names are escaped
		`datetime="2026-01-02T03:04:05Z"`,
		`21.5°C`,
		`&lt;script&gt;`, // details are escaped
	} {
		if !strings.Contains(out, want) {
			t.Errorf("devices page missing %q", want)
		}
	}
	if strings.Contains(out, "<b>") || strings.Contains(out, `"<script>"`) {
		t.Error("unescaped content in devices page")
	}
}

func TestRenderDevicesFragment(t *testing.T) {
	w := httptest.NewRecorder()
	renderTemplate(w, pageTemplates["devices"], "devices_body", testBody())
	out := w.Body.String()
	if strings.Contains(out, "<html") {
		t.Error("fragment should not include the layout")
	}
	if !strings.Contains(out, "Latest readings") {
		t.Error("fragment missing detail")
	}
}

func TestFragmentRequestDetection(t *testing.T) {
	r := httptest.NewRequest("GET", "/devices/a1", nil)
	if isFragmentRequest(r) {
		t.Error("plain request treated as fragment")
	}
	r.Header.Set("HX-Request", "true")
	if !isFragmentRequest(r) {
		t.Error("htmx request not treated as fragment")
	}
	r.Header.Set("HX-History-Restore-Request", "true")
	if isFragmentRequest(r) {
		t.Error("history restore should get the full page")
	}
}
