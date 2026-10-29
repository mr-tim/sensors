package ewelink

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const oauthPageURL = "https://c2ccdn.coolkit.cc/oauth/index.html"

type Client struct {
	AppID     string
	AppSecret string

	http *http.Client
}

func NewClient(appID, appSecret string) *Client {
	return &Client{
		AppID:     appID,
		AppSecret: appSecret,
		http:      &http.Client{Timeout: 20 * time.Second},
	}
}

// tld returns the top-level domain coolkit uses for the region.
func tld(region string) string {
	if region == "cn" || region == "test" {
		return "cn"
	}
	return "cc"
}

func apiBase(region string) string {
	return fmt.Sprintf("https://%s-apia.coolkit.%s", region, tld(region))
}

// sign computes the HMAC-SHA256 signature eWeLink expects, over the
// exact bytes of the request body.
func (c *Client) sign(msg []byte) string {
	mac := hmac.New(sha256.New, []byte(c.AppSecret))
	mac.Write(msg)
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

const nonceChars = "abcdefghijklmnopqrstuvwxyz0123456789"

func nonce() string {
	b := make([]byte, 8)
	for i := range b {
		b[i] = nonceChars[rand.Intn(len(nonceChars))]
	}
	return string(b)
}

// LoginURL builds the oauth login page URL the user's browser should visit.
func (c *Client) LoginURL(redirectURL, state string) string {
	seq := strconv.FormatInt(time.Now().UnixMilli(), 10)
	params := url.Values{
		"clientId":      {c.AppID},
		"redirectUrl":   {redirectURL},
		"grantType":     {"authorization_code"},
		"state":         {state},
		"nonce":         {nonce()},
		"seq":           {seq},
		"authorization": {c.sign([]byte(c.AppID + "_" + seq))},
	}
	return oauthPageURL + "?" + params.Encode()
}

// envelope is the common eWeLink API response wrapper.
type envelope struct {
	Error int             `json:"error"`
	Msg   string          `json:"msg"`
	Data  json.RawMessage `json:"data"`
}

// postSigned sends a signed POST request and decodes data into out.
func (c *Client) postSigned(ctx context.Context, endpoint string, body any, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CK-Appid", c.AppID)
	req.Header.Set("X-CK-Nonce", nonce())
	req.Header.Set("Date", strconv.FormatInt(time.Now().UnixMilli(), 10))
	req.Header.Set("Authorization", "Sign "+c.sign(payload))
	return c.do(req, out)
}

// getBearer sends a GET request authenticated with the access token
// and decodes data into out.
func (c *Client) getBearer(ctx context.Context, endpoint, at string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-CK-Appid", c.AppID)
	req.Header.Set("X-CK-Nonce", nonce())
	req.Header.Set("Date", strconv.FormatInt(time.Now().UnixMilli(), 10))
	req.Header.Set("Authorization", "Bearer "+at)
	return c.do(req, out)
}

func (c *Client) do(req *http.Request, out any) error {
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	var env envelope
	if err := json.NewDecoder(res.Body).Decode(&env); err != nil {
		return fmt.Errorf("%s %s: decoding response (http %d): %w", req.Method, req.URL.Path, res.StatusCode, err)
	}
	if env.Error != 0 {
		return fmt.Errorf("%s %s: api error %d: %s", req.Method, req.URL.Path, env.Error, env.Msg)
	}
	if out != nil {
		if err := json.Unmarshal(env.Data, out); err != nil {
			return fmt.Errorf("%s %s: decoding data: %w", req.Method, req.URL.Path, err)
		}
	}
	return nil
}

type TokenData struct {
	AccessToken   string `json:"accessToken"`
	AtExpiredTime int64  `json:"atExpiredTime"`
	RefreshToken  string `json:"refreshToken"`
	RtExpiredTime int64  `json:"rtExpiredTime"`
}

// GetToken exchanges an oauth authorization code for tokens.
func (c *Client) GetToken(ctx context.Context, region, code, redirectURL string) (*TokenData, error) {
	body := map[string]string{
		"redirectUrl": redirectURL,
		"code":        code,
		"grantType":   "authorization_code",
	}
	var data TokenData
	if err := c.postSigned(ctx, apiBase(region)+"/v2/user/oauth/token", body, &data); err != nil {
		return nil, err
	}
	return &data, nil
}

type RefreshData struct {
	At string `json:"at"`
	Rt string `json:"rt"`
}

// Refresh exchanges a refresh token for a new access/refresh token pair.
func (c *Client) Refresh(ctx context.Context, region, rt string) (*RefreshData, error) {
	body := map[string]string{"rt": rt}
	var data RefreshData
	if err := c.postSigned(ctx, apiBase(region)+"/v2/user/refresh", body, &data); err != nil {
		return nil, err
	}
	return &data, nil
}

// UserAPIKey fetches the apikey of the user's first family, which the
// websocket handshake needs.
func (c *Client) UserAPIKey(ctx context.Context, region, at string) (string, error) {
	var data struct {
		FamilyList []struct {
			APIKey string `json:"apikey"`
		} `json:"familyList"`
	}
	if err := c.getBearer(ctx, apiBase(region)+"/v2/family", at, &data); err != nil {
		return "", err
	}
	if len(data.FamilyList) == 0 {
		return "", fmt.Errorf("no families returned for user")
	}
	return data.FamilyList[0].APIKey, nil
}

// DispatchWsURL asks the dispatch service for the websocket server to use.
func (c *Client) DispatchWsURL(ctx context.Context, region string) (string, error) {
	endpoint := fmt.Sprintf("https://%s-dispa.coolkit.%s/dispatch/app", region, tld(region))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	res, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	var data struct {
		Port   int    `json:"port"`
		Domain string `json:"domain"`
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(res.Body).Decode(&data); err != nil {
		return "", fmt.Errorf("decoding dispatch response: %w", err)
	}
	if data.Reason != "ok" {
		return "", fmt.Errorf("dispatch failed: %s", data.Reason)
	}
	return fmt.Sprintf("wss://%s:%d/api/ws", data.Domain, data.Port), nil
}

// Device is a device registered to the user's account.
type Device struct {
	DeviceID     string
	Name         string
	ProductModel string
	// Raw is the full itemData object as returned by the API.
	Raw json.RawMessage
}

// ListDevices fetches the devices registered to the user's account.
func (c *Client) ListDevices(ctx context.Context, region, at string) ([]Device, error) {
	var data struct {
		ThingList []struct {
			ItemData json.RawMessage `json:"itemData"`
		} `json:"thingList"`
	}
	if err := c.getBearer(ctx, apiBase(region)+"/v2/device/thing", at, &data); err != nil {
		return nil, err
	}
	devices := make([]Device, 0, len(data.ThingList))
	for _, t := range data.ThingList {
		var item struct {
			DeviceID     string `json:"deviceid"`
			Name         string `json:"name"`
			ProductModel string `json:"productModel"`
		}
		if err := json.Unmarshal(t.ItemData, &item); err != nil {
			return nil, fmt.Errorf("decoding thing: %w", err)
		}
		if item.DeviceID == "" {
			continue
		}
		devices = append(devices, Device{
			DeviceID:     item.DeviceID,
			Name:         item.Name,
			ProductModel: item.ProductModel,
			Raw:          t.ItemData,
		})
	}
	return devices, nil
}

// DeviceParams fetches the last state eWeLink's cloud has for a device,
// returned as the raw params object. This is cached server-side, not a
// live reading from the device.
func (c *Client) DeviceParams(ctx context.Context, region, at, deviceID string) (json.RawMessage, error) {
	endpoint := apiBase(region) + "/v2/device/thing/status?type=1&id=" + url.QueryEscape(deviceID)
	var data struct {
		Params json.RawMessage `json:"params"`
	}
	if err := c.getBearer(ctx, endpoint, at, &data); err != nil {
		return nil, err
	}
	return data.Params, nil
}
