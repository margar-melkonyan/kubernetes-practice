package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"math"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/shirou/gopsutil/cpu"
	"github.com/shirou/gopsutil/mem"
)

var (
	processedTasks = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "worker_processed_tasks_total",
			Help: "Total number of processed tasks",
		})

	processingDuration = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "worker_task_processing_duration_seconds",
			Help:    "Duration of task processing in seconds",
			Buckets: prometheus.LinearBuckets(0.1, 0.2, 10),
		})

	isActive int32 = 1

	atomicProcessedTasks uint64
	atomicTotalDuration  atomic.Value
)

type MetricsPayload struct {
	ServiceName string             `json:"service_name"`
	Timestamp   string             `json:"timestamp"`
	Metrics     map[string]float64 `json:"metrics"`
}

func processTask() {
	start := time.Now()

	x := 0.0001
	for i := 0; i < 50_000_000; i++ {
		x += math.Sqrt(float64(i))
	}

	duration := time.Since(start).Seconds()

	processedTasks.Inc()
	processingDuration.Observe(duration)

	atomic.AddUint64(&atomicProcessedTasks, 1)

	for {
		oldVal := atomicTotalDuration.Load()
		var oldFloat float64
		if oldVal != nil {
			oldFloat = oldVal.(float64)
		}

		newVal := oldFloat + duration
		if atomicTotalDuration.CompareAndSwap(oldVal, newVal) {
			break
		}
	}
}

func workloadGenerator(ctx context.Context, workers int) {
	for i := 0; i < workers; i++ {
		go func() {
			for {
				if atomic.LoadInt32(&isActive) == 1 {
					processTask()
				}

				select {
				case <-ctx.Done():
					return
				default:
					time.Sleep(10 * time.Millisecond)
				}
			}
		}()
	}
}

func getSystemMetrics() (cpuPercent float64, memUsedPercent float64, err error) {
	cpuPercents, err := cpu.Percent(0, false)
	if err != nil {
		return 0, 0, err
	}
	memStat, err := mem.VirtualMemory()
	if err != nil {
		return 0, 0, err
	}
	return cpuPercents[0], memStat.UsedPercent, nil
}

func gatherAndSendStats(apiURL string) {
	count := atomic.LoadUint64(&atomicProcessedTasks)
	totalDurationVal := atomicTotalDuration.Load()
	var totalDuration float64
	if totalDurationVal != nil {
		totalDuration = totalDurationVal.(float64)
	}

	var avg float64
	if count > 0 {
		avg = totalDuration / float64(count)
	}

	cpuUsage, memUsage, err := getSystemMetrics()
	if err != nil {
		log.Println("Error getting system metrics:", err)
		return
	}

	payload := MetricsPayload{
		ServiceName: "worker",
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
		Metrics: map[string]float64{
			"processed_tasks":       float64(count),
			"avg_processing_time_s": avg,
			"cpu_usage_percent":     cpuUsage,
			"memory_usage_percent":  memUsage,
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		log.Println("Error marshaling JSON:", err)
		return
	}

	resp, err := http.Post(apiURL, "application/json", bytes.NewBuffer(body))
	if err != nil {
		log.Println("Error sending stats to api-service:", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("Non-OK response from api-service: %d\n", resp.StatusCode)
		return
	}

	log.Println("Stats sent successfully:", string(body))
}

func main() {
	prometheus.MustRegister(processedTasks)
	prometheus.MustRegister(processingDuration)

	atomicTotalDuration.Store(float64(0))

	apiURL := os.Getenv("API_URL")

	ctx, cancel := context.WithCancel(context.Background())
	go workloadGenerator(ctx, 5)

	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				gatherAndSendStats(apiURL)
			}
		}
	}()

	http.Handle("/metrics", promhttp.Handler())

	http.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		atomic.StoreInt32(&isActive, 1)
		w.Write([]byte("Workload started\n"))
	})

	http.HandleFunc("/stop", func(w http.ResponseWriter, r *http.Request) {
		atomic.StoreInt32(&isActive, 0)
		w.Write([]byte("Workload stopped\n"))
	})

	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK\n"))
	})

	http.HandleFunc("/ready", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("READY\n"))
	})

	srv := &http.Server{Addr: ":8080"}

	go func() {
		log.Println("Starting worker-service on :8080")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("ListenAndServe(): %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	<-stop

	log.Println("Shutting down worker-service...")
	cancel()
	srv.Shutdown(context.Background())
}
