package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	influxdb2 "github.com/influxdata/influxdb-client-go/v2"
)

// newClimateMock returns a fake InfluxDB that answers Flux queries with
// annotated CSV and records the last query it received.
func newClimateMock(t *testing.T, lastQuery *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/v2/query") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body struct {
			Query string `json:"query"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		*lastQuery = body.Query

		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		if strings.Contains(body.Query, "last()") {
			_, _ = w.Write([]byte(buildAnnotatedFluxCSV([][]string{
				{"#datatype", "string", "long", "dateTime:RFC3339", "string", "string", "double"},
				{"#group", "false", "false", "false", "true", "true", "false"},
				{"#default", "_result", "", "", "", "", ""},
				{"", "result", "table", "_time", "_field", "location", "_value"},
				{"", "_result", "0", "2026-11-12T15:00:00Z", "temperature", "inside", "21.5"},
				{"", "_result", "1", "2026-11-12T15:00:00Z", "humidity", "inside", "48"},
				{"", "_result", "2", "2026-11-12T15:00:00Z", "co2", "inside", "950"},
				{"", "_result", "3", "2026-11-12T14:45:00Z", "temperature", "outside", "7.2"},
			})))
			return
		}
		_, _ = w.Write([]byte(buildAnnotatedFluxCSV([][]string{
			{"#datatype", "string", "long", "dateTime:RFC3339", "string", "double"},
			{"#group", "false", "false", "false", "true", "false"},
			{"#default", "_result", "", "", "", ""},
			{"", "result", "table", "_time", "location", "_value"},
			{"", "_result", "0", "2026-11-12T14:00:00Z", "inside", "21.0"},
			{"", "_result", "0", "2026-11-12T14:10:00Z", "inside", "21.4"},
			{"", "_result", "1", "2026-11-12T14:00:00Z", "outside", "6.9"},
		})))
	}))
}

func TestClimateCurrent(t *testing.T) {
	var q string
	ts := newClimateMock(t, &q)
	defer ts.Close()
	client := influxdb2.NewClient(ts.URL, "test-token")
	defer client.Close()

	rr := httptest.NewRecorder()
	NewClimateAPI(client, "org", "sensor_data").handleCurrent(rr, httptest.NewRequest(http.MethodGet, "/api/climate/current", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Locations map[string]ClimateReading `json:"locations"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	in := resp.Locations["inside"]
	if in.Temperature == nil || *in.Temperature != 21.5 || in.CO2 == nil || *in.CO2 != 950 {
		t.Errorf("unexpected inside reading: %+v", in)
	}
	out := resp.Locations["outside"]
	if out.Temperature == nil || *out.Temperature != 7.2 || out.CO2 != nil {
		t.Errorf("unexpected outside reading: %+v", out)
	}
	if !strings.Contains(q, `from(bucket: "sensor_data")`) {
		t.Errorf("query used wrong bucket: %s", q)
	}
}

func TestClimateHistory(t *testing.T) {
	var q string
	ts := newClimateMock(t, &q)
	defer ts.Close()
	client := influxdb2.NewClient(ts.URL, "test-token")
	defer client.Close()

	rr := httptest.NewRecorder()
	NewClimateAPI(client, "org", "sensor_data").handleHistory(rr,
		httptest.NewRequest(http.MethodGet, "/api/climate/history?field=temperature&range=7d", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Series map[string][]ClimatePoint `json:"series"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(resp.Series["inside"]) != 2 || len(resp.Series["outside"]) != 1 {
		t.Errorf("unexpected series: %+v", resp.Series)
	}
	if !strings.Contains(q, "range(start: -7d)") || !strings.Contains(q, "every: 1h") {
		t.Errorf("unexpected range/window in query: %s", q)
	}
}

func TestClimateHistory_RejectsInvalidInput(t *testing.T) {
	api := NewClimateAPI(influxdb2.NewClient("http://127.0.0.1:1", "x"), "org", "bucket")
	for _, url := range []string{
		"/api/climate/history?field=password",
		`/api/climate/history?field=temperature") |> drop(`,
		"/api/climate/history?range=999y",
	} {
		rr := httptest.NewRecorder()
		api.handleHistory(rr, httptest.NewRequest(http.MethodGet, strings.ReplaceAll(url, " ", "%20"), nil))
		if rr.Code != http.StatusBadRequest {
			t.Errorf("%s: expected 400, got %d", url, rr.Code)
		}
	}
}

func TestClimate_NotConfigured(t *testing.T) {
	rr := httptest.NewRecorder()
	NewClimateAPI(nil, "", "").handleCurrent(rr, httptest.NewRequest(http.MethodGet, "/api/climate/current", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rr.Code)
	}
}

func TestDashboardHandler_Renders(t *testing.T) {
	t.Setenv("GRAFANA_EMBED_URL", "")
	rr := httptest.NewRecorder()
	dashboardHandler(rr, httptest.NewRequest(http.MethodGet, "/dashboard", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "Climate Dashboard") || !strings.Contains(body, "/api/climate/history") {
		t.Errorf("dashboard page missing expected content")
	}
	if strings.Contains(body, "<iframe") {
		t.Errorf("Grafana iframe should be hidden when GRAFANA_EMBED_URL is empty")
	}
}

func TestDashboardHandler_GrafanaEmbed(t *testing.T) {
	t.Setenv("GRAFANA_EMBED_URL", "http://localhost:3000/d/abc/atmos?kiosk")
	rr := httptest.NewRecorder()
	dashboardHandler(rr, httptest.NewRequest(http.MethodGet, "/dashboard", nil))
	if !strings.Contains(rr.Body.String(), `src="http://localhost:3000/d/abc/atmos?kiosk"`) {
		t.Errorf("Grafana iframe missing")
	}
}
