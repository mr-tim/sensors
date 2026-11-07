package main

import (
	"context"
	"errors"
	"io/fs"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"github.com/mr-tim/ewelink/internal/api"
	"github.com/mr-tim/ewelink/internal/config"
	"github.com/mr-tim/ewelink/internal/ewelink"
	"github.com/mr-tim/ewelink/internal/migrations"
	"github.com/mr-tim/ewelink/internal/readings"
	"github.com/mr-tim/ewelink/internal/token"
)

const (
	tokenFile = "token.json"
	apiAddr   = ":8080"

	// Refresh the access token a day before it expires (tokens last 30 days).
	refreshMargin = 24 * time.Hour
	// The refresh response doesn't include new expiry times; these are
	// the documented eWeLink lifetimes.
	accessTokenTTL  = 30 * 24 * time.Hour
	refreshTokenTTL = 60 * 24 * time.Hour
)

func main() {
	// Load .env from the current directory if there is one; real
	// environment variables take precedence over its values.
	if err := godotenv.Load(); err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Fatalf("loading .env: %v", err)
	}
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	store, err := token.NewStore(tokenFile)
	if err != nil {
		log.Fatalf("loading token store: %v", err)
	}
	client := ewelink.NewClient(cfg.EwelinkAppID, cfg.EwelinkAppSecret)

	if err := migrations.Up(cfg.DatabaseURL); err != nil {
		log.Fatalf("migrating database: %v", err)
	}
	db, err := readings.NewStore(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("connecting to postgres: %v", err)
	}
	defer db.Close()

	refreshDevices := func(ctx context.Context) (int, error) {
		tok := store.Get()
		if !tok.Valid() {
			return 0, api.ErrNotLoggedIn
		}
		return syncDevices(ctx, client, tok, db)
	}
	server := api.NewServer(client, store, db, refreshDevices, apiAddr)

	if store.Get().Valid() {
		log.Printf("using saved token from %s", tokenFile)
	} else {
		log.Printf("no valid token: open %s in a browser to log in", server.LoginURL())
	}

	go func() {
		if err := server.Run(); err != nil {
			log.Fatalf("api server: %v", err)
		}
	}()
	go runRefresher(ctx, client, store)
	go runDeviceSync(ctx, client, store, db)
	go runWsListener(ctx, client, store, db)

	<-ctx.Done()
	log.Println("shutting down")
}

// runRefresher keeps the access token fresh, refreshing it refreshMargin
// before expiry. When there is no usable token it just waits for the
// login flow to produce one.
func runRefresher(ctx context.Context, client *ewelink.Client, store *token.Store) {
	for {
		wait := time.Hour
		tok := store.Get()
		switch {
		case tok.Valid():
			refreshAt := time.UnixMilli(tok.AtExpiredTime).Add(-refreshMargin)
			wait = time.Until(refreshAt)
			if wait > 0 {
				break
			}
			fallthrough
		case tok.RefreshValid():
			log.Println("refresher: refreshing access token")
			data, err := client.Refresh(ctx, tok.Region, tok.RefreshToken)
			if err != nil {
				log.Printf("refresher: refresh failed, retrying in 5m: %v", err)
				wait = 5 * time.Minute
				break
			}
			now := time.Now()
			err = store.Set(token.Token{
				AccessToken:   data.At,
				AtExpiredTime: now.Add(accessTokenTTL).UnixMilli(),
				RefreshToken:  data.Rt,
				RtExpiredTime: now.Add(refreshTokenTTL).UnixMilli(),
				Region:        tok.Region,
			})
			if err != nil {
				log.Printf("refresher: saving refreshed token: %v", err)
			} else {
				log.Println("refresher: token refreshed and saved")
			}
			continue
		default:
			if tok != nil {
				log.Println("refresher: token fully expired, log in again at /login")
			}
		}

		select {
		case <-ctx.Done():
			return
		case <-store.Updated():
		case <-time.After(min(wait, time.Hour)):
		}
	}
}

// runDeviceSync loads the account's device details into the database
// once a valid token is available, retrying on failure.
func runDeviceSync(ctx context.Context, client *ewelink.Client, store *token.Store, db *readings.Store) {
	for ctx.Err() == nil {
		tok := store.Get()
		if !tok.Valid() {
			select {
			case <-ctx.Done():
				return
			case <-store.Updated():
			}
			continue
		}

		n, err := syncDevices(ctx, client, tok, db)
		if err == nil {
			log.Printf("devices: stored %d devices", n)
			return
		}
		log.Printf("devices: sync failed, retrying in 30s: %v", err)
		select {
		case <-ctx.Done():
		case <-time.After(30 * time.Second):
		}
	}
}

func syncDevices(ctx context.Context, client *ewelink.Client, tok *token.Token, db *readings.Store) (int, error) {
	devices, err := client.ListDevices(ctx, tok.Region, tok.AccessToken)
	if err != nil {
		return 0, err
	}
	rows := make([]readings.Device, len(devices))
	for i, d := range devices {
		rows[i] = readings.Device{DeviceID: d.DeviceID, Name: d.Name, ProductModel: d.ProductModel, Details: d.Raw}
		log.Printf("devices: %s = %q (%s)", d.DeviceID, d.Name, d.ProductModel)
	}
	if err := db.UpsertDevices(ctx, rows); err != nil {
		return 0, err
	}
	for _, d := range devices {
		storeCurrentReading(ctx, client, tok, db, d.DeviceID)
	}
	return len(rows), nil
}

// storeCurrentReading records the last temperature eWeLink has cached
// for a device. Failures are logged rather than returned: one device
// without a reading shouldn't fail the whole sync.
func storeCurrentReading(ctx context.Context, client *ewelink.Client, tok *token.Token, db *readings.Store, deviceID string) {
	params, err := client.DeviceParams(ctx, tok.Region, tok.AccessToken, deviceID)
	if err != nil {
		log.Printf("devices: fetching status for %s: %v", deviceID, err)
		return
	}
	log.Printf("devices: status for %s: %s", deviceID, params)
	if update, ok := ewelink.ParseTemperatureParams(deviceID, params); ok {
		storeReading(ctx, db, "devices", readings.Reading{
			DeviceID:   deviceID,
			Kind:       readings.Temperature,
			RecordedAt: update.At,
			Value:      update.Temperature,
		})
	}
	if update, ok := ewelink.ParseHumidityParams(deviceID, params); ok {
		storeReading(ctx, db, "devices", readings.Reading{
			DeviceID:   deviceID,
			Kind:       readings.Humidity,
			RecordedAt: update.At,
			Value:      update.Humidity,
		})
	}
}

func storeReading(ctx context.Context, db *readings.Store, source string, r readings.Reading) {
	if err := db.Insert(ctx, r); err != nil {
		log.Printf("%s: storing %s reading for %s: %v", source, r.Kind, r.DeviceID, err)
		return
	}
	log.Printf("%s: stored %s %.2f @ %s for %s", source, r.Kind, r.Value, r.RecordedAt.Format(time.RFC3339), r.DeviceID)
}

// runWsListener maintains a websocket connection whenever a valid token
// is available, storing temperature updates pushed by eWeLink. It
// reconnects on failure and picks up new tokens as they appear.
func runWsListener(ctx context.Context, client *ewelink.Client, store *token.Store, db *readings.Store) {
	for ctx.Err() == nil {
		tok := store.Get()
		if !tok.Valid() {
			select {
			case <-ctx.Done():
				return
			case <-store.Updated():
			}
			continue
		}

		if err := connectAndListen(ctx, client, tok, db); err != nil && ctx.Err() == nil {
			log.Printf("ws: connection lost: %v (reconnecting in 5s)", err)
			select {
			case <-ctx.Done():
			case <-time.After(5 * time.Second):
			}
		}
	}
}

func connectAndListen(ctx context.Context, client *ewelink.Client, tok *token.Token, db *readings.Store) error {
	apiKey, err := client.UserAPIKey(ctx, tok.Region, tok.AccessToken)
	if err != nil {
		return err
	}
	wsURL, err := client.DispatchWsURL(ctx, tok.Region)
	if err != nil {
		return err
	}
	log.Printf("ws: connecting to %s", wsURL)
	return client.Listen(ctx, wsURL, tok.AccessToken, apiKey, func(msg []byte) {
		if update, ok := ewelink.ParseTemperatureUpdate(msg); ok {
			storeReading(ctx, db, "ws", readings.Reading{
				DeviceID:   update.DeviceID,
				Kind:       readings.Temperature,
				RecordedAt: update.At,
				Value:      update.Temperature,
			})
		}
		if update, ok := ewelink.ParseHumidityUpdate(msg); ok {
			storeReading(ctx, db, "ws", readings.Reading{
				DeviceID:   update.DeviceID,
				Kind:       readings.Humidity,
				RecordedAt: update.At,
				Value:      update.Humidity,
			})
		}
	})
}
