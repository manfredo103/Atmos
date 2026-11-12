package main

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	influxdb2 "github.com/influxdata/influxdb-client-go/v2"
	"github.com/influxdata/influxdb-client-go/v2/api/write"
)

const maxVolleyballBodyBytes = 2 << 20 // 2 MiB cap to protect host RAM

var safeMatchIDRegex = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)

// VolleyballSetScore represents the final score of a single set.
type VolleyballSetScore struct {
	SetNumber     int `json:"setNumber"`
	MyScore       int `json:"myScore"`
	OpponentScore int `json:"opponentScore"`
}

// VolleyballRosterPlayer represents a starter or bench/libero player in a match.
type VolleyballRosterPlayer struct {
	ID       string `json:"id,omitempty"`
	Position int    `json:"position,omitempty"`
	Name     string `json:"name"`
	Role     string `json:"role"`
	IsLibero bool   `json:"isLibero,omitempty"`
}

// VolleyballEventDTO represents a single scouted rally action, point, or substitution.
type VolleyballEventDTO struct {
	ID           string `json:"id"`
	Timestamp    string `json:"timestamp"`              // Display time (e.g. "15:24:09")
	ISOTimestamp string `json:"isoTimestamp,omitempty"` // RFC3339 / ISO-8601 UTC timestamp
	SetNumber    int    `json:"setNumber,omitempty"`
	ActionID     string `json:"actionId,omitempty"`
	Category     string `json:"category"`
	Quality      string `json:"quality"`
	Action       string `json:"action"`
	Rotation     int    `json:"rotation"`
	Player       string `json:"player,omitempty"`
	Position     int    `json:"position,omitempty"`
	Row          string `json:"row,omitempty"`
	Team         string `json:"team,omitempty"`
	PassRating   *int   `json:"passRating,omitempty"`
	ScoreAfter   string `json:"scoreAfter,omitempty"`
	IsSideout    bool   `json:"isSideout,omitempty"`
	IsMatchEnd   bool   `json:"isMatchEnd,omitempty"`
	Note         string `json:"note,omitempty"`
}

// VolleyballMatchPayload is the complete multi-set match record uploaded by the Svelte PWA.
type VolleyballMatchPayload struct {
	MatchID      string                   `json:"matchId"`
	StartedAt    string                   `json:"startedAt"`
	EndedAt      string                   `json:"endedAt"`
	TeamName     string                   `json:"teamName"`
	OpponentName string                   `json:"opponentName"`
	System       string                   `json:"system"`
	InitialServe string                   `json:"initialServe,omitempty"`
	Winner       string                   `json:"winner"`    // "us" | "them" | "tie"
	EndReason    string                   `json:"endReason"` // "rulebook" | "manual"
	Sets         []VolleyballSetScore     `json:"sets"`
	Players      []VolleyballRosterPlayer `json:"players,omitempty"`
	Bench        []VolleyballRosterPlayer `json:"bench,omitempty"`
	Events       []VolleyballEventDTO     `json:"events"`
}

// SavedMatchSummary is the lightweight match card returned by GET /api/volleyball/matches.
type SavedMatchSummary struct {
	MatchID         string  `json:"matchId"`
	StartedAt       string  `json:"startedAt"`
	EndedAt         string  `json:"endedAt"`
	TeamName        string  `json:"teamName"`
	OpponentName    string  `json:"opponentName"`
	System          string  `json:"system"`
	Winner          string  `json:"winner"`
	EndReason       string  `json:"endReason"`
	SetsUs          int     `json:"setsUs"`
	SetsThem        int     `json:"setsThem"`
	SetsSummary     string  `json:"setsSummary"`
	TotalPointsUs   int     `json:"totalPointsUs"`
	TotalPointsThem int     `json:"totalPointsThem"`
	Aces            int     `json:"aces"`
	Kills           int     `json:"kills"`
	Blocks          int     `json:"blocks"`
	ErrorsOwn       int     `json:"errorsOwn"`
	PassAvg         float64 `json:"passAvg"`
	PassCount       int     `json:"passCount"`
	EventCount      int     `json:"eventCount"`
}

// VolleyballAPI encapsulates the InfluxDB connection settings for volleyball endpoints.
type VolleyballAPI struct {
	client influxdb2.Client
	org    string
	bucket string
}

func NewVolleyballAPI(client influxdb2.Client, org, bucket string) *VolleyballAPI {
	return &VolleyballAPI{
		client: client,
		org:    org,
		bucket: bucket,
	}
}

func setVolleyballCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-API-Key")
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status": "error",
		"error":  msg,
	})
}

func checkVolleyballAPIKey(r *http.Request) bool {
	expectedKey := strings.TrimSpace(getEnvOrDefault("VOLLEYBALL_API_KEY", "VOLLEYBALL_PIN"))
	if expectedKey == "" {
		return true
	}
	providedKey := strings.TrimSpace(r.Header.Get("X-API-Key"))
	if len(providedKey) != len(expectedKey) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(providedKey), []byte(expectedKey)) == 1
}

func (api *VolleyballAPI) resolveClient() (influxdb2.Client, string, string, func()) {
	if api != nil && api.client != nil {
		return api.client, api.org, api.bucket, func() {}
	}
	influxURL := getEnvOrDefault("INFLUXDB_URL", "INFLUX_URL")
	token := getEnvOrDefault("INFLUXDB_TOKEN", "INFLUX_TOKEN")
	org := getEnvOrDefault("INFLUXDB_ORG", "INFLUX_ORG")
	bucket := getEnvOrDefault("INFLUXDB_VOLLEYBALL_BUCKET", "INFLUXDB_BUCKET")
	if bucket == "" {
		bucket = getEnvOrDefault("INFLUX_BUCKET", "INFLUXDB_BUCKET")
	}
	c := influxdb2.NewClient(influxURL, token)
	return c, org, bucket, func() { c.Close() }
}

// handleMatches routes GET /api/volleyball/matches and POST /api/volleyball/matches.
func (api *VolleyballAPI) handleMatches(w http.ResponseWriter, r *http.Request) {
	setVolleyballCORS(w)

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	switch r.Method {
	case http.MethodPost:
		api.handleUploadMatch(w, r)
	case http.MethodGet:
		api.handleListMatches(w, r)
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "Method Not Allowed: use GET or POST")
	}
}

// handleMatchByID handles GET /api/volleyball/matches/{id}.
func (api *VolleyballAPI) handleMatchByID(w http.ResponseWriter, r *http.Request) {
	setVolleyballCORS(w)

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "Method Not Allowed: use GET")
		return
	}

	matchID := strings.TrimPrefix(r.URL.Path, "/api/volleyball/matches/")
	matchID = strings.Trim(matchID, "/")
	if !safeMatchIDRegex.MatchString(matchID) {
		writeJSONError(w, http.StatusBadRequest, "Invalid matchId format")
		return
	}

	client, org, bucket, cleanup := api.resolveClient()
	defer cleanup()

	flux := fmt.Sprintf(`from(bucket: %q)
  |> range(start: 0)
  |> filter(fn: (r) => r._measurement == "volleyball_matches" and r.match_id == %q and r._field == "payload_json")
  |> last()`, bucket, matchID)

	queryAPI := client.QueryAPI(org)
	result, err := queryAPI.Query(r.Context(), flux)
	if err != nil {
		log.Printf("Failed to query volleyball match %s: %v", matchID, err)
		writeJSONError(w, http.StatusInternalServerError, "Failed to query match from InfluxDB")
		return
	}
	defer result.Close()

	var rawPayload string
	for result.Next() {
		if s, ok := result.Record().Value().(string); ok && s != "" {
			rawPayload = s
		}
	}
	if err := result.Err(); err != nil {
		log.Printf("Error reading Flux result for match %s: %v", matchID, err)
		writeJSONError(w, http.StatusInternalServerError, "Error reading match record")
		return
	}

	if rawPayload == "" {
		writeJSONError(w, http.StatusNotFound, "Match not found")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(rawPayload))
}

func parseScorePair(scoreAfter string) (int, int, bool) {
	parts := strings.SplitN(strings.TrimSpace(scoreAfter), ":", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	us, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	them, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return us, them, true
}

func isOwnErrorAction(actionID string) bool {
	switch actionID {
	case "err_serve", "err_rcv", "err_atk", "atk_blocked", "err_set", "err_net", "err_def":
		return true
	default:
		return false
	}
}

func (api *VolleyballAPI) handleUploadMatch(w http.ResponseWriter, r *http.Request) {
	if !checkVolleyballAPIKey(r) {
		writeJSONError(w, http.StatusUnauthorized, "Invalid or missing X-API-Key")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxVolleyballBodyBytes)
	var payload VolleyballMatchPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("Invalid JSON payload: %v", err))
		return
	}

	payload.MatchID = strings.TrimSpace(payload.MatchID)
	payload.TeamName = strings.TrimSpace(payload.TeamName)
	payload.OpponentName = strings.TrimSpace(payload.OpponentName)

	if !safeMatchIDRegex.MatchString(payload.MatchID) {
		writeJSONError(w, http.StatusBadRequest, "Valid alphanumeric matchId is required")
		return
	}
	if payload.TeamName == "" {
		payload.TeamName = "Mein Team"
	}
	if payload.OpponentName == "" {
		payload.OpponentName = "Gegner"
	}
	if payload.System == "" {
		payload.System = "5-1"
	}
	if payload.EndReason == "" {
		payload.EndReason = "manual"
	}

	startedAt, err := time.Parse(time.RFC3339Nano, payload.StartedAt)
	if err != nil {
		startedAt = time.Now().UTC()
		payload.StartedAt = startedAt.Format(time.RFC3339Nano)
	} else {
		startedAt = startedAt.UTC()
	}

	endedAt, err := time.Parse(time.RFC3339Nano, payload.EndedAt)
	if err != nil {
		endedAt = time.Now().UTC()
		payload.EndedAt = endedAt.Format(time.RFC3339Nano)
	} else {
		endedAt = endedAt.UTC()
	}

	// Compute set summary & overall winner if omitted
	setsUs := 0
	setsThem := 0
	totalPointsUs := 0
	totalPointsThem := 0
	setParts := make([]string, 0, len(payload.Sets))
	for _, s := range payload.Sets {
		totalPointsUs += s.MyScore
		totalPointsThem += s.OpponentScore
		if s.MyScore > s.OpponentScore {
			setsUs++
		} else if s.OpponentScore > s.MyScore {
			setsThem++
		}
		setParts = append(setParts, fmt.Sprintf("%d:%d", s.MyScore, s.OpponentScore))
	}
	setsSummary := strings.Join(setParts, ", ")

	if payload.Winner != "us" && payload.Winner != "them" && payload.Winner != "tie" {
		if setsUs > setsThem {
			payload.Winner = "us"
		} else if setsThem > setsUs {
			payload.Winner = "them"
		} else if totalPointsUs > totalPointsThem {
			payload.Winner = "us"
		} else if totalPointsThem > totalPointsUs {
			payload.Winner = "them"
		} else {
			payload.Winner = "tie"
		}
	}

	// Aggregate KPIs across all match events
	aces := 0
	kills := 0
	blocks := 0
	errorsOwn := 0
	passSum := 0
	passCount := 0

	for _, ev := range payload.Events {
		switch ev.ActionID {
		case "ace":
			aces++
		case "kill", "tip_kill":
			kills++
		case "block":
			blocks++
		}
		if isOwnErrorAction(ev.ActionID) {
			errorsOwn++
		}
		if ev.PassRating != nil && *ev.PassRating >= 0 && *ev.PassRating <= 3 {
			passSum += *ev.PassRating
			passCount++
		}
	}

	passAvg := 0.0
	if passCount > 0 {
		passAvg = float64(passSum) / float64(passCount)
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "Failed to serialize match record")
		return
	}

	// Build batch of InfluxDB points: 1 summary point + N granular event points
	points := make([]*write.Point, 0, len(payload.Events)+1)

	matchPoint := influxdb2.NewPointWithMeasurement("volleyball_matches").
		AddTag("match_id", payload.MatchID).
		AddTag("team", payload.TeamName).
		AddTag("opponent", payload.OpponentName).
		AddTag("winner", payload.Winner).
		AddTag("system", payload.System).
		AddField("ended_at", endedAt.Format(time.RFC3339Nano)).
		AddField("end_reason", payload.EndReason).
		AddField("sets_us", setsUs).
		AddField("sets_them", setsThem).
		AddField("sets_summary", setsSummary).
		AddField("total_points_us", totalPointsUs).
		AddField("total_points_them", totalPointsThem).
		AddField("aces", aces).
		AddField("kills", kills).
		AddField("blocks", blocks).
		AddField("errors_own", errorsOwn).
		AddField("pass_avg", passAvg).
		AddField("pass_count", passCount).
		AddField("event_count", len(payload.Events)).
		AddField("payload_json", string(payloadBytes)).
		SetTime(startedAt)

	points = append(points, matchPoint)

	for idx, ev := range payload.Events {
		evTime := startedAt.Add(time.Duration(idx+1) * time.Millisecond)
		if ev.ISOTimestamp != "" {
			if parsed, pErr := time.Parse(time.RFC3339Nano, ev.ISOTimestamp); pErr == nil {
				// Deterministic +idx microsecond tiebreaker prevents duplicate-timestamp overwrites
				evTime = parsed.UTC().Add(time.Duration(idx) * time.Microsecond)
			}
		}

		setNum := ev.SetNumber
		if setNum <= 0 {
			setNum = 1
		}
		category := ev.Category
		if category == "" {
			category = "note"
		}
		quality := ev.Quality
		if quality == "" {
			quality = "neutral"
		}
		actionID := ev.ActionID
		if actionID == "" {
			actionID = "custom"
		}

		ep := influxdb2.NewPointWithMeasurement("volleyball_events").
			AddTag("match_id", payload.MatchID).
			AddTag("team", payload.TeamName).
			AddTag("opponent", payload.OpponentName).
			AddTag("set_number", strconv.Itoa(setNum)).
			AddTag("category", category).
			AddTag("quality", quality).
			AddTag("action_id", actionID).
			AddField("event_index", idx).
			AddField("event_id", ev.ID).
			AddField("rotation", ev.Rotation).
			AddField("action_label", ev.Action).
			AddField("is_sideout", ev.IsSideout).
			AddField("is_match_end", ev.IsMatchEnd).
			SetTime(evTime)

		if ev.Player != "" {
			ep.AddField("player", ev.Player)
		}
		if ev.Position > 0 {
			ep.AddField("position", ev.Position)
		}
		if ev.Row != "" {
			ep.AddField("row", ev.Row)
		}
		if ev.Team != "" {
			ep.AddField("target_team", ev.Team)
		}
		if ev.PassRating != nil {
			ep.AddField("pass_rating", *ev.PassRating)
		}
		if usScore, themScore, ok := parseScorePair(ev.ScoreAfter); ok {
			ep.AddField("my_score", usScore)
			ep.AddField("opp_score", themScore)
		}

		points = append(points, ep)
	}

	client, org, bucket, cleanup := api.resolveClient()
	defer cleanup()

	writeAPI := client.WriteAPIBlocking(org, bucket)
	if err := writeAPI.WritePoint(r.Context(), points...); err != nil {
		log.Printf("Failed to write volleyball match %s to InfluxDB: %v", payload.MatchID, err)
		writeJSONError(w, http.StatusInternalServerError, "Failed to write match to InfluxDB")
		return
	}

	log.Printf("Saved volleyball match %s (%s vs %s, sets %d:%d, %d events)",
		payload.MatchID, payload.TeamName, payload.OpponentName, setsUs, setsThem, len(payload.Events))

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"status":       "success",
		"matchId":      payload.MatchID,
		"syncedEvents": len(payload.Events),
		"setsSummary":  setsSummary,
	})
}

func fluxValueAsInt(v interface{}) int {
	switch val := v.(type) {
	case int64:
		return int(val)
	case uint64:
		return int(val)
	case int:
		return val
	case float64:
		return int(val)
	case string:
		n, _ := strconv.Atoi(val)
		return n
	default:
		return 0
	}
}

func fluxValueAsFloat(v interface{}) float64 {
	switch val := v.(type) {
	case float64:
		return val
	case int64:
		return float64(val)
	case uint64:
		return float64(val)
	case int:
		return float64(val)
	case string:
		f, _ := strconv.ParseFloat(val, 64)
		return f
	default:
		return 0
	}
}

func fluxValueAsString(v interface{}) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

func (api *VolleyballAPI) handleListMatches(w http.ResponseWriter, r *http.Request) {
	client, org, bucket, cleanup := api.resolveClient()
	defer cleanup()

	// Avoid heavy Flux pivot() inside InfluxDB to keep host RAM minimal; group scalar fields in Go.
	flux := fmt.Sprintf(`from(bucket: %q)
  |> range(start: 0)
  |> filter(fn: (r) => r._measurement == "volleyball_matches" and r._field != "payload_json")`, bucket)

	queryAPI := client.QueryAPI(org)
	result, err := queryAPI.Query(r.Context(), flux)
	if err != nil {
		log.Printf("Failed to list volleyball matches from InfluxDB: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "Failed to query saved matches")
		return
	}
	defer result.Close()

	byID := make(map[string]*SavedMatchSummary)
	for result.Next() {
		rec := result.Record()
		mID := fluxValueAsString(rec.ValueByKey("match_id"))
		if mID == "" {
			continue
		}

		item, exists := byID[mID]
		if !exists {
			item = &SavedMatchSummary{
				MatchID:      mID,
				StartedAt:    rec.Time().UTC().Format(time.RFC3339),
				TeamName:     fluxValueAsString(rec.ValueByKey("team")),
				OpponentName: fluxValueAsString(rec.ValueByKey("opponent")),
				Winner:       fluxValueAsString(rec.ValueByKey("winner")),
				System:       fluxValueAsString(rec.ValueByKey("system")),
			}
			byID[mID] = item
		}

		switch rec.Field() {
		case "ended_at":
			item.EndedAt = fluxValueAsString(rec.Value())
		case "end_reason":
			item.EndReason = fluxValueAsString(rec.Value())
		case "sets_us":
			item.SetsUs = fluxValueAsInt(rec.Value())
		case "sets_them":
			item.SetsThem = fluxValueAsInt(rec.Value())
		case "sets_summary":
			item.SetsSummary = fluxValueAsString(rec.Value())
		case "total_points_us":
			item.TotalPointsUs = fluxValueAsInt(rec.Value())
		case "total_points_them":
			item.TotalPointsThem = fluxValueAsInt(rec.Value())
		case "aces":
			item.Aces = fluxValueAsInt(rec.Value())
		case "kills":
			item.Kills = fluxValueAsInt(rec.Value())
		case "blocks":
			item.Blocks = fluxValueAsInt(rec.Value())
		case "errors_own":
			item.ErrorsOwn = fluxValueAsInt(rec.Value())
		case "pass_avg":
			item.PassAvg = fluxValueAsFloat(rec.Value())
		case "pass_count":
			item.PassCount = fluxValueAsInt(rec.Value())
		case "event_count":
			item.EventCount = fluxValueAsInt(rec.Value())
		}
	}

	if err := result.Err(); err != nil {
		log.Printf("Error iterating volleyball match list: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "Error reading saved matches")
		return
	}

	summaries := make([]SavedMatchSummary, 0, len(byID))
	for _, s := range byID {
		summaries = append(summaries, *s)
	}

	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].StartedAt > summaries[j].StartedAt
	})

	if len(summaries) > 100 {
		summaries = summaries[:100]
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(summaries)
}
