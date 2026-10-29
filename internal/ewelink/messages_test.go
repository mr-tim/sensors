package ewelink

import (
	"testing"
	"time"
)

func TestParseTemperatureUpdate(t *testing.T) {
	tests := []struct {
		name string
		msg  string
		want TemperatureUpdate
	}{
		{
			name: "SNZB-02P, integer hundredths of a degree",
			msg:  `{"action":"update","deviceid":"a48013db7a","apikey":"09763ef7-f144-47dd-8ad2-5191cd8ccaf8","userAgent":"device","d_seq":59495,"params":{"temperature":"2780","trigTime":"1783353572000","tempComfortStatus":2}}`,
			want: TemperatureUpdate{
				DeviceID:    "a48013db7a",
				Temperature: 27.80,
				At:          time.UnixMilli(1783353572000),
			},
		},
		{
			name: "SNZB-02LD, decimal degrees",
			msg:  `{"action":"update","deviceid":"a48013db7e","apikey":"09763ef7-f144-47dd-8ad2-5191cd8ccaf8","userAgent":"device","d_seq":45617,"params":{"temperatureF":"85.6","temperature":"29.8","trigTime":"1783353826000"}}`,
			want: TemperatureUpdate{
				DeviceID:    "a48013db7e",
				Temperature: 29.8,
				At:          time.UnixMilli(1783353826000),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ParseTemperatureUpdate([]byte(tt.msg))
			if !ok {
				t.Fatal("ParseTemperatureUpdate returned ok=false")
			}
			if got.DeviceID != tt.want.DeviceID {
				t.Errorf("DeviceID = %q, want %q", got.DeviceID, tt.want.DeviceID)
			}
			if got.Temperature != tt.want.Temperature {
				t.Errorf("Temperature = %v, want %v", got.Temperature, tt.want.Temperature)
			}
			if !got.At.Equal(tt.want.At) {
				t.Errorf("At = %v, want %v", got.At, tt.want.At)
			}
		})
	}
}

func TestParseTemperatureUpdateIgnoresOtherMessages(t *testing.T) {
	tests := []struct {
		name string
		msg  string
	}{
		{"handshake response", `{"error":0,"apikey":"09763ef7-f144-47dd-8ad2-5191cd8ccaf8","config":{"hb":1,"hbInterval":145},"sequence":"1783353572000"}`},
		{"update without temperature", `{"action":"update","deviceid":"a48013db7a","params":{"switch":"on"}}`},
		{"non-update action", `{"action":"sysmsg","deviceid":"a48013db7a","params":{"online":false}}`},
		{"unparseable temperature", `{"action":"update","deviceid":"a48013db7a","params":{"temperature":"unavailable"}}`},
		{"not json", `pong`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, ok := ParseTemperatureUpdate([]byte(tt.msg)); ok {
				t.Errorf("ParseTemperatureUpdate = %+v, ok=true, want ok=false", got)
			}
		})
	}
}

func TestParseTemperatureParams(t *testing.T) {
	got, ok := ParseTemperatureParams("a48013db7e", []byte(`{"temperature":"29.8","humidity":"55","trigTime":"1783353826000"}`))
	if !ok {
		t.Fatal("ok=false")
	}
	if got.DeviceID != "a48013db7e" || got.Temperature != 29.8 || !got.At.Equal(time.UnixMilli(1783353826000)) {
		t.Errorf("got %+v", got)
	}
	if _, ok := ParseTemperatureParams("x", []byte(`{"switch":"on"}`)); ok {
		t.Error("expected ok=false without temperature")
	}
}

func TestParseHumidityUpdate(t *testing.T) {
	tests := []struct {
		name string
		msg  string
		want float64
	}{
		{"integer hundredths", `{"action":"update","deviceid":"a48013db7a","params":{"temperature":"2780","humidity":"5830","trigTime":"1783353572000"}}`, 58.3},
		{"whole percent", `{"action":"update","deviceid":"a48013db7a","params":{"humidity":"58","trigTime":"1783353572000"}}`, 58},
		{"decimal percent", `{"action":"update","deviceid":"a48013db7e","params":{"humidity":"58.3","trigTime":"1783353572000"}}`, 58.3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ParseHumidityUpdate([]byte(tt.msg))
			if !ok {
				t.Fatal("ok=false")
			}
			if got.Humidity != tt.want || !got.At.Equal(time.UnixMilli(1783353572000)) {
				t.Errorf("got %+v, want humidity %v", got, tt.want)
			}
		})
	}
	for _, msg := range []string{
		`{"action":"update","deviceid":"a48013db7a","params":{"temperature":"2780"}}`,
		`{"action":"sysmsg","deviceid":"a48013db7a","params":{"humidity":"58"}}`,
		`{"action":"update","deviceid":"a48013db7a","params":{"humidity":"n/a"}}`,
	} {
		if _, ok := ParseHumidityUpdate([]byte(msg)); ok {
			t.Errorf("expected ok=false for %s", msg)
		}
	}
}
