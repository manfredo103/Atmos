#!/bin/bash
# simulate_esp32.sh - Sends random sensor data to the Atmos Go API every 2 seconds

URL="http://localhost:8080/api/sensors"

echo "📡 Starting ESP32 Simulator... (Press Ctrl+C to stop)"
echo "Targeting: $URL"
echo "------------------------------------------------------"

while true; do
    # Generate realistic random values
    # Temperature: 20.0 to 25.0 °C
    TEMP=$(awk -v min=20 -v max=25 'BEGIN{srand(); printf "%.1f", min+rand()*(max-min)}')
    
    # Humidity: 40.0 to 60.0 %
    HUM=$(awk -v min=40 -v max=60 'BEGIN{srand(); printf "%.1f", min+rand()*(max-min)}')
    
    # CO2: 400 to 1000 ppm
    CO2=$((400 + RANDOM % 600))
    
    # Format as JSON
    PAYLOAD="{\"temperature\": $TEMP, \"humidity\": $HUM, \"co2\": $CO2}"
    
    # Send the request silently (-s) but capture the HTTP status code (-w)
    STATUS=$(curl -s -o /dev/null -w "%{http_code}" -X POST $URL -H "Content-Type: application/json" -d "$PAYLOAD")
    
    if [ "$STATUS" -eq 200 ] || [ "$STATUS" -eq 204 ]; then
        echo "✅ Sent: $PAYLOAD  | Status: $STATUS"
    else
        echo "❌ Failed to send data. HTTP Status: $STATUS"
    fi
    
    # Wait 30 seconds before the next reading (adjust as needed)
    sleep 30
done
