package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLandingHandler_Renders(t *testing.T) {
	t.Setenv("VOLLEYBALL_URL", "https://setpoint.example.com")
	t.Setenv("GRAFANA_URL", "")
	t.Setenv("INFLUXDB_UI_URL", "")

	rr := httptest.NewRecorder()
	landingHandler(rr, httptest.NewRequest(http.MethodGet, "/", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("expected text/html, got %q", ct)
	}
	body := rr.Body.String()
	if !strings.Contains(body, `href="https://setpoint.example.com"`) {
		t.Errorf("volleyball link missing from landing page")
	}
	if !strings.Contains(body, "SetPoint") {
		t.Errorf("SetPoint card missing from landing page")
	}
}

func TestLandingHandler_HidesEmptyLinks(t *testing.T) {
	t.Setenv("INFLUXDB_UI_URL", "")

	rr := httptest.NewRecorder()
	landingHandler(rr, httptest.NewRequest(http.MethodGet, "/", nil))

	if strings.Contains(rr.Body.String(), "Data explorer") {
		t.Errorf("InfluxDB card should be hidden when INFLUXDB_UI_URL is empty")
	}
}

func TestLandingHandler_NotFound(t *testing.T) {
	rr := httptest.NewRecorder()
	landingHandler(rr, httptest.NewRequest(http.MethodGet, "/does-not-exist", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}
}

func TestHealthHandler_NoInflux(t *testing.T) {
	rr := httptest.NewRecorder()
	healthHandler(nil)(rr, httptest.NewRequest(http.MethodGet, "/api/health", nil))

	var resp map[string]string
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if resp["backend"] != "ok" || resp["influxdb"] != "unreachable" {
		t.Errorf("unexpected health response: %v", resp)
	}
}
