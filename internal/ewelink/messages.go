package ewelink

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// TemperatureUpdate is a temperature reading pushed by a device over
// the websocket, e.g.:
//
//	{"action":"update","deviceid":"a48013db7a","apikey":"...","userAgent":"device",
//	 "d_seq":28009,"params":{"temperature":"2920","trigTime":"1783345672000","tempComfortStatus":2}}
//
// where trigTime is a millisecond epoch timestamp. Depending on the
// device model, temperature is either integer hundredths of a degree
// ("2780", SNZB-02P) or decimal degrees ("29.8", SNZB-02LD).
type TemperatureUpdate struct {
	DeviceID    string
	Temperature float64
	At          time.Time
}

// ParseTemperatureUpdate extracts a temperature reading from a ws
// message. ok is false for messages that aren't temperature updates.
func ParseTemperatureUpdate(msg []byte) (TemperatureUpdate, bool) {
	var m struct {
		Action   string `json:"action"`
		DeviceID string `json:"deviceid"`
		Params   struct {
			Temperature string `json:"temperature"`
			TrigTime    string `json:"trigTime"`
		} `json:"params"`
	}
	if err := json.Unmarshal(msg, &m); err != nil {
		return TemperatureUpdate{}, false
	}
	if m.Action != "update" || m.DeviceID == "" || m.Params.Temperature == "" {
		return TemperatureUpdate{}, false
	}
	temp, err := parseTemperature(m.Params.Temperature)
	if err != nil {
		return TemperatureUpdate{}, false
	}

	return TemperatureUpdate{
		DeviceID:    m.DeviceID,
		Temperature: temp,
		At:          parseTrigTime(m.Params.TrigTime),
	}, true
}

// ParseTemperatureParams extracts a temperature reading from a device's
// params object, as returned by the thing status endpoint. ok is false
// if the device has no (parseable) temperature.
func ParseTemperatureParams(deviceID string, params []byte) (TemperatureUpdate, bool) {
	var p struct {
		Temperature string `json:"temperature"`
		TrigTime    string `json:"trigTime"`
	}
	if err := json.Unmarshal(params, &p); err != nil || p.Temperature == "" {
		return TemperatureUpdate{}, false
	}
	temp, err := parseTemperature(p.Temperature)
	if err != nil {
		return TemperatureUpdate{}, false
	}
	return TemperatureUpdate{DeviceID: deviceID, Temperature: temp, At: parseTrigTime(p.TrigTime)}, true
}

// parseTrigTime converts a millisecond epoch string, falling back to
// the current time when it is missing.
func parseTrigTime(s string) time.Time {
	if ms, err := strconv.ParseInt(s, 10, 64); err == nil {
		return time.UnixMilli(ms)
	}
	return time.Now()
}

// parseTemperature handles both wire formats: decimal degrees ("29.8")
// and integer hundredths of a degree ("2780").
func parseTemperature(s string) (float64, error) {
	if strings.Contains(s, ".") {
		return strconv.ParseFloat(s, 64)
	}
	hundredths, err := strconv.Atoi(s)
	return float64(hundredths) / 100, err
}

// HumidityUpdate is a relative humidity reading (percent) pushed by a
// device. It arrives in the same kind of update message as temperature,
// in params.humidity, with the same trigTime.
type HumidityUpdate struct {
	DeviceID string
	Humidity float64
	At       time.Time
}

// ParseHumidityUpdate extracts a humidity reading from a ws message. ok
// is false for messages that aren't humidity updates.
func ParseHumidityUpdate(msg []byte) (HumidityUpdate, bool) {
	var m struct {
		Action   string          `json:"action"`
		DeviceID string          `json:"deviceid"`
		Params   json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(msg, &m); err != nil || m.Action != "update" || m.DeviceID == "" {
		return HumidityUpdate{}, false
	}
	return ParseHumidityParams(m.DeviceID, m.Params)
}

// ParseHumidityParams extracts a humidity reading from a device's params
// object. ok is false if the device has no (parseable) humidity.
func ParseHumidityParams(deviceID string, params []byte) (HumidityUpdate, bool) {
	var p struct {
		Humidity string `json:"humidity"`
		TrigTime string `json:"trigTime"`
	}
	if err := json.Unmarshal(params, &p); err != nil || p.Humidity == "" {
		return HumidityUpdate{}, false
	}
	h, err := parseHumidity(p.Humidity)
	if err != nil {
		return HumidityUpdate{}, false
	}
	return HumidityUpdate{DeviceID: deviceID, Humidity: h, At: parseTrigTime(p.TrigTime)}, true
}

// parseHumidity handles decimal percent ("55.5") and integers, which are
// either whole percent ("55") or hundredths of a percent ("5550") like
// temperature. The two can be told apart because humidity never exceeds
// 100, so an integer above 100 must be in hundredths.
func parseHumidity(s string) (float64, error) {
	if strings.Contains(s, ".") {
		return strconv.ParseFloat(s, 64)
	}
	n, err := strconv.Atoi(s)
	if n > 100 {
		return float64(n) / 100, err
	}
	return float64(n), err
}
