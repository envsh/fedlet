package misskey

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

var hc = &http.Client{Timeout: 30 * time.Second}

func apiPost(host, endpoint string, body, out any) error {
	url := strings.TrimRight(host, "/") + "/api/" + endpoint
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(body); err != nil {
		return fmt.Errorf("misskey: encode %s: %w", endpoint, err)
	}
	resp, err := hc.Post(url, "application/json", &buf)
	if err != nil {
		return fmt.Errorf("misskey: %s: %w", endpoint, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("misskey: %s: HTTP %d: %s", endpoint, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("misskey: decode %s: %w", endpoint, err)
		}
	}
	return nil
}

type noteCreateResp struct {
	ID string `json:"id"`
}

func SendNote(host, token, text, visibility string) (string, error) {
	var resp noteCreateResp
	err := apiPost(host, "notes/create", noteCreateReq{
		I:          token,
		Text:       text,
		Visibility: visibility,
	}, &resp)
	return resp.ID, err
}

func timelineEndpoint(timeline string) string {
	if timeline == "home" || timeline == "" {
		return "notes/timeline"
	}
	return "notes/" + timeline + "-timeline"
}

func FetchTimeline(host, token, timeline, sinceID string) ([]Note, error) {
	ep := timelineEndpoint(timeline)
	var notes []Note
	err := apiPost(host, ep, timelineReq{
		I:       token,
		Limit:   20,
		SinceID: sinceID,
	}, &notes)
	return notes, err
}

func VerifyToken(host, token string) (*iResp, error) {
	var resp iResp
	err := apiPost(host, "i", iReq{I: token}, &resp)
	if err != nil {
		return nil, err
	}
	return &resp, nil
}

func FetchMeta(host string) (*metaResp, error) {
	var resp metaResp
	err := apiPost(host, "meta", struct{}{}, &resp)
	if err != nil {
		return nil, err
	}
	return &resp, nil
}
