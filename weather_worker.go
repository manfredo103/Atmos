package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"

	influxdb2 "github.com/influxdata/influxdb-client-go/v2"
)

// BrightSkyResponse maps the JSON structure returned by the Bright Sky API
type BrightSkyResponse struct {
	Weather struct {
		Temperature      float64 `json:"temperature"`
		RelativeHumidity float64 `json:"relative_humidity"`
	} `json:"weather"`
}

// startWeatherWorker runs continuously in the background
func startWeatherWorker(influxClient influxdb2.Client, org, bucket string) {
	// Fetch immediately on startup
	fetchAndStoreWeather(influxClient, org, bucket)

	// Then fetch every 15 minutes
	ticker := time.NewTicker(15 * time.Minute)
	for range ticker.C {
		fetchAndStoreWeather(influxClient, org, bucket)
	}
}

func fetchAndStoreWeather(influxClient influxdb2.Client, org, bucket string) {
	// Coordinates for Erlangen: 49.59 lat, 11.01 lon
	resp, err := http.Get("https://api.brightsky.dev/current_weather?lat=49.59&lon=11.01")
	if err != nil {
		log.Printf("Weather worker failed to fetch data: %v", err)
		return
	}
	defer resp.Body.Close()

	var data BrightSkyResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		log.Printf("Weather worker failed to parse JSON: %v", err)
		return
	}

	// Write to InfluxDB
	point := influxdb2.NewPointWithMeasurement("room_atmosphere").
		AddTag("location", "outside"). // <-- This tag separates it from your ESP32
		AddField("temperature", data.Weather.Temperature).
		AddField("humidity", data.Weather.RelativeHumidity).
		SetTime(time.Now())

	writeAPI := influxClient.WriteAPIBlocking(org, bucket)
	if err := writeAPI.WritePoint(context.Background(), point); err != nil {
		log.Printf("Weather worker failed to write to InfluxDB: %v", err)
	} else {
		log.Printf("Weather worker saved outside temperature: %.1f°C", data.Weather.Temperature)
	}
}
