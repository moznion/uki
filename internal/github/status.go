// Package github sets commit statuses via the REST API.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	Token  string
	Repo   string // owner/name
	APIURL string // defaults to https://api.github.com
	HTTP   *http.Client
}

type Status struct {
	State       string `json:"state"`
	Context     string `json:"context"`
	Description string `json:"description,omitempty"`
	TargetURL   string `json:"target_url,omitempty"`
}

// ErrUnknownCommit is returned when GitHub does not have the commit (HTTP 422).
var ErrUnknownCommit = errors.New("commit not found on GitHub")

func (c Client) SetStatus(ctx context.Context, sha string, s Status) error {
	api := c.APIURL
	if api == "" {
		api = "https://api.github.com"
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	body, err := json.Marshal(s)
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%s/repos/%s/statuses/%s", strings.TrimRight(api, "/"), c.Repo, sha)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Content-Type", "application/json")

	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusUnprocessableEntity {
		return ErrUnknownCommit
	}
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("set status: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	return nil
}
