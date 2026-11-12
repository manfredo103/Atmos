package main

import (
	"context"
	"embed"
	"encoding/json"
	"html/template"
	"log"
	"net/http"
	"os"
	"time"

	influxdb2 "github.com/influxdata/influxdb-client-go/v2"
)

//go:embed web/templates/*.html
var templateFS embed.FS

var (
	landingTemplate   = template.Must(template.ParseFS(templateFS, "web/templates/partials.html", "web/templates/index.html"))
	dashboardTemplate = template.Must(template.ParseFS(templateFS, "web/templates/partials.html", "web/templates/dashboard.html"))
)

// LandingLink is a single card on the landing page.
type LandingLink struct {
	Title       string
	Tag         string
	Description string
	URL         string
	External    bool
}

// LandingPageData is passed to the landing page template.
type LandingPageData struct {
	AppName string
	Year    int
	Page    string
	Apps    []LandingLink
	Tools   []LandingLink
}

// DashboardPageData is passed to the dashboard template.
type DashboardPageData struct {
	AppName         string
	Year            int
	Page            string
	GrafanaURL      string
	GrafanaEmbedURL string
}

// envOr returns the value of the environment variable or the given default.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// buildLandingData assembles the cards shown on the landing page.
// All URLs can be overridden via environment variables; cards with an
// empty URL are hidden.
func buildLandingData() LandingPageData {
	volleyballDefault := "http://localhost:5173"
	if os.Getenv("STATIC_DIR") != "" {
		volleyballDefault = "/volleyball/"
	}

	apps := []LandingLink{
		{
			Title:       "SetPoint",
			Tag:         "Volleyball",
			Description: "Live scoring, rotations and substitutions for volleyball matches, with a full match archive.",
			URL:         envOr("VOLLEYBALL_URL", volleyballDefault),
		},
		{
			Title:       "Climate Dashboard",
			Tag:         "Sensors",
			Description: "Current and historical indoor temperature, humidity and CO2, compared with the outdoor weather in Erlangen.",
			URL:         "/dashboard",
		},
	}

	tools := []LandingLink{
		{
			Title:       "Grafana",
			Tag:         "Monitoring",
			Description: "Detailed Grafana dashboards for all stored sensor data.",
			URL:         envOr("GRAFANA_URL", "http://localhost:3000"),
		},
		{
			Title:       "InfluxDB",
			Tag:         "Database",
			Description: "Data explorer and bucket management for all stored time series.",
			URL:         os.Getenv("INFLUXDB_UI_URL"),
		},
		{
			Title:       "System Status",
			Tag:         "API",
			Description: "JSON health check of the backend and the InfluxDB connection.",
			URL:         "/api/health",
		},
	}

	data := LandingPageData{
		AppName: envOr("APP_NAME", "Atmos"),
		Year:    time.Now().Year(),
		Page:    "home",
	}
	for _, l := range apps {
		if l.URL != "" {
			l.External = isExternal(l.URL)
			data.Apps = append(data.Apps, l)
		}
	}
	for _, l := range tools {
		if l.URL != "" {
			l.External = isExternal(l.URL)
			data.Tools = append(data.Tools, l)
		}
	}
	return data
}

func isExternal(url string) bool {
	return len(url) > 0 && url[0] != '/'
}

// landingHandler serves the landing page at "/".
func landingHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := landingTemplate.ExecuteTemplate(w, "index.html", buildLandingData()); err != nil {
		log.Printf("landing page render failed: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

// dashboardHandler serves the climate dashboard at "/dashboard".
// If GRAFANA_EMBED_URL is set, a "Grafana" tab embedding that URL is shown.
func dashboardHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	data := DashboardPageData{
		AppName:         envOr("APP_NAME", "Atmos"),
		Year:            time.Now().Year(),
		Page:            "dashboard",
		GrafanaURL:      envOr("GRAFANA_URL", "http://localhost:3000"),
		GrafanaEmbedURL: os.Getenv("GRAFANA_EMBED_URL"),
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := dashboardTemplate.ExecuteTemplate(w, "dashboard.html", data); err != nil {
		log.Printf("dashboard render failed: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

// healthHandler reports whether the backend and InfluxDB are reachable.
func healthHandler(client influxdb2.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		influxStatus := "unreachable"
		if client != nil {
			if ok, err := client.Ping(ctx); err == nil && ok {
				influxStatus = "ok"
			}
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"backend":  "ok",
			"influxdb": influxStatus,
			"time":     time.Now().UTC().Format(time.RFC3339),
		})
	}
}
