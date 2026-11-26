package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	"github.com/simonwhitaker/home-energy-otel/energy"
)

const (
	healthServerAddr = ":8080"
	mqttClientID     = "home-energy-otel"

	// silenceThreshold is how long without an electricity message before we
	// log that the display has gone quiet. The display publishes electricity
	// roughly every 10s.
	silenceThreshold     = 5 * time.Minute
	silenceCheckInterval = 30 * time.Second
)

// ingester turns MQTT messages from a Glow display into readings and hands
// them to every writer.
type ingester struct {
	logger  *log.Logger
	writers []energy.EnergyDataWriter

	mu sync.Mutex
	// lastTimestamp holds the newest message timestamp seen per device and
	// meter, so replayed (e.g. retained) or out-of-order messages are dropped.
	lastTimestamp map[string]time.Time
	// lastElectricity is when an electricity message last arrived (local
	// clock), and silent whether we've already logged that it's gone quiet.
	lastElectricity time.Time
	silent          bool
}

func newIngester(logger *log.Logger, writers []energy.EnergyDataWriter, now time.Time) *ingester {
	return &ingester{
		logger:          logger,
		writers:         writers,
		lastTimestamp:   map[string]time.Time{},
		lastElectricity: now,
	}
}

func (i *ingester) handle(topic string, payload []byte, now time.Time) {
	msg, err := energy.ParseGlowMessage(topic, payload)
	if err != nil {
		i.logger.Printf("Skipping message on %s: %v", topic, err)
		return
	}

	i.mu.Lock()
	key := msg.Device + "/" + string(msg.Meter)
	if last, seen := i.lastTimestamp[key]; seen && !msg.Timestamp.After(last) {
		i.mu.Unlock()
		return
	}
	i.lastTimestamp[key] = msg.Timestamp

	if msg.Meter == energy.GlowElectricity {
		if i.silent {
			i.logger.Printf("Electricity data resumed after %v", now.Sub(i.lastElectricity).Truncate(time.Second))
			i.silent = false
		}
		i.lastElectricity = now
	}
	i.mu.Unlock()

	// Write to every writer even if one of them fails; a broken exporter
	// shouldn't cost us the readings the others could have taken.
	var errs []error
	for _, w := range i.writers {
		if err := w.WriteReadings(msg.Readings); err != nil {
			errs = append(errs, err)
		}
	}
	if err := errors.Join(errs...); err != nil {
		i.logger.Printf("Error writing readings from %s: %v", topic, err)
	}
}

// checkSilence logs, once per run, when no electricity message has arrived
// for silenceThreshold. It doesn't affect readiness: a quiet display isn't a
// fault in this app.
func (i *ingester) checkSilence(now time.Time) {
	i.mu.Lock()
	defer i.mu.Unlock()

	if !i.silent && now.Sub(i.lastElectricity) > silenceThreshold {
		i.silent = true
		i.logger.Printf("No electricity data for %v", now.Sub(i.lastElectricity).Truncate(time.Second))
	}
}

func livenessHandler(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// readinessHandler reports ready while connected to the MQTT broker.
func readinessHandler(connected func() bool) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		if !connected() {
			http.Error(w, "not connected to MQTT broker", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}
}

func startHealthServer(logger *log.Logger, connected func() bool) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", livenessHandler)
	mux.HandleFunc("/readyz", readinessHandler(connected))

	server := &http.Server{
		Addr:    healthServerAddr,
		Handler: mux,
	}

	go func() {
		logger.Printf("Health server listening on %s", healthServerAddr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Fatalf("Health server failed: %v", err)
		}
	}()

	return server
}

func newMQTTClient(logger *log.Logger, ing *ingester, brokerURL, username, password, topicPrefix string) mqtt.Client {
	topics := map[string]byte{
		topicPrefix + "/+/STATE":    1,
		topicPrefix + "/+/SENSOR/+": 1,
	}

	opts := mqtt.NewClientOptions().
		AddBroker(brokerURL).
		SetClientID(mqttClientID).
		SetUsername(username).
		SetPassword(password).
		SetCleanSession(false).
		SetAutoReconnect(true).
		SetMaxReconnectInterval(2 * time.Minute).
		SetConnectionLostHandler(func(_ mqtt.Client, err error) {
			logger.Printf("Lost connection to MQTT broker: %v", err)
		}).
		SetReconnectingHandler(func(mqtt.Client, *mqtt.ClientOptions) {
			logger.Println("Reconnecting to MQTT broker...")
		}).
		SetOnConnectHandler(func(c mqtt.Client) {
			logger.Printf("Connected to MQTT broker %s", brokerURL)
			// Subscribe on every (re)connect: the broker may have lost the
			// session.
			token := c.SubscribeMultiple(topics, func(_ mqtt.Client, m mqtt.Message) {
				ing.handle(m.Topic(), m.Payload(), time.Now())
			})
			go func() {
				if token.Wait() && token.Error() != nil {
					logger.Printf("Subscribing to %v failed: %v", topics, token.Error())
				}
			}()
		})

	return mqtt.NewClient(opts)
}

// connectWithRetry makes the first connection to the broker, retrying with
// capped exponential backoff and logging why each attempt failed (paho's own
// ConnectRetry option retries silently, hiding e.g. a wrong password).
func connectWithRetry(ctx context.Context, logger *log.Logger, client mqtt.Client) {
	backoff := 5 * time.Second
	const maxBackoff = 2 * time.Minute

	for {
		token := client.Connect()
		token.Wait()
		if token.Error() == nil {
			return
		}

		logger.Printf("Connecting to MQTT broker failed: %v (retrying in %v)", token.Error(), backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, maxBackoff)
	}
}

func main() {
	logger := log.New(os.Stdout, "", log.LstdFlags)

	// Configure writers
	writers := []energy.EnergyDataWriter{
		energy.NewLoggerWriter(logger),
	}

	if _, ok := os.LookupEnv("OTEL_EXPORTER_OTLP_ENDPOINT"); ok {
		otelHostname := getEnvOrDefault("OTEL_HOSTNAME", "localhost")
		otelWriter, err := energy.NewOTelWriter(context.Background(), otelHostname, logger)
		if err != nil {
			logger.Fatalf("Failed to initialize OTel writer: %v", err)
		}
		writers = append(writers, otelWriter)
	} else {
		logger.Println("Skipping OTel; OTEL_EXPORTER_OTLP_ENDPOINT not set")
	}

	ing := newIngester(logger, writers, time.Now())
	client := newMQTTClient(logger, ing,
		getEnvOrDefault("MQTT_BROKER_URL", "tcp://mosquitto:1883"),
		os.Getenv("MQTT_USERNAME"),
		os.Getenv("MQTT_PASSWORD"),
		getEnvOrDefault("MQTT_TOPIC_PREFIX", "glow"),
	)

	healthServer := startHealthServer(logger, client.IsConnectionOpen)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Readiness reports the connection state while we retry. Once connected,
	// paho's auto-reconnect takes over.
	go connectWithRetry(ctx, logger, client)

	ticker := time.NewTicker(silenceCheckInterval)
	defer ticker.Stop()
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case now := <-ticker.C:
			ing.checkSilence(now)
		}
	}

	logger.Println("Shutting down...")
	client.Disconnect(250)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := healthServer.Shutdown(shutdownCtx); err != nil {
		logger.Printf("Error shutting down health server: %v", err)
	}
	for _, w := range writers {
		if err := w.Close(); err != nil {
			logger.Printf("Error closing writer: %v", err)
		}
	}
}
