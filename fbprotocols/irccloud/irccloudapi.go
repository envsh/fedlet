package irccloud

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const baseURL = "https://www.irccloud.com"

type Config struct {
	Email    string
	Password string
}

type Client struct {
	hc *http.Client
}

func NewClient() *Client {
	return &Client{hc: &http.Client{}}
}

func (c *Client) Authenticate(cfg Config) (sessionKey string, err error) {
	token, err := c.getFormToken()
	if err != nil {
		return "", fmt.Errorf("formtoken: %w", err)
	}
	sessionKey, err = c.doLogin(cfg.Email, cfg.Password, token)
	if err != nil {
		return "", fmt.Errorf("login: %w", err)
	}
	return sessionKey, nil
}

func (c *Client) getFormToken() (string, error) {
	req, err := http.NewRequest("POST", baseURL+"/chat/auth-formtoken", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Length", "0")
	resp, err := c.hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var r struct {
		Success bool   `json:"success"`
		Token   string `json:"token"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return "", fmt.Errorf("decode: %w, body=%s", err, string(body))
	}
	if !r.Success {
		return "", fmt.Errorf("failed: %s", string(body))
	}
	return r.Token, nil
}

func (c *Client) doLogin(email, password, token string) (string, error) {
	v := url.Values{}
	v.Set("token", token)
	v.Set("email", email)
	v.Set("password", password)

	req, err := http.NewRequest("POST", baseURL+"/chat/login", strings.NewReader(v.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("x-auth-formtoken", token)

	resp, err := c.hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var r struct {
		Success bool   `json:"success"`
		Session string `json:"session"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return "", fmt.Errorf("decode: %w, body=%s", err, string(body))
	}
	if !r.Success {
		return "", fmt.Errorf("failed: %s", string(body))
	}
	return r.Session, nil
}

func (c *Client) authenticatedPost(path, sessionKey string, form url.Values) (*http.Response, error) {
	if form == nil {
		form = url.Values{}
	}
	form.Set("session", sessionKey)

	req, err := http.NewRequest("POST", baseURL+path, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", "session="+sessionKey)
	return c.hc.Do(req)
}

func (c *Client) ConnectStream(sessionKey, sinceID, streamID string) (<-chan []byte, error) {
	u := baseURL + "/chat/stream"
	if streamID != "" {
		q := url.Values{}
		q.Set("since_id", sinceID)
		q.Set("stream_id", streamID)
		u += "?" + q.Encode()
	}

	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Cookie", "session="+sessionKey)

	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("stream %d: %s", resp.StatusCode, string(body))
	}

	ch := make(chan []byte, 256)
	go func() {
		defer resp.Body.Close()
		defer close(ch)
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			ch <- []byte(line)
		}
	}()
	return ch, nil
}

func (c *Client) FetchOOB(oobURL string) (<-chan []byte, error) {
	resp, err := c.hc.Get(oobURL)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("OOB %d: %s", resp.StatusCode, string(body))
	}

	ch := make(chan []byte, 256)
	go func() {
		defer resp.Body.Close()
		defer close(ch)
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			ch <- []byte(line)
		}
	}()
	return ch, nil
}

func (c *Client) Join(sessionKey string, cid int, channel, key string) error {
	v := url.Values{}
	v.Set("cid", strconv.Itoa(cid))
	v.Set("channel", channel)
	if key != "" {
		v.Set("key", key)
	}

	resp, err := c.authenticatedPost("/chat/join", sessionKey, v)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("join %s status %d: %s", channel, resp.StatusCode, string(body))
	}
	return nil
}

func (c *Client) Say(sessionKey string, cid int, to, msg string) error {
	v := url.Values{}
	v.Set("cid", strconv.Itoa(cid))
	v.Set("to", to)
	v.Set("msg", msg)

	resp, err := c.authenticatedPost("/chat/say", sessionKey, v)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("say status %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

func (c *Client) Nick(sessionKey string, cid int, nick string) error {
	v := url.Values{}
	v.Set("cid", strconv.Itoa(cid))
	v.Set("nick", nick)

	resp, err := c.authenticatedPost("/chat/nick", sessionKey, v)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("nick status %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

func (c *Client) Whois(sessionKey string, cid int, nick, server string) error {
	v := url.Values{}
	v.Set("cid", strconv.Itoa(cid))
	v.Set("nick", nick)
	if server != "" {
		v.Set("server", server)
	}

	resp, err := c.authenticatedPost("/chat/whois", sessionKey, v)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("whois status %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

func (c *Client) Heartbeat(sessionKey string, selectedBid int, seenEidsJSON string) error {
	v := url.Values{}
	v.Set("selectedBuffer", strconv.Itoa(selectedBid))
	v.Set("seenEids", seenEidsJSON)

	resp, err := c.authenticatedPost("/chat/heartbeat", sessionKey, v)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("heartbeat status %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

func (c *Client) Backlog(sessionKey string, cid, bid, num, beforeID int) (<-chan []byte, error) {
	q := url.Values{}
	q.Set("cid", strconv.Itoa(cid))
	q.Set("bid", strconv.Itoa(bid))
	if num > 0 {
		q.Set("num", strconv.Itoa(num))
	}
	if beforeID > 0 {
		q.Set("beforeid", strconv.Itoa(beforeID))
	}
	u := baseURL + "/chat/backlog?" + q.Encode()

	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Cookie", "session="+sessionKey)

	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("backlog %d: %s", resp.StatusCode, string(body))
	}

	ch := make(chan []byte, 256)
	go func() {
		defer resp.Body.Close()
		defer close(ch)
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			ch <- []byte(line)
		}
	}()
	return ch, nil
}
