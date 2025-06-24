package main

import (
	"context"
	"encoding/json"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	influxdb2 "github.com/influxdata/influxdb-client-go/v2"
)

type MetricsRequest struct {
	ServiceName string             `json:"service_name"`
	Timestamp   time.Time          `json:"timestamp"`
	Metrics     map[string]float64 `json:"metrics"`
}

type InfluxdbConnect struct {
	client influxdb2.Client
	bucket string
	org    string
}

var influxConnection *InfluxdbConnect

func NewInfluxConnect() *InfluxdbConnect {
	client := influxdb2.NewClient(os.Getenv("INFLUXDB_URL"), os.Getenv("INFLUXDB_TOKEN"))
	return &InfluxdbConnect{
		client: client,
		org:    os.Getenv("INFLUXDB_ORG"),
		bucket: os.Getenv("INFLUXDB_BUCKET"),
	}
}

func metricsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Only POST allowed", http.StatusMethodNotAllowed)
		return
	}

	var req MetricsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	if req.ServiceName == "" || req.Timestamp.IsZero() || len(req.Metrics) == 0 {
		http.Error(w, "Missing required fields", http.StatusBadRequest)
		return
	}

	writeAPI := influxConnection.client.WriteAPIBlocking(influxConnection.org, influxConnection.bucket)

	for key, value := range req.Metrics {
		slog.Info("Received metric", "metric", key, "value", value)
		point := influxdb2.NewPoint(
			key,
			map[string]string{"service": req.ServiceName},
			map[string]interface{}{"value": value},
			req.Timestamp,
		)

		if err := writeAPI.WritePoint(r.Context(), point); err != nil {
			slog.Error("Influx write error", "error", err)
			http.Error(w, "Failed to write to InfluxDB", http.StatusInternalServerError)
			return
		}
	}

	w.WriteHeader(http.StatusAccepted)
	w.Write([]byte("Metrics stored"))
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	influxConnection = NewInfluxConnect()
	defer influxConnection.client.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", metricsHandler)
	mux.HandleFunc("/health", healthHandler)
	mux.HandleFunc("/ready", healthHandler)

	server := &http.Server{
		Addr:    ":8080",
		Handler: mux,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Println("API-service listening on :8080")
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server error: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("Shutting down API-service...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	server.Shutdown(shutdownCtx)
}
