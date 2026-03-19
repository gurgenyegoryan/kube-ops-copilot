package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

func main() {
	var (
		mode = flag.String("mode", envOrDefault("E2E_BACKEND_MODE", "prometheus"), "Backend mode: prometheus|loki|jaeger|tempo")
		addr = flag.String("listen", envOrDefault("E2E_BACKEND_LISTEN", ":8080"), "Listen address")
	)
	flag.Parse()

	mux := http.NewServeMux()
	switch strings.ToLower(strings.TrimSpace(*mode)) {
	case "prometheus":
		registerPrometheus(mux)
	case "loki":
		registerLoki(mux)
	case "jaeger":
		registerJaeger(mux)
	case "tempo":
		registerTempo(mux)
	default:
		log.Fatalf("unsupported mode %q", *mode)
	}

	server := &http.Server{
		Addr:              *addr,
		Handler:           loggingMiddleware(strings.TrimSpace(*mode), mux),
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Printf("starting fake telemetry backend mode=%s addr=%s", *mode, *addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func envOrDefault(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func loggingMiddleware(mode string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("mode=%s method=%s path=%s rawQuery=%s", mode, r.Method, r.URL.Path, r.URL.RawQuery)
		next.ServeHTTP(w, r)
	})
}

func registerPrometheus(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/status/buildinfo", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"status": "success",
			"data": map[string]any{
				"version": "v2.53.0-koc-e2e",
			},
		})
	})
	mux.HandleFunc("/api/v1/query", func(w http.ResponseWriter, r *http.Request) {
		query := strings.TrimSpace(r.URL.Query().Get("query"))
		switch {
		case strings.Contains(query, "up"):
			writeVector(w, []map[string]any{{
				"metric": map[string]string{"job": "koc-e2e-prometheus"},
				"value":  promValue(1),
			}})
		case strings.Contains(query, "OOMKilled"):
			writeVector(w, []map[string]any{{
				"metric": workloadMetric(),
				"value":  promValue(1),
			}})
		case strings.Contains(query, "restarts_total"), strings.Contains(query, "changes("):
			writeVector(w, []map[string]any{{
				"metric": workloadMetric(),
				"value":  promValue(7),
			}})
		default:
			writeVector(w, []map[string]any{{
				"metric": map[string]string{"job": "koc-e2e-prometheus"},
				"value":  promValue(1),
			}})
		}
	})
	mux.HandleFunc("/api/v1/query_range", func(w http.ResponseWriter, r *http.Request) {
		query := strings.TrimSpace(r.URL.Query().Get("query"))
		switch {
		case strings.Contains(query, "container_cpu_cfs_throttled_seconds_total"):
			writeMatrix(w, workloadMetric(), []float64{0.02, 0.08, 0.27})
		case strings.Contains(query, "container_cpu_usage_seconds_total"), strings.Contains(query, "node_namespace_pod_container:container_cpu_usage_seconds_total:sum_irate"):
			writeMatrix(w, workloadMetric(), []float64{0.35, 0.92, 1.31})
		case strings.Contains(query, "container_memory_working_set_bytes"), strings.Contains(query, "container_memory_usage_bytes"), strings.Contains(query, "node_namespace_pod_container:container_memory_working_set_bytes"):
			writeMatrix(w, workloadMetric(), []float64{800 * 1024 * 1024, 1300 * 1024 * 1024, 1700 * 1024 * 1024})
		default:
			writeMatrix(w, workloadMetric(), []float64{1})
		}
	})
}

func registerLoki(mux *http.ServeMux) {
	mux.HandleFunc("/loki/api/v1/labels", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"status": "success",
			"data":   []string{"exported_namespace", "exported_pod", "job"},
		})
	})
	mux.HandleFunc("/loki/api/v1/query", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"status": "success",
			"data": map[string]any{
				"resultType": "vector",
				"result": []map[string]any{
					{
						"metric": map[string]string{
							"exported_namespace": "payments",
							"exported_pod":       "payments-worker-0",
						},
						"value": promValue(14),
					},
					{
						"metric": map[string]string{
							"exported_namespace": "payments",
							"exported_pod":       "payments-api-live-e2e",
						},
						"value": promValue(9),
					},
				},
			},
		})
	})
}

func registerJaeger(mux *http.ServeMux) {
	mux.HandleFunc("/api/services", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"data": []string{"payments-api", "payments-worker"}})
	})
	mux.HandleFunc("/api/services/payments-api/operations", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"data": []string{"GET /checkout", "POST /payments"}})
	})
	mux.HandleFunc("/jaeger/api/services", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"data": []string{"payments-api", "payments-worker"}})
	})
}

func registerTempo(mux *http.ServeMux) {
	mux.HandleFunc("/ready", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready"))
	})
	mux.HandleFunc("/api/search/tag/service.name/values", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"tagValues": []string{"payments-api", "payments-worker"}})
	})
	mux.HandleFunc("/api/search/tag/service/values", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"tagValues": []string{"payments-api", "payments-worker"}})
	})
	mux.HandleFunc("/api/search/tags", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"tagNames": []string{"service.name", "namespace", "pod"}})
	})
}

func workloadMetric() map[string]string {
	return map[string]string{
		"exported_namespace": "payments",
		"exported_pod":       "payments-worker-0",
	}
}

func promValue(v float64) []any {
	return []any{float64(time.Now().Unix()), fmt.Sprintf("%.3f", v)}
}

func writeVector(w http.ResponseWriter, result []map[string]any) {
	writeJSON(w, map[string]any{
		"status": "success",
		"data": map[string]any{
			"resultType": "vector",
			"result":     result,
		},
	})
}

func writeMatrix(w http.ResponseWriter, metric map[string]string, values []float64) {
	series := make([][]any, 0, len(values))
	now := time.Now().Add(-time.Duration(len(values)) * 15 * time.Minute)
	for _, value := range values {
		now = now.Add(15 * time.Minute)
		series = append(series, []any{float64(now.Unix()), fmt.Sprintf("%.3f", value)})
	}
	writeJSON(w, map[string]any{
		"status": "success",
		"data": map[string]any{
			"resultType": "matrix",
			"result": []map[string]any{{
				"metric": metric,
				"values": series,
			}},
		},
	})
}

func writeJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	if err := enc.Encode(payload); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
