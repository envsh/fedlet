package irccloud

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type ShardRedirect struct {
	APIHost string
	Cookie  string
}

func (s *ShardRedirect) Error() string {
	return fmt.Sprintf("redirect to shard %s", s.APIHost)
}

type Config struct {
	Email    string
	Password string
}

type ServerParams struct {
	Hostname     string
	Port         int
	SSL          bool
	Netname      string
	Nickname     string
	Realname     string
	ServerPass   string
	NSPass       string
	JoinCommands string
	Channels     string
}

type Client struct {
	hc      *http.Client
	baseURL string
}

func NewClient() *Client {
	return &Client{
		hc: &http.Client{
			Transport: &http.Transport{
				DisableKeepAlives:   true,
				MaxIdleConnsPerHost: -1,
			},
		},
		baseURL: "https://www.irccloud.com",
	}
}

func (c *Client) SetBaseURL(u string) {
	c.baseURL = u
}

func (c *Client) Authenticate(cfg Config) (sessionKey string, err error) {
	t0 := time.Now()
	token, err := c.getFormToken()
	if err != nil {
		return "", fmt.Errorf("formtoken: %w", err)
	}
	log.Println("IRCCloud formtoken took", time.Since(t0))

	t1 := time.Now()
	sessionKey, err = c.doLogin(cfg.Email, cfg.Password, token)
	if err != nil {
		return "", fmt.Errorf("login: %w", err)
	}
	log.Println("IRCCloud login took", time.Since(t1))
	return sessionKey, nil
}

func (c *Client) getFormToken() (string, error) {
	req, err := http.NewRequest("POST", c.baseURL+"/chat/auth-formtoken", nil)
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

	req, err := http.NewRequest("POST", c.baseURL+"/chat/login", strings.NewReader(v.Encode()))
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

	req, err := http.NewRequest("POST", c.baseURL+path, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", "session="+sessionKey)
	return c.hc.Do(req)
}

func (c *Client) ConnectStream(sessionKey, sinceID, streamID string) (<-chan []byte, error) {
	u := c.baseURL + "/chat/stream"
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
	req.Close = true

	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode == http.StatusForbidden {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		var sh struct {
			Success bool   `json:"success"`
			Message string `json:"message"`
			APIHost string `json:"api_host"`
			Cookie  string `json:"cookie"`
		}
		if json.Unmarshal(body, &sh) == nil && sh.Message == "set_shard" && sh.APIHost != "" {
			return nil, &ShardRedirect{APIHost: sh.APIHost, Cookie: sh.Cookie}
		}
		return nil, fmt.Errorf("stream %d: %s", resp.StatusCode, string(body))
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

func (c *Client) FetchOOB(sessionKey, oobURL string) (<-chan []byte, error) {
	if strings.HasPrefix(oobURL, "/") {
		oobURL = c.baseURL + oobURL
	}

	req, err := http.NewRequest("GET", oobURL, nil)
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

func (c *Client) AddServer(sessionKey string, p ServerParams) (int, error) {
	v := url.Values{}
	v.Set("hostname", p.Hostname)
	v.Set("port", strconv.Itoa(p.Port))
	if p.SSL {
		v.Set("ssl", "1")
	}
	if p.Netname != "" {
		v.Set("netname", p.Netname)
	}
	v.Set("nickname", p.Nickname)
	if p.Realname != "" {
		v.Set("realname", p.Realname)
	}
	if p.ServerPass != "" {
		v.Set("server_pass", p.ServerPass)
	}
	if p.NSPass != "" {
		v.Set("nspass", p.NSPass)
	}
	if p.JoinCommands != "" {
		v.Set("joincommands", p.JoinCommands)
	}
	if p.Channels != "" {
		v.Set("channels", p.Channels)
	}

	resp, err := c.authenticatedPost("/chat/add-server", sessionKey, v)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	log.Printf("AddServer %s (netname=%s port=%d) HTTP %d", p.Hostname, p.Netname, p.Port, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	var sh struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
		APIHost string `json:"api_host"`
		Cookie  string `json:"cookie"`
	}
	if json.Unmarshal(body, &sh) == nil && sh.Message == "set_shard" && sh.APIHost != "" {
		return 0, &ShardRedirect{APIHost: sh.APIHost, Cookie: sh.Cookie}
	}
	var r struct {
		Success bool `json:"success"`
		CID     int  `json:"cid"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return 0, fmt.Errorf("add-server decode: %w, body=%s", err, string(body))
	}
	if !r.Success {
		return 0, fmt.Errorf("add-server failed: %s", string(body))
	}
	return r.CID, nil
}

func (c *Client) AddDefaultServer(sessionKey, nickname, realname string) (int, error) {
	v := url.Values{}
	v.Set("nickname", nickname)
	v.Set("realname", realname)

	resp, err := c.authenticatedPost("/chat/add-default-server", sessionKey, v)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var sh struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
		APIHost string `json:"api_host"`
		Cookie  string `json:"cookie"`
	}
	if json.Unmarshal(body, &sh) == nil && sh.Message == "set_shard" && sh.APIHost != "" {
		return 0, &ShardRedirect{APIHost: sh.APIHost, Cookie: sh.Cookie}
	}
	var r struct {
		Success bool `json:"success"`
		CID     int  `json:"cid"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return 0, fmt.Errorf("add-default-server decode: %w, body=%s", err, string(body))
	}
	if !r.Success {
		return 0, fmt.Errorf("add-default-server failed: %s", string(body))
	}
	return r.CID, nil
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
	u := c.baseURL + "/chat/backlog?" + q.Encode()

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
