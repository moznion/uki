package github

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSetStatusRequest(t *testing.T) {
	var got struct {
		method, path, auth, accept, version string
		body                                Status
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.method, got.path = r.Method, r.URL.Path
		got.auth, got.accept, got.version = r.Header.Get("Authorization"), r.Header.Get("Accept"), r.Header.Get("X-GitHub-Api-Version")
		if err := json.NewDecoder(r.Body).Decode(&got.body); err != nil {
			t.Errorf("decode: %v", err)
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	c := Client{Token: "tkn", Repo: "o/r", APIURL: srv.URL + "/"}
	want := Status{State: "success", Context: "uki", Description: "Passed: test", TargetURL: "https://example.com/run"}
	if err := c.SetStatus(context.Background(), "abc", want); err != nil {
		t.Fatal(err)
	}
	if got.method != http.MethodPost || got.path != "/repos/o/r/statuses/abc" {
		t.Errorf("request = %s %s", got.method, got.path)
	}
	if got.auth != "Bearer tkn" || got.accept != "application/vnd.github+json" || got.version == "" {
		t.Errorf("headers: auth=%q accept=%q version=%q", got.auth, got.accept, got.version)
	}
	if got.body != want {
		t.Errorf("body = %+v", got.body)
	}
}

func TestSetStatusErrors(t *testing.T) {
	cases := []struct {
		code    int
		wantErr func(error) bool
	}{
		{http.StatusUnprocessableEntity, func(err error) bool { return errors.Is(err, ErrUnknownCommit) }},
		{http.StatusInternalServerError, func(err error) bool {
			return err != nil && !errors.Is(err, ErrUnknownCommit) && strings.Contains(err.Error(), "boom")
		}},
	}
	for _, c := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(c.code)
			_, _ = w.Write([]byte("boom"))
		}))
		err := Client{Token: "t", Repo: "o/r", APIURL: srv.URL}.SetStatus(context.Background(), "abc", Status{State: "success"})
		srv.Close()
		if !c.wantErr(err) {
			t.Errorf("HTTP %d: unexpected error %v", c.code, err)
		}
	}
}
