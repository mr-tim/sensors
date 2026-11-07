package neohub

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

const liveData = `{
  "devices": [
    {"DEVICE_ID": 1, "ZONE_NAME": "Living Room", "ACTUAL_TEMP": "21.5", "SET_TEMP": "20.0", "OFFLINE": false, "HEAT_ON": true},
    {"DEVICE_ID": 2, "device": "Bedroom", "ACTUAL_TEMP": 18, "SET_TEMP": 17.5, "OFFLINE": false},
    {"DEVICE_ID": 3, "ZONE_NAME": "Hall", "ACTUAL_TEMP": "255.255", "OFFLINE": true},
    {"DEVICE_ID": 4, "ZONE_NAME": "Hot Water", "TIMECLOCK": true, "ACTUAL_TEMP": "0"},
    {"DEVICE_ID": 5, "ZONE_NAME": "No Reading", "ACTUAL_TEMP": "n/a"},
    {"ZONE_NAME": "Repeater", "ACTUAL_TEMP": "20"}
  ]
}`

func TestParseLiveData(t *testing.T) {
	zones, err := parseLiveData(json.RawMessage(liveData))
	if err != nil {
		t.Fatal(err)
	}
	if len(zones) != 5 { // the repeater has no DEVICE_ID
		t.Fatalf("got %d zones, want 5", len(zones))
	}

	living := zones[0]
	if living.ID != 1 || living.Name != "Living Room" || !living.HeatOn ||
		*living.Temperature != 21.5 || *living.Target != 20 || !living.HasTemperature() {
		t.Errorf("living room parsed wrong: %+v", living)
	}
	if zones[1].Name != "Bedroom" || *zones[1].Temperature != 18 {
		t.Errorf("numeric temperature / device-name fallback failed: %+v", zones[1])
	}

	for i, want := range []bool{true, true, false, false, false} {
		if got := zones[i].HasTemperature(); got != want {
			t.Errorf("zone %q HasTemperature = %v, want %v", zones[i].Name, got, want)
		}
	}
	if zones[4].Temperature != nil {
		t.Error("unparseable temperature should be nil")
	}
	if !strings.Contains(string(zones[0].Raw), "Living Room") {
		t.Error("raw device JSON not retained")
	}
}

func TestParseLiveDataErrors(t *testing.T) {
	for _, in := range []string{`{}`, `not json`} {
		if _, err := parseLiveData(json.RawMessage(in)); err == nil {
			t.Errorf("parseLiveData(%q) succeeded, want error", in)
		}
	}
}

// fakeHub serves the NeoHub protocol over TLS. respond receives the decoded
// command and token and returns the raw `response` field to send back.
func fakeHub(t *testing.T, respond func(command, token string) any) *Client {
	t.Helper()
	upgrader := websocket.Upgrader{}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var outer struct {
			MessageType string `json:"message_type"`
			Message     string `json:"message"`
		}
		json.Unmarshal(msg, &outer)
		var inner struct {
			Token    string `json:"token"`
			Commands []struct {
				Command   string `json:"COMMAND"`
				CommandID int    `json:"COMMANDID"`
			} `json:"COMMANDS"`
		}
		json.Unmarshal([]byte(outer.Message), &inner)
		if outer.MessageType != "hm_get_command_queue" || len(inner.Commands) != 1 {
			return // a real hub drops malformed requests
		}
		// An unrelated message first, which the client must skip.
		conn.WriteJSON(map[string]any{"command_id": 99, "message_type": "hm_set_command_response", "response": "{}"})
		conn.WriteJSON(map[string]any{
			"command_id":   inner.Commands[0].CommandID,
			"message_type": "hm_set_command_response",
			"device_id":    "aa:bb:cc",
			"response":     respond(inner.Commands[0].Command, inner.Token),
		})
	}))
	t.Cleanup(srv.Close)

	host, portStr, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "https://"))
	port, _ := strconv.Atoi(portStr)
	return NewClient(host, port, "secret-token")
}

func TestZonesAgainstFakeHub(t *testing.T) {
	// The real hub sends its response as a JSON string containing JSON.
	c := fakeHub(t, func(command, token string) any {
		if command != "{'GET_LIVE_DATA': 0}" || token != "secret-token" {
			t.Errorf("hub got command %q token %q", command, token)
		}
		return liveData
	})
	zones, err := c.Zones(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(zones) != 5 || zones[0].Name != "Living Room" {
		t.Errorf("unexpected zones: %+v", zones)
	}
}

func TestZonesNestedObjectResponse(t *testing.T) {
	c := fakeHub(t, func(string, string) any { return json.RawMessage(liveData) })
	zones, err := c.Zones(context.Background())
	if err != nil || len(zones) != 5 {
		t.Fatalf("zones=%d err=%v", len(zones), err)
	}
}

func TestZonesUnreachableHub(t *testing.T) {
	c := NewClient("127.0.0.1", 1, "x")
	if _, err := c.Zones(context.Background()); err == nil {
		t.Error("expected a connection error")
	}
}
