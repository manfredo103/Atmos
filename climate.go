package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	influxdb2 "github.com/influxdata/influxdb-client-go/v2"
)

// ClimateAPI serves sensor data (indoor ESP32 + outdoor weather) from InfluxDB
// to the dashboard. The InfluxDB token never leaves the server.
type ClimateAPI struct {
	client influxdb2.Client
	org    string
	bucket string
}

func NewClimateAPI(client influxdb2.Client, org, bucket string) *ClimateAPI {
	return &ClimateAPI{client: client, org: org, bucket: bucket}
}

// climateRange maps a user-selectable time range to a Flux start offset and
// an aggregation window, so a chart never has more than ~300 points.
type climateRange struct {
	Start string
	Every string
}

var climateRanges = map[string]climateRange{
	"1h":  {Start: "-1h", Every: "1m"},
	"6h":  {Start: "-6h", Every: "2m"},
	"24h": {Start: "-24h", Every: "10m"},
	"7d":  {Start: "-7d", Every: "1h"},
	"30d": {Start: "-30d", Every: "3h"},
}

// Whitelist of fields that may be queried (prevents Flux injection).
var climateFields = map[string]bool{
	"temperature": true,
	"humidity":    true,
	"co2":         true,
}

// ClimateReading is the latest known value set for one location.
type ClimateReading struct {
	Temperature *float64  `json:"temperature,omitempty"`
	Humidity    *float64  `json:"humidity,omitempty"`
	CO2         *float64  `json:"co2,omitempty"`
	Time        time.Time `json:"time"`
}

// ClimatePoint is a single (time, value) pair of a history series.
type ClimatePoint struct {
	T time.Time `json:"t"`
	V float64   `json:"v"`
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (c *ClimateAPI) ready(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "use GET"})
		return false
	}
	if c.client == nil || c.bucket == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "InfluxDB is not configured"})
		return false
	}
	return true
}

// handleCurrent returns the latest temperature, humidity and CO2 per location.
// GET /api/climate/current
func (c *ClimateAPI) handleCurrent(w http.ResponseWriter, r *http.Request) {
	if !c.ready(w, r) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	flux := fmt.Sprintf(`from(bucket: %q)
  |> range(start: -30d)
  |> filter(fn: (r) => r._measurement == "room_atmosphere")
  |> filter(fn: (r) => r._field == "temperature" or r._field == "humidity" or r._field == "co2")
  |> last()`, c.bucket)

	result, err := c.client.QueryAPI(c.org).Query(ctx, flux)
	if err != nil {
		log.Printf("climate current query failed: %v", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "InfluxDB query failed"})
		return
	}
	defer result.Close()

	readings := map[string]*ClimateReading{}
	for result.Next() {
		rec := result.Record()
		loc := fluxValueAsString(rec.ValueByKey("location"))
		if loc == "" {
			loc = "unknown"
		}
		rd, ok := readings[loc]
		if !ok {
			rd = &ClimateReading{}
			readings[loc] = rd
		}
		v := fluxValueAsFloat(rec.Value())
		switch rec.Field() {
		case "temperature":
			rd.Temperature = &v
		case "humidity":
			rd.Humidity = &v
		case "co2":
			rd.CO2 = &v
		}
		if rec.Time().After(rd.Time) {
			rd.Time = rec.Time()
		}
	}
	if err := result.Err(); err != nil {
		log.Printf("climate current result error: %v", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "InfluxDB query failed"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"locations": readings})
}

// handleHistory returns an aggregated time series per location.
// GET /api/climate/history?field=temperature&range=24h
func (c *ClimateAPI) handleHistory(w http.ResponseWriter, r *http.Request) {
	if !c.ready(w, r) {
		return
	}

	field := r.URL.Query().Get("field")
	if field == "" {
		field = "temperature"
	}
	if !climateFields[field] {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid field"})
		return
	}
	rangeKey := r.URL.Query().Get("range")
	if rangeKey == "" {
		rangeKey = "24h"
	}
	rg, ok := climateRanges[rangeKey]
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid range"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	flux := fmt.Sprintf(`from(bucket: %q)
  |> range(start: %s)
  |> filter(fn: (r) => r._measurement == "room_atmosphere" and r._field == %q)
  |> aggregateWindow(every: %s, fn: mean, createEmpty: false)
  |> keep(columns: ["_time", "_value", "location"])`, c.bucket, rg.Start, field, rg.Every)

	result, err := c.client.QueryAPI(c.org).Query(ctx, flux)
	if err != nil {
		log.Printf("climate history query failed: %v", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "InfluxDB query failed"})
		return
	}
	defer result.Close()

	series := map[string][]ClimatePoint{}
	for result.Next() {
		rec := result.Record()
		loc := fluxValueAsString(rec.ValueByKey("location"))
		if loc == "" {
			loc = "unknown"
		}
		series[loc] = append(series[loc], ClimatePoint{T: rec.Time(), V: fluxValueAsFloat(rec.Value())})
	}
	if err := result.Err(); err != nil {
		log.Printf("climate history result error: %v", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "InfluxDB query failed"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"field":  field,
		"range":  rangeKey,
		"series": series,
	})
}
