package post

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCommentRefreshesThenPosts(t *testing.T) {
	var tokenCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			tokenCalls++
			r.ParseForm()
			if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") != "rt" {
				t.Errorf("bad token request: %v", r.Form)
			}
			w.Write([]byte(`{"access_token":"at"}`))
		case "/commentThreads":
			if r.Header.Get("Authorization") != "Bearer at" {
				t.Errorf("missing access token")
			}
			var body map[string]map[string]any
			b, _ := io.ReadAll(r.Body)
			json.Unmarshal(b, &body)
			if body["snippet"]["videoId"] != "vid" || !strings.Contains(string(b), `"textOriginal":"Hello"`) {
				t.Errorf("bad body %s", b)
			}
			w.Write([]byte(`{"id":"c1"}`))
		}
	}))
	defer srv.Close()
	y := NewYouTube("id", "secret", "rt")
	y.TokenURL, y.APIBase, y.HTTP = srv.URL+"/token", srv.URL, srv.Client()
	for i := 0; i < 2; i++ {
		id, err := y.Comment(context.Background(), "vid", "Hello")
		if err != nil || id != "c1" {
			t.Fatalf("got %q, %v", id, err)
		}
	}
	if tokenCalls != 1 {
		t.Fatalf("the access token should be fetched once per run, got %d", tokenCalls)
	}
}

func TestCommentReportsRefusals(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			w.Write([]byte(`{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`))
		}
	}))
	defer srv.Close()
	y := NewYouTube("id", "secret", "rt")
	y.TokenURL, y.APIBase, y.HTTP = srv.URL+"/token", srv.URL, srv.Client()
	if _, err := y.Comment(context.Background(), "vid", "x"); err == nil || !strings.Contains(err.Error(), "youtube-login") {
		t.Fatalf("an expired sign-in should say how to fix it, got %v", err)
	}
}

func TestAuthURLAsksForOfflineAccess(t *testing.T) {
	u := AuthURL("cid", "http://127.0.0.1:5000/", "s")
	for _, want := range []string{"access_type=offline", "prompt=consent", "youtube.force-ssl", "client_id=cid"} {
		if !strings.Contains(u, want) {
			t.Errorf("%s missing %s", u, want)
		}
	}
}
