package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	influxdb2 "github.com/influxdata/influxdb-client-go/v2"
)

func TestVolleyballUploadMatch_BatchWriteAndLineProtocol(t *testing.T) {
	var writeRequests int32
	var receivedLineProtocol string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v2/write") {
			atomic.AddInt32(&writeRequests, 1)
			buf := new(bytes.Buffer)
			_, _ = buf.ReadFrom(r.Body)
			receivedLineProtocol = buf.String()
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	t.Setenv("VOLLEYBALL_API_KEY", "")

	client := influxdb2.NewClient(ts.URL, "test-token")
	defer client.Close()
	vbAPI := NewVolleyballAPI(client, "test-org", "volleyball-bucket")

	passThree := 3
	passZero := 0
	payload := VolleyballMatchPayload{
		MatchID:      "match-2026-test-01",
		StartedAt:    "2026-11-08T14:00:00Z",
		EndedAt:      "2026-11-08T15:15:00Z",
		TeamName:     "TVE Damen 6",
		OpponentName: "VC Erlangen",
		System:       "5-1",
		InitialServe: "us",
		Winner:       "us",
		EndReason:    "manual",
		Sets: []VolleyballSetScore{
			{SetNumber: 1, MyScore: 25, OpponentScore: 21},
			{SetNumber: 2, MyScore: 15, OpponentScore: 12},
		},
		Players: []VolleyballRosterPlayer{
			{ID: "s1", Position: 1, Name: "Anna", Role: "S"},
		},
		Bench: []VolleyballRosterPlayer{
			{ID: "b1", Name: "Lena", Role: "L", IsLibero: true},
		},
		Events: []VolleyballEventDTO{
			{
				ID:           "ev-1",
				Timestamp:    "14:05:00",
				ISOTimestamp: "2026-11-08T14:05:00.100Z",
				SetNumber:    1,
				ActionID:     "pass_3",
				Category:     "reception",
				Quality:      "good",
				Action:       "🎯 Perfekte Annahme (3)",
				Rotation:     1,
				Player:       "Lena (L)",
				Position:     5,
				Row:          "back",
				Team:         "myTeam",
				PassRating:   &passThree,
			},
			{
				ID:           "ev-2",
				Timestamp:    "14:05:00",
				ISOTimestamp: "2026-11-08T14:05:00.100Z", // Identical ms timestamp to verify collision protection
				SetNumber:    1,
				ActionID:     "ace",
				Category:     "serve",
				Quality:      "good",
				Action:       "🔥 Ass",
				Rotation:     1,
				Player:       "Anna (S)",
				Position:     1,
				Row:          "back",
				Team:         "myTeam",
				ScoreAfter:   "25:21",
				IsMatchEnd:   true,
			},
			{
				ID:           "ev-3",
				Timestamp:    "15:10:00",
				ISOTimestamp: "2026-11-08T15:10:00.500Z",
				SetNumber:    2,
				ActionID:     "err_rcv",
				Category:     "reception",
				Quality:      "bad",
				Action:       "💔 Annahmefehler (0)",
				Rotation:     4,
				PassRating:   &passZero,
				ScoreAfter:   "14:12",
			},
			{
				ID:           "ev-4",
				Timestamp:    "15:14:55",
				ISOTimestamp: "2026-11-08T15:14:55.000Z",
				SetNumber:    2,
				ActionID:     "kill",
				Category:     "attack",
				Quality:      "good",
				Action:       "💥 Kill Angriff",
				Rotation:     3,
				ScoreAfter:   "15:12",
				IsSideout:    true,
			},
		},
	}

	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/volleyball/matches", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	vbAPI.handleMatches(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
	}
	if got := atomic.LoadInt32(&writeRequests); got != 1 {
		t.Fatalf("expected 1 single batched InfluxDB write request, got %d", got)
	}

	lines := strings.Split(strings.TrimSpace(receivedLineProtocol), "\n")
	if len(lines) != 5 {
		t.Fatalf("expected 5 line-protocol points (1 match summary + 4 events), got %d:\n%s", len(lines), receivedLineProtocol)
	}
	if !strings.Contains(receivedLineProtocol, "volleyball_matches") {
		t.Errorf("expected volleyball_matches measurement in line protocol")
	}
	if !strings.Contains(receivedLineProtocol, "sets_us=2i") || !strings.Contains(receivedLineProtocol, "aces=1i") || !strings.Contains(receivedLineProtocol, "kills=1i") {
		t.Errorf("expected aggregated stats in volleyball_matches line protocol, got:\n%s", lines[0])
	}
	if !strings.Contains(receivedLineProtocol, "pass_avg=1.5") {
		t.Errorf("expected pass_avg=1.5 in volleyball_matches, got:\n%s", lines[0])
	}
	if !strings.Contains(receivedLineProtocol, "set_number=2") {
		t.Errorf("expected set_number=2 tag in volleyball_events")
	}
}

func TestVolleyballUploadMatch_APIKeyProtection(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()

	t.Setenv("VOLLEYBALL_API_KEY", "secret-coach-pin")

	client := influxdb2.NewClient(ts.URL, "test-token")
	defer client.Close()
	vbAPI := NewVolleyballAPI(client, "test-org", "volleyball-bucket")

	body := []byte(`{"matchId":"m-auth-1","teamName":"Us","opponentName":"Them","sets":[{"setNumber":1,"myScore":25,"opponentScore":20}],"events":[]}`)

	// 1. Missing X-API-Key -> 401
	req1 := httptest.NewRequest(http.MethodPost, "/api/volleyball/matches", bytes.NewReader(body))
	w1 := httptest.NewRecorder()
	vbAPI.handleMatches(w1, req1)
	if w1.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without API key, got %d", w1.Code)
	}

	// 2. Wrong X-API-Key -> 401
	req2 := httptest.NewRequest(http.MethodPost, "/api/volleyball/matches", bytes.NewReader(body))
	req2.Header.Set("X-API-Key", "wrong-pin")
	w2 := httptest.NewRecorder()
	vbAPI.handleMatches(w2, req2)
	if w2.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with wrong API key, got %d", w2.Code)
	}

	// 3. Correct X-API-Key -> 200
	req3 := httptest.NewRequest(http.MethodPost, "/api/volleyball/matches", bytes.NewReader(body))
	req3.Header.Set("X-API-Key", "secret-coach-pin")
	w3 := httptest.NewRecorder()
	vbAPI.handleMatches(w3, req3)
	if w3.Code != http.StatusOK {
		t.Fatalf("expected 200 with valid API key, got %d: %s", w3.Code, w3.Body.String())
	}
}

func buildAnnotatedFluxCSV(rows [][]string) string {
	var buf bytes.Buffer
	cw := csv.NewWriter(&buf)
	for _, row := range rows {
		_ = cw.Write(row)
	}
	cw.Flush()
	return buf.String() + "\n"
}

func TestVolleyballListAndGetMatchByID(t *testing.T) {
	storedJSON := `{"matchId":"match-abc","startedAt":"2026-11-08T14:00:00Z","endedAt":"2026-11-08T15:00:00Z","teamName":"TVE Damen 6","opponentName":"Gegner","system":"5-1","winner":"us","endReason":"rulebook","sets":[{"setNumber":1,"myScore":25,"opponentScore":19}],"events":[]}`

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/v2/query") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var reqBody struct {
			Query string `json:"query"`
		}
		_ = json.NewDecoder(r.Body).Decode(&reqBody)
		q := reqBody.Query

		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		if strings.Contains(q, `payload_json`) && strings.Contains(q, `match-abc`) {
			csvResp := buildAnnotatedFluxCSV([][]string{
				{"#datatype", "string", "long", "dateTime:RFC3339", "string", "string", "string"},
				{"#group", "false", "false", "false", "true", "true", "false"},
				{"#default", "_result", "", "", "", "", ""},
				{"", "result", "table", "_time", "_field", "match_id", "_value"},
				{"", "_result", "0", "2026-11-08T14:00:00Z", "payload_json", "match-abc", storedJSON},
			})
			_, _ = w.Write([]byte(csvResp))
			return
		}

		if strings.Contains(q, `_field != "payload_json"`) {
			csvResp := buildAnnotatedFluxCSV([][]string{
				{"#datatype", "string", "long", "dateTime:RFC3339", "string", "string", "string", "string", "string", "string", "long"},
				{"#group", "false", "false", "false", "true", "true", "true", "true", "true", "true", "false"},
				{"#default", "_result", "", "", "", "", "", "", "", "", ""},
				{"", "result", "table", "_time", "_field", "match_id", "team", "opponent", "winner", "system", "_value"},
				{"", "_result", "0", "2026-11-08T14:00:00Z", "sets_us", "match-abc", "TVE Damen 6", "Gegner", "us", "5-1", "2"},
				{"", "_result", "1", "2026-11-08T14:00:00Z", "sets_them", "match-abc", "TVE Damen 6", "Gegner", "us", "5-1", "1"},
				{"", "_result", "2", "2026-11-08T14:00:00Z", "aces", "match-abc", "TVE Damen 6", "Gegner", "us", "5-1", "6"},
			})
			_, _ = w.Write([]byte(csvResp))
			return
		}

		// Empty Flux result
		_, _ = w.Write([]byte("\r\n"))
	}))
	defer ts.Close()

	client := influxdb2.NewClient(ts.URL, "test-token")
	defer client.Close()
	vbAPI := NewVolleyballAPI(client, "test-org", "volleyball-bucket")

	// 1. Test GET /api/volleyball/matches (list)
	reqList := httptest.NewRequest(http.MethodGet, "/api/volleyball/matches", nil)
	wList := httptest.NewRecorder()
	vbAPI.handleMatches(wList, reqList)
	if wList.Code != http.StatusOK {
		t.Fatalf("expected 200 on list matches, got %d: %s", wList.Code, wList.Body.String())
	}
	var list []SavedMatchSummary
	if err := json.Unmarshal(wList.Body.Bytes(), &list); err != nil {
		t.Fatalf("failed to decode SavedMatchSummary list: %v", err)
	}
	if len(list) != 1 || list[0].MatchID != "match-abc" || list[0].SetsUs != 2 || list[0].SetsThem != 1 || list[0].Aces != 6 {
		t.Fatalf("unexpected summary item: %+v", list)
	}

	// 2. Test GET /api/volleyball/matches/match-abc (detail)
	reqDetail := httptest.NewRequest(http.MethodGet, "/api/volleyball/matches/match-abc", nil)
	wDetail := httptest.NewRecorder()
	vbAPI.handleMatchByID(wDetail, reqDetail)
	if wDetail.Code != http.StatusOK {
		t.Fatalf("expected 200 on match detail, got %d: %s", wDetail.Code, wDetail.Body.String())
	}
	var detail VolleyballMatchPayload
	if err := json.Unmarshal(wDetail.Body.Bytes(), &detail); err != nil {
		t.Fatalf("failed to decode match detail: %v", err)
	}
	if detail.MatchID != "match-abc" || len(detail.Sets) != 1 || detail.Sets[0].MyScore != 25 {
		t.Fatalf("unexpected detail payload: %+v", detail)
	}

	// 3. Test invalid/unsafe ID rejection (Flux injection prevention)
	reqBad := httptest.NewRequest(http.MethodGet, `/api/volleyball/matches/bad"id`, nil)
	wBad := httptest.NewRecorder()
	vbAPI.handleMatchByID(wBad, reqBad)
	if wBad.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unsafe matchId, got %d", wBad.Code)
	}

	// 4. Test missing match -> 404
	reqMissing := httptest.NewRequest(http.MethodGet, "/api/volleyball/matches/nonexistent-id", nil)
	wMissing := httptest.NewRecorder()
	vbAPI.handleMatchByID(wMissing, reqMissing)
	if wMissing.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for nonexistent matchId, got %d", wMissing.Code)
	}
}
