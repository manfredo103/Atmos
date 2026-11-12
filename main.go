package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	influxdb2 "github.com/influxdata/influxdb-client-go/v2"
	"github.com/manfredo103/Atmos/greetings"
	"github.com/joho/godotenv"
)
// =======
// WEATHER 
// =======
// SensorData represents incoming environmental sensor measurements.
type SensorData struct {
	Temperature float64 `json:"temperature"`
	Humidity    float64 `json:"humidity"`
	CO2         int     `json:"co2"`
}

type BrigthskyResponse struct {
	Weather struct {
		Temperature float64 "json:'temperature'"
		RelativeHumidity float64 "json:'relative_humidity'"
	} "json:'weather'"
}

// Helper
func getEnvOrDefault(primary, fallback string) string {
	if val := os.Getenv(primary); val != "" {
		return val
	}
	return os.Getenv(fallback)
}

// greetHandler demonstrates how to use query parameters and your greetings package.
// Visit: http://localhost:8080/greet?name=Manfred
func greetHandler(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" {
		name = "World"
	}
	message := greetings.Hello(name)
	fmt.Fprintln(w, message)
}

// sensorHandler receives and parses JSON sensor readings via POST.
// Expected JSON format: {"temperature": 23.5, "humidity": 45.2, "co2": 800}
func sensorHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed: use POST", http.StatusMethodNotAllowed)
		return
	}

	var data SensorData
	decoder := json.NewDecoder(r.Body)
	// Optionally disallow unknown fields for stricter validation
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&data); err != nil {
		http.Error(w, fmt.Sprintf("Bad Request - invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	// Generate a current UTC timestamp
	timestamp := time.Now().UTC()

	// Read InfluxDB configuration from environment variables
	influxURL := os.Getenv("INFLUXDB_URL")
	if influxURL == "" {
		influxURL = os.Getenv("INFLUX_URL")
	}
	token := os.Getenv("INFLUXDB_TOKEN")
	if token == "" {
		token = os.Getenv("INFLUX_TOKEN")
	}
	org := os.Getenv("INFLUXDB_ORG")
	if org == "" {
		org = os.Getenv("INFLUX_ORG")
	}
	bucket := os.Getenv("INFLUXDB_BUCKET")
	if bucket == "" { bucket = os.Getenv("INFLUX_BUCKET") }
	// Create InfluxDB client and write point synchronously
	client := influxdb2.NewClient(influxURL, token)
	defer client.Close()

	point := influxdb2.NewPointWithMeasurement("room_atmosphere").
		AddTag("location", "inside").
		AddField("temperature", data.Temperature).
		AddField("humidity", data.Humidity).
		AddField("co2", data.CO2).
		SetTime(timestamp)

	writeAPI := client.WriteAPIBlocking(org, bucket)
	if err := writeAPI.WritePoint(r.Context(), point); err != nil {
		log.Printf("Failed to write to InfluxDB: %v", err)
		http.Error(w, fmt.Sprintf("Failed to write to InfluxDB: %v", err), http.StatusInternalServerError)
		return
	}

	// Print sensor readings beautifully to the terminal
	fmt.Println()
	fmt.Println("┌──────────────────────────────────────────┐")
	fmt.Println("│           NEW SENSOR READING             │")
	fmt.Println("├──────────────────────────────────────────┤")
	fmt.Printf("│  🌡️   Temperature : %-8.2f °C          │\n", data.Temperature)
	fmt.Printf("│  💧   Humidity    : %-8.2f %%           │\n", data.Humidity)
	fmt.Printf("│  🫧   CO2         : %-8d ppm         │\n", data.CO2)
	fmt.Println("└──────────────────────────────────────────┘")

	// Return 200 OK status with a confirmation
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status":  "success",
		"message": "Sensor data received successfully",
	})
}

func main() {
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found, using system environment variables")
	}
	influxURL := getEnvOrDefault("INFLUXDB_URL", "INFLUX_URL")
	token := getEnvOrDefault("INFLUXDB_TOKEN", "INFLUX_TOKEN")
	org := getEnvOrDefault("INFLUXDB_ORG", "INFLUX_ORG")
	bucket := getEnvOrDefault("INFLUXDB_BUCKET", "INFLUX_BUCKET")
	vbBucket := getEnvOrDefault("INFLUXDB_VOLLEYBALL_BUCKET", "INFLUXDB_BUCKET")
	if vbBucket == "" {
		vbBucket = bucket
	}

	client := influxdb2.NewClient(influxURL, token)
	defer client.Close()

	vbAPI := NewVolleyballAPI(client, org, vbBucket)

	// Create a new HTTP request multiplexer (router)
	mux := http.NewServeMux()

	// Register URL patterns to their respective handler functions
	mux.HandleFunc("/", landingHandler)
	// Optionally serve a built frontend (e.g. the SetPoint volleyball app) under /volleyball/.
	// The Vite build must then use `base: '/volleyball/'`.
	if staticDir := os.Getenv("STATIC_DIR"); staticDir != "" {
		mux.Handle("/volleyball/", http.StripPrefix("/volleyball/", http.FileServer(http.Dir(staticDir))))
	}
	mux.HandleFunc("/dashboard", dashboardHandler)
	mux.HandleFunc("/api/health", healthHandler(client))
	climateAPI := NewClimateAPI(client, org, bucket)
	mux.HandleFunc("/api/climate/current", climateAPI.handleCurrent)
	mux.HandleFunc("/api/climate/history", climateAPI.handleHistory)
	mux.HandleFunc("/greet", greetHandler)
	mux.HandleFunc("/api/sensors", sensorHandler)
	mux.HandleFunc("/api/volleyball/matches", vbAPI.handleMatches)
	mux.HandleFunc("/api/volleyball/matches/", vbAPI.handleMatchByID)

	port := ":8080"
	if envPort := os.Getenv("PORT"); envPort != "" {
		if envPort[0] == ':' {
			port = envPort
		} else {
			port = ":" + envPort
		}
	}
	fmt.Printf("Server starting at http://localhost%s (volleyball bucket: %s)\n", port, vbBucket)

	go startWeatherWorker(client, org, bucket)

	// Start listening for incoming HTTP requests
	if err := http.ListenAndServe(port, mux); err != nil {
		log.Fatalf("Server failed to start: %v", err)
	}
}
