package outlookgraph

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	AuthEmpty        = ""
	AuthAwaiting     = "awaiting_auth"
	AuthReady        = "authenticated"
	AuthRefreshFailed = "refresh_failed"
	AuthError        = "error"
)

const tokenURL = "https://login.microsoftonline.com/common/oauth2/v2.0/token"
const authEndpoint = "https://login.microsoftonline.com/common/oauth2/v2.0/authorize"

type tokenJSON struct {
	AccessToken  string    `json:"access_token,omitempty"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	Expiry       time.Time `json:"expiry,omitempty"`
	Status       string    `json:"status"`
	AuthURL      string    `json:"auth_url,omitempty"`
	Error        string    `json:"error,omitempty"`
}

var (
	tokMu sync.Mutex
	tok   *tokenJSON
)

func loadToken() {
	data, err := os.ReadFile(tokenFilePath())
	if err != nil {
		return
	}
	var t tokenJSON
	if err := json.Unmarshal(data, &t); err != nil {
		return
	}
	tok = &t
	statusAuthStatus.Store(tok.Status)
}

func saveToken() {
	if tok == nil {
		return
	}
	data, _ := json.MarshalIndent(tok, "", "  ")
	p := tokenFilePath()
	os.MkdirAll(filepath.Dir(p), 0700)
	os.WriteFile(p, data, 0600)
}

func generateVerifier() string {
	b := make([]byte, 43)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func generateChallenge(v string) string {
	h := sha256.Sum256([]byte(v))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

func isDesktop() bool {
	switch runtime.GOOS {
	case "darwin", "windows":
		return true
	case "linux":
		return os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != ""
	}
	return false
}

func openURL(u string) error {
	switch runtime.GOOS {
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", u).Start()
	case "darwin":
		return exec.Command("open", u).Start()
	default:
		return exec.Command("xdg-open", u).Start()
	}
}

func getToken(ctx context.Context, clientID string) (string, error) {
	tokMu.Lock()
	defer tokMu.Unlock()

	if tok == nil {
		loadToken()
	}

	if tok != nil && tok.Status == AuthReady && time.Now().Add(5*time.Minute).Before(tok.Expiry) {
		return tok.AccessToken, nil
	}

	if tok != nil && tok.RefreshToken != "" {
		if err := refreshToken(clientID); err == nil {
			return tok.AccessToken, nil
		}
		log.Printf("outlook: refresh failed, re-authenticating")
		tok.Status = AuthRefreshFailed
		saveToken()
	}

	if err := authCodeFlow(ctx, clientID); err != nil {
		return "", err
	}
	return tok.AccessToken, nil
}

func refreshToken(clientID string) error {
	v := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {tok.RefreshToken},
		"client_id":     {clientID},
		"scope":         {"https://graph.microsoft.com/Mail.Read https://graph.microsoft.com/Mail.Send https://graph.microsoft.com/User.Read offline_access"},
	}
	resp, err := http.PostForm(tokenURL, v)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		tok.Status = AuthRefreshFailed
		saveToken()
		statusAuthStatus.Store(AuthRefreshFailed)
		return fmt.Errorf("refresh: HTTP %d: %s", resp.StatusCode, string(body))
	}

	var r struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		Error        string `json:"error"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return fmt.Errorf("refresh: parse: %w", err)
	}
	if r.Error != "" {
		return fmt.Errorf("%s", r.Error)
	}
	tok.AccessToken = r.AccessToken
	if r.RefreshToken != "" {
		tok.RefreshToken = r.RefreshToken
	}
	tok.Expiry = time.Now().Add(time.Duration(r.ExpiresIn) * time.Second)
	tok.Status = AuthReady
	saveToken()
	return nil
}

func authCodeFlow(ctx context.Context, clientID string) error {
	verifier := generateVerifier()
	challenge := generateChallenge(verifier)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	port := listener.Addr().(*net.TCPAddr).Port

	codeCh := make(chan string, 1)
	srv := &http.Server{}

	go func() {
		mux := http.NewServeMux()
		mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
			code := r.URL.Query().Get("code")
			if code != "" {
				codeCh <- code
				w.Write([]byte("Authentication complete. You may close this window."))
			} else {
				w.WriteHeader(400)
				w.Write([]byte("Missing code parameter"))
			}
		})
		srv.Handler = mux
		srv.Serve(listener)
	}()
	defer srv.Close()

	redirectURI := fmt.Sprintf("http://127.0.0.1:%d/callback", port)
	authorizeURL := fmt.Sprintf("%s?client_id=%s&response_type=code&redirect_uri=%s&scope=%s&code_challenge=%s&code_challenge_method=S256",
		authEndpoint, clientID, url.QueryEscape(redirectURI),
		url.QueryEscape("https://graph.microsoft.com/Mail.Read https://graph.microsoft.com/Mail.Send https://graph.microsoft.com/User.Read offline_access"), challenge)

	tok = &tokenJSON{Status: AuthAwaiting, AuthURL: authorizeURL}
	saveToken()
	statusAuthStatus.Store(AuthAwaiting)

	var code string

	if isDesktop() {
		if err := openURL(authorizeURL); err != nil {
			log.Printf("outlook: failed to open browser: %v", err)
			log.Printf("outlook: open this URL manually:\n%s", authorizeURL)
		} else {
			log.Printf("outlook: browser opened. Waiting for authentication on %s", redirectURI)
		}
	} else {
		log.Printf("outlook: open this URL in a browser:\n\n%s\n\n", authorizeURL)
		log.Printf("After authorizing, paste the 'code' parameter from the redirect URL here:")
		go func() {
			scanner := bufio.NewScanner(os.Stdin)
			if scanner.Scan() {
				codeCh <- strings.TrimSpace(scanner.Text())
			}
		}()
	}

	select {
	case c := <-codeCh:
		code = c
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(5 * time.Minute):
		return fmt.Errorf("authentication timed out")
	}

	v := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {clientID},
		"code_verifier": {verifier},
	}
	resp, err := http.PostForm(tokenURL, v)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		tok = &tokenJSON{Status: AuthError, Error: string(body)}
		saveToken()
		statusAuthStatus.Store(AuthError)
		return fmt.Errorf("token exchange: HTTP %d: %s", resp.StatusCode, string(body))
	}

	var r struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		Error        string `json:"error"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return fmt.Errorf("token exchange: parse: %w", err)
	}
	if r.Error != "" {
		tok = &tokenJSON{Status: AuthError, Error: r.Error}
		saveToken()
		statusAuthStatus.Store(AuthError)
		return fmt.Errorf("token exchange: %s", r.Error)
	}

	tok = &tokenJSON{
		AccessToken:  r.AccessToken,
		RefreshToken: r.RefreshToken,
		Expiry:       time.Now().Add(time.Duration(r.ExpiresIn) * time.Second),
		Status:       AuthReady,
	}
	saveToken()
	statusAuthStatus.Store(AuthReady)
	log.Printf("outlook: authentication complete")
	return nil
}
