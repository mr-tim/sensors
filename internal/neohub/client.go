// Package neohub reads thermostat state from a Heatmiser NeoHub using its
// token-authenticated WebSocket API (port 4243).
package neohub

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/gorilla/websocket"
)

// Client talks to a NeoHub. Each call opens a short-lived connection, so a
// Client is safe for concurrent use and needs no cleanup.
type Client struct {
	Host    string
	Port    int
	Token   string
	Timeout time.Duration // per request; defaults to 15s
}

func NewClient(host string, port int, token string) *Client {
	return &Client{Host: host, Port: port, Token: token}
}

// Zone is one thermostat (or wireless sensor) as reported by the hub.
type Zone struct {
	ID          int
	Name        string
	Temperature *float64 // current temperature in °C
	Target      *float64 // set point in °C
	Offline     bool
	TimeClock   bool // a timeclock (no temperature) rather than a thermostat
	HeatOn      bool
	Raw         json.RawMessage // the hub's full entry for this device
}

// HasTemperature reports whether the zone is online and has a usable
// temperature, i.e. is worth recording.
func (z Zone) HasTemperature() bool {
	return !z.Offline && !z.TimeClock && z.Temperature != nil
}

// Zones fetches live data for every device the hub knows about.
func (c *Client) Zones(ctx context.Context) ([]Zone, error) {
	// The hub expects the command as the Python repr of a dict, which is
	// what the official neohubapi library sends.
	resp, err := c.send(ctx, "{'GET_LIVE_DATA': 0}")
	if err != nil {
		return nil, err
	}
	return parseLiveData(resp)
}

// send runs one command and returns the hub's response payload.
func (c *Client) send(ctx context.Context, command string) (json.RawMessage, error) {
	timeout := c.Timeout
	if timeout == 0 {
		timeout = 15 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	dialer := websocket.Dialer{
		HandshakeTimeout: timeout,
		// The hub presents a self-signed certificate, so it can't be
		// verified (the official neohubapi client does the same). The
		// token still authenticates us to the hub.
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	addr := "wss://" + net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
	conn, _, err := dialer.DialContext(ctx, addr, nil)
	if err != nil {
		return nil, fmt.Errorf("connecting to neohub %s: %w", addr, err)
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		conn.SetReadDeadline(dl)
		conn.SetWriteDeadline(dl)
	}

	const commandID = 1
	inner, err := json.Marshal(map[string]any{
		"token":    c.Token,
		"COMMANDS": []map[string]any{{"COMMAND": command, "COMMANDID": commandID}},
	})
	if err != nil {
		return nil, err
	}
	outer, err := json.Marshal(map[string]string{
		"message_type": "hm_get_command_queue",
		"message":      string(inner),
	})
	if err != nil {
		return nil, err
	}
	if err := conn.WriteMessage(websocket.TextMessage, outer); err != nil {
		return nil, fmt.Errorf("sending to neohub: %w", err)
	}

	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			// A wrong token typically makes the hub just drop the connection.
			return nil, fmt.Errorf("reading from neohub (bad token?): %w", err)
		}
		var reply struct {
			CommandID   int             `json:"command_id"`
			MessageType string          `json:"message_type"`
			Response    json.RawMessage `json:"response"`
		}
		if err := json.Unmarshal(msg, &reply); err != nil {
			return nil, fmt.Errorf("decoding neohub reply: %w", err)
		}
		if reply.CommandID != commandID || reply.MessageType != "hm_set_command_response" {
			continue
		}
		return unwrapResponse(reply.Response)
	}
}

// unwrapResponse handles the hub sending its response as a JSON-encoded
// string containing JSON, rather than as a nested object.
func unwrapResponse(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) > 0 && raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, fmt.Errorf("decoding neohub response: %w", err)
		}
		return json.RawMessage(s), nil
	}
	return raw, nil
}

func parseLiveData(data json.RawMessage) ([]Zone, error) {
	var live struct {
		Devices []json.RawMessage `json:"devices"`
	}
	if err := json.Unmarshal(data, &live); err != nil {
		return nil, fmt.Errorf("decoding live data: %w", err)
	}
	if live.Devices == nil {
		return nil, errors.New("live data has no devices list")
	}

	var zones []Zone
	for _, raw := range live.Devices {
		var d struct {
			DeviceID   *int            `json:"DEVICE_ID"`
			ZoneName   string          `json:"ZONE_NAME"`
			Device     string          `json:"device"`
			ActualTemp flexFloat       `json:"ACTUAL_TEMP"`
			SetTemp    flexFloat       `json:"SET_TEMP"`
			Offline    bool            `json:"OFFLINE"`
			TimeClock  json.RawMessage `json:"TIMECLOCK"`
			HeatOn     bool            `json:"HEAT_ON"`
		}
		if err := json.Unmarshal(raw, &d); err != nil {
			return nil, fmt.Errorf("decoding device: %w", err)
		}
		// Entries without a DEVICE_ID (e.g. repeaters) aren't thermostats.
		if d.DeviceID == nil {
			continue
		}
		name := d.ZoneName
		if name == "" {
			name = d.Device
		}
		if name == "" {
			name = "Unknown"
		}
		zones = append(zones, Zone{
			ID:          *d.DeviceID,
			Name:        name,
			Temperature: d.ActualTemp.value,
			Target:      d.SetTemp.value,
			Offline:     d.Offline,
			TimeClock:   len(d.TimeClock) > 0,
			HeatOn:      d.HeatOn,
			Raw:         raw,
		})
	}
	return zones, nil
}

// flexFloat decodes a temperature the hub may send as a number, a numeric
// string, or something unusable (which leaves it nil).
type flexFloat struct{ value *float64 }

func (f *flexFloat) UnmarshalJSON(b []byte) error {
	f.value = nil
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return nil
	}
	switch n := v.(type) {
	case float64:
		f.value = &n
	case string:
		if parsed, err := strconv.ParseFloat(n, 64); err == nil {
			f.value = &parsed
		}
	}
	return nil
}
