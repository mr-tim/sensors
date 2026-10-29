package ewelink

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"strconv"
	"time"

	"github.com/gorilla/websocket"
)

const defaultHbInterval = 145 // seconds, matching the official client

// Listen connects to the websocket server, performs the userOnline
// handshake, keeps the connection alive with heartbeats, and invokes
// onMessage for every message received. It blocks until the connection
// drops or ctx is cancelled.
func (c *Client) Listen(ctx context.Context, wsURL, at, userAPIKey string, onMessage func([]byte)) error {
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		return fmt.Errorf("dialing %s: %w", wsURL, err)
	}
	defer conn.Close()

	handshake, err := json.Marshal(map[string]string{
		"action":    "userOnline",
		"at":        at,
		"apikey":    userAPIKey,
		"appid":     c.AppID,
		"nonce":     nonce(),
		"userAgent": "app",
		"sequence":  strconv.FormatInt(time.Now().UnixMilli(), 10),
	})
	if err != nil {
		return err
	}
	if err := conn.WriteMessage(websocket.TextMessage, handshake); err != nil {
		return fmt.Errorf("sending userOnline: %w", err)
	}

	// The heartbeat goroutine sends "ping" on an interval the server
	// specifies in its handshake response. Writes are funnelled through
	// it via pingCh so we never write from two goroutines at once.
	hbCh := make(chan int, 1)
	done := make(chan struct{})
	defer close(done)
	go func() {
		interval := time.Duration(defaultHbInterval) * time.Second
		timer := time.NewTimer(interval)
		defer timer.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				conn.Close()
				return
			case hb := <-hbCh:
				// Same jitter the official client applies: 80-100% of the interval.
				interval = time.Duration(float64(hb)*(0.8+0.2*rand.Float64())) * time.Second
				timer.Reset(interval)
			case <-timer.C:
				if err := conn.WriteMessage(websocket.TextMessage, []byte("ping")); err != nil {
					conn.Close()
					return
				}
				timer.Reset(interval)
			}
		}
	}()

	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("reading message: %w", err)
		}
		if string(msg) == "pong" {
			continue
		}

		// The handshake response carries the heartbeat config.
		var hello struct {
			Error  int `json:"error"`
			Config struct {
				Hb         int `json:"hb"`
				HbInterval int `json:"hbInterval"`
			} `json:"config"`
		}
		if json.Unmarshal(msg, &hello) == nil && hello.Config.Hb != 0 {
			if hello.Error != 0 {
				return fmt.Errorf("userOnline handshake rejected: error %d", hello.Error)
			}
			log.Printf("ws: handshake ok, heartbeat every ~%ds", hello.Config.HbInterval)
			hbCh <- hello.Config.HbInterval
			continue
		}

		onMessage(msg)
	}
}
