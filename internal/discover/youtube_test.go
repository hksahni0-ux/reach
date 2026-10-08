package discover

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParseVideos(t *testing.T) {
	raw := json.RawMessage(`{"items":[{"id":"abc","snippet":{"publishedAt":"2026-10-07T10:00:00Z","channelTitle":"Chan","channelId":"UC1","title":"AI agents explained","description":"How they work"},
		"statistics":{"viewCount":"250000","likeCount":"12000","commentCount":"340"}}]}`)
	posts, err := ParseVideos(raw, "AI agents")
	if err != nil || len(posts) != 1 {
		t.Fatalf("got %v, %v", posts, err)
	}
	p := posts[0]
	if p.Platform != "youtube" || p.URL != "https://www.youtube.com/watch?v=abc" || p.Views != 250000 || p.Likes != 12000 || p.Comments != 340 || p.AuthorName != "Chan" || !strings.HasPrefix(p.Text, "AI agents explained") {
		t.Fatalf("unexpected %+v", p)
	}
}

func TestVideosSearchesThenFetchesStats(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("key") != "k" {
			t.Errorf("missing key on %s", r.URL.Path)
		}
		switch r.URL.Path {
		case "/search":
			if r.URL.Query().Get("order") != "viewCount" || r.URL.Query().Get("publishedAfter") == "" {
				t.Errorf("search should sort by views since a date: %s", r.URL.RawQuery)
			}
			w.Write([]byte(`{"items":[{"id":{"videoId":"v1"}},{"id":{"videoId":"v1"}}]}`))
		case "/videos":
			w.Write([]byte(`{"items":[{"id":"v1","snippet":{"publishedAt":"2026-10-07T10:00:00Z","title":"T","defaultAudioLanguage":"en"},"statistics":{"viewCount":"9"}}]}`))
		}
	}))
	defer srv.Close()
	y := &YouTube{Key: "k", Base: srv.URL, HTTP: srv.Client()}
	posts, errs := y.Videos(context.Background(), []Topic{{Name: "t", Queries: []string{"q1", "q2"}}}, time.Now().Add(-24*time.Hour), 10)
	if len(errs) != 0 || len(posts) != 1 || posts[0].Views != 9 {
		t.Fatalf("got %+v, %v", posts, errs)
	}
}

func TestYouTubeErrorsHideTheKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		w.Write([]byte(`{"error":"bad key secret-k"}`))
	}))
	defer srv.Close()
	y := &YouTube{Key: "secret-k", Base: srv.URL, HTTP: srv.Client()}
	_, errs := y.Videos(context.Background(), []Topic{{Name: "t", Queries: []string{"q"}}}, time.Now(), 5)
	if len(errs) != 1 || strings.Contains(errs[0].Error(), "secret-k") {
		t.Fatalf("errors should hide the key: %v", errs)
	}
}

func TestParseVideosKeepsEnglishOnly(t *testing.T) {
	raw := json.RawMessage(`{"items":[
		{"id":"en1","snippet":{"title":"What is the Lethal Trifecta in AI Agents?","description":"How data can leak"}},
		{"id":"en2","snippet":{"title":"Kurzes Video","defaultAudioLanguage":"en-GB"}},
		{"id":"es","snippet":{"title":"ROILAN ME TRAJO RELOJES, ORO Y HASTA SUS GAFAS… ¿NECESITA EFECTIVO?","description":"Compramos oro y relojes"}},
		{"id":"pl","snippet":{"title":"Ulepszyłem STANOWISKO GAMINGOWE Drukarką 3D... Dziś testuje","description":"Drukarka 3D w akcji"}},
		{"id":"hi","snippet":{"title":"AI एजेंट क्या है? पूरी जानकारी"}},
		{"id":"hiaudio","snippet":{"title":"What is this Muse tool of Instagram? Is it agentic AI?","defaultAudioLanguage":"hi"}},
		{"id":"te","snippet":{"title":"ఇంజనీరింగ్ అభ్యర్థులకు బిగ్ అలర్ట్! | MECON Limited Project Contract Engineer Recruitment 2026","defaultLanguage":"en"}}]}`)
	posts, err := ParseVideos(raw, "AI agents")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, p := range posts {
		got = append(got, strings.TrimPrefix(p.URL, "https://www.youtube.com/watch?v="))
	}
	if strings.Join(got, ",") != "en1,en2" {
		t.Fatalf("kept %v, want only the English videos en1,en2", got)
	}
}
