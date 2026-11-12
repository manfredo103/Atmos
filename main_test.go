package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSensorHandler_Valid(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()

	t.Setenv("INFLUXDB_URL", ts.URL)
	t.Setenv("INFLUXDB_TOKEN", "test-token")
	t.Setenv("INFLUXDB_ORG", "test-org")
	t.Setenv("INFLUXDB_BUCKET", "test-bucket")

	payload := `{"temperature": 23.5, "humidity": 45.2, "co2": 800}`
	req := httptest.NewRequest(http.MethodPost, "/api/sensors", bytes.NewBufferString(payload))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	sensorHandler(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 OK, got %d", resp.StatusCode)
	}

	body := w.Body.String()
	if !strings.Contains(body, "success") {
		t.Errorf("expected response body to contain 'success', got %q", body)
	}
}

func TestSensorHandler_InfluxDBWriteSuccess(t *testing.T) {
	var receivedBody string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := new(bytes.Buffer)
		buf.ReadFrom(r.Body)
		receivedBody = buf.String()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()

	t.Setenv("INFLUXDB_URL", ts.URL)
	t.Setenv("INFLUXDB_TOKEN", "test-token")
	t.Setenv("INFLUXDB_ORG", "test-org")
	t.Setenv("INFLUXDB_BUCKET", "test-bucket")

	payload := `{"temperature": 21.5, "humidity": 55.0, "co2": 650}`
	req := httptest.NewRequest(http.MethodPost, "/api/sensors", bytes.NewBufferString(payload))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	sensorHandler(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 OK, got %d", resp.StatusCode)
	}

	if !strings.Contains(receivedBody, "room_atmosphere") {
		t.Errorf("expected line protocol to contain measurement 'room_atmosphere', got %q", receivedBody)
	}
	if !strings.Contains(receivedBody, "temperature=21.5") {
		t.Errorf("expected line protocol to contain 'temperature=21.5', got %q", receivedBody)
	}
	if !strings.Contains(receivedBody, "humidity=55") {
		t.Errorf("expected line protocol to contain 'humidity=55', got %q", receivedBody)
	}
	if !strings.Contains(receivedBody, "co2=650i") {
		t.Errorf("expected line protocol to contain 'co2=650i', got %q", receivedBody)
	}
}

func TestSensorHandler_InfluxDBError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	t.Setenv("INFLUXDB_URL", ts.URL)
	t.Setenv("INFLUXDB_TOKEN", "test-token")
	t.Setenv("INFLUXDB_ORG", "test-org")
	t.Setenv("INFLUXDB_BUCKET", "test-bucket")

	payload := `{"temperature": 23.5, "humidity": 45.2, "co2": 800}`
	req := httptest.NewRequest(http.MethodPost, "/api/sensors", bytes.NewBufferString(payload))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	sensorHandler(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected status 500 Internal Server Error, got %d", resp.StatusCode)
	}
}

func TestSensorHandler_MethodNotAllowed(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/sensors", nil)
	w := httptest.NewRecorder()

	sensorHandler(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("expected status 405 Method Not Allowed, got %d", resp.StatusCode)
	}
}

func TestSensorHandler_InvalidJSON(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/sensors", bytes.NewBufferString(`{invalid json}`))
	w := httptest.NewRecorder()

	sensorHandler(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected status 400 Bad Request, got %d", resp.StatusCode)
	}
}
