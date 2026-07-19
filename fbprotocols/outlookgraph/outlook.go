package outlookgraph

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/envsh/fedlet/fbprotocols/fbshared"
)

var pubfn_ func(any) error

func SetPublishInfo(pubfn func(any) error) {
	pubfn_ = pubfn
}

func publish(v any) error {
	if pubfn_ == nil {
		return fmt.Errorf("outlook: publish fn not set")
	}
	return pubfn_(v)
}

var globalClientID string

func Send(to, msg, msgType string, filedata []byte, fileinfo *fbshared.MediaDataInfo) error {
	ctx := context.Background()
	token, err := getToken(ctx, globalClientID)
	if err != nil {
		return fmt.Errorf("outlook send: auth: %w", err)
	}

	payload := buildSendMailPayload(to, msg)
	req, _ := http.NewRequestWithContext(ctx, "POST", graphAPI+"/me/messages", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("outlook send: create draft: %w", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 201 {
		return fmt.Errorf("outlook send: create draft: HTTP %d: %s", resp.StatusCode, string(body))
	}
	var draft struct{ ID string `json:"id"` }
	json.Unmarshal(body, &draft)
	log.Printf("outlook send: created draft ID=%s", draft.ID)

	req2, _ := http.NewRequestWithContext(ctx, "POST", graphAPI+"/me/messages/"+draft.ID+"/send", nil)
	req2.Header.Set("Authorization", "Bearer "+token)
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		return fmt.Errorf("outlook send: send failed (draft=%s): %w", draft.ID, err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != 202 {
		return fmt.Errorf("outlook send: send (draft=%s): HTTP %d", draft.ID, resp2.StatusCode)
	}
	log.Printf("outlook send: sent draft ID=%s", draft.ID)
	return nil
}

func buildSendMailPayload(to, msg string) []byte {
	type emailAddr struct {
		Address string `json:"address"`
	}
	type recipient struct {
		EmailAddress emailAddr `json:"emailAddress"`
	}
	type body struct {
		ContentType string `json:"contentType"`
		Content     string `json:"content"`
	}
	type message struct {
		Subject      string      `json:"subject"`
		Body         body        `json:"body"`
		ToRecipients []recipient `json:"toRecipients"`
	}
	type payload struct {
		Message message `json:"message"`
	}

	p := payload{
		Message: message{
			Subject:      "fedlet message",
			Body:         body{ContentType: "Text", Content: msg},
			ToRecipients: []recipient{{EmailAddress: emailAddr{Address: to}}},
		},
	}
	data, _ := json.Marshal(p)
	return data
}

type Config struct {
	ClientID string `json:"clientId"`
}

func tokenFilePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "fedlet", "outlook-tokens.json")
}

type folderInfo struct {
	ID        string
	Name      string
	DeltaLink string
}

type messageData struct {
	ID               string   `json:"id"`
	Subject          string   `json:"subject"`
	From             string   `json:"from"`
	ToRecipients     []string `json:"toRecipients"`
	BodyPreview      string   `json:"bodyPreview"`
	ReceivedDateTime string   `json:"receivedDateTime"`
	FolderID         string   `json:"folderId"`
	FolderName       string   `json:"folderName"`
	HasAttachments   bool     `json:"hasAttachments"`
}

const graphAPI = "https://graph.microsoft.com/v1.0"

type deltaPage struct {
	Context   string            `json:"@odata.context"`
	NextLink  string            `json:"@odata.nextLink"`
	DeltaLink string            `json:"@odata.deltaLink"`
	Value     []json.RawMessage `json:"value"`
}

type rawMsg struct {
	ID               string `json:"id"`
	Removed          *struct{} `json:"@removed,omitempty"`
	Subject          *string   `json:"subject"`
	BodyPreview      *string   `json:"bodyPreview"`
	ReceivedDateTime *string   `json:"receivedDateTime"`
	HasAttachments   *bool     `json:"hasAttachments"`
	From             *struct {
		EmailAddress *struct {
			Address *string `json:"address"`
		} `json:"emailAddress"`
	} `json:"from"`
	ToRecipients *[]struct {
		EmailAddress *struct {
			Address *string `json:"address"`
		} `json:"emailAddress"`
	} `json:"toRecipients"`
}

func enumerateFolders(ctx context.Context, token string) ([]folderInfo, error) {
	folders := []folderInfo{}
	nextLink := graphAPI + "/me/mailFolders?$select=id,displayName"
	for nextLink != "" {
		req, _ := http.NewRequestWithContext(ctx, "GET", nextLink, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("list folders: HTTP %d: %s", resp.StatusCode, string(body))
		}
		var page struct {
			Value []struct {
				ID   string `json:"id"`
				Name string `json:"displayName"`
			} `json:"value"`
			NextLink string `json:"@odata.nextLink"`
		}
		json.Unmarshal(body, &page)
		for _, f := range page.Value {
			folders = append(folders, folderInfo{ID: f.ID, Name: f.Name})
		}
		nextLink = page.NextLink
	}
	for i := 0; i < len(folders); i++ {
		children, err := getChildFolders(ctx, token, folders[i].ID)
		if err != nil {
			log.Printf("outlook: get children for %s: %v", folders[i].Name, err)
			continue
		}
		folders = append(folders, children...)
	}
	return folders, nil
}

func getChildFolders(ctx context.Context, token, parentID string) ([]folderInfo, error) {
	var children []folderInfo
	nextLink := graphAPI + "/me/mailFolders/" + parentID + "/childFolders?$select=id,displayName"
	for nextLink != "" {
		req, _ := http.NewRequestWithContext(ctx, "GET", nextLink, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("list child folders: HTTP %d: %s", resp.StatusCode, string(body))
		}
		var page struct {
			Value []struct {
				ID   string `json:"id"`
				Name string `json:"displayName"`
			} `json:"value"`
			NextLink string `json:"@odata.nextLink"`
		}
		json.Unmarshal(body, &page)
		for _, f := range page.Value {
			children = append(children, folderInfo{ID: f.ID, Name: f.Name})
		}
		nextLink = page.NextLink
	}
	for i := 0; i < len(children); i++ {
		grand, err := getChildFolders(ctx, token, children[i].ID)
		if err != nil {
			continue
		}
		children = append(children, grand...)
	}
	return children, nil
}

func initDeltaSync(ctx context.Context, token, folderID string) (string, error) {
	url := graphAPI + "/me/mailFolders/" + folderID + "/messages/delta?$select=id,subject,from,toRecipients,bodyPreview,receivedDateTime,hasAttachments"
	var deltaLink string
	for url != "" {
		req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Prefer", "odata.maxpagesize=100")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return "", err
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("init delta: HTTP %d: %s", resp.StatusCode, string(body))
		}
		var page deltaPage
		json.Unmarshal(body, &page)
		if page.DeltaLink != "" {
			deltaLink = page.DeltaLink
		}
		if page.NextLink != "" {
			url = page.NextLink
		} else {
			break
		}
	}
	if deltaLink == "" {
		return "", fmt.Errorf("no delta link returned")
	}
	return deltaLink, nil
}

func pollDelta(ctx context.Context, token, deltaLink string) ([]messageData, string, error) {
	var msgs []messageData
	url := deltaLink
	for url != "" {
		req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Prefer", "odata.maxpagesize=100")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, deltaLink, err
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, deltaLink, fmt.Errorf("poll delta: HTTP %d: %s", resp.StatusCode, string(body))
		}
		var page deltaPage
		json.Unmarshal(body, &page)
		if page.DeltaLink != "" {
			deltaLink = page.DeltaLink
		}
		for _, item := range page.Value {
			var m rawMsg
			json.Unmarshal(item, &m)
			if m.ID == "" {
				continue
			}
			if m.Removed != nil {
				continue
			}
			msg := messageData{ID: m.ID}
			if m.Subject != nil {
				msg.Subject = *m.Subject
			}
			if m.BodyPreview != nil {
				msg.BodyPreview = *m.BodyPreview
			}
			if m.ReceivedDateTime != nil {
				msg.ReceivedDateTime = *m.ReceivedDateTime
			}
			if m.HasAttachments != nil {
				msg.HasAttachments = *m.HasAttachments
			}
			if m.From != nil && m.From.EmailAddress != nil && m.From.EmailAddress.Address != nil {
				msg.From = *m.From.EmailAddress.Address
			}
			if m.ToRecipients != nil {
				for _, r := range *m.ToRecipients {
					if r.EmailAddress != nil && r.EmailAddress.Address != nil {
						msg.ToRecipients = append(msg.ToRecipients, *r.EmailAddress.Address)
					}
				}
			}
			msgs = append(msgs, msg)
		}
		if page.NextLink != "" {
			url = page.NextLink
		} else {
			break
		}
	}
	return msgs, deltaLink, nil
}

func Start(info string) {
	var cfg Config
	if err := json.Unmarshal([]byte(info), &cfg); err != nil {
		log.Println("outlook: config error:", err)
		return
	}
	if cfg.ClientID == "" {
		log.Println("outlook: --outlook-client-id is required. Create an Azure AD app at https://portal.azure.com/#view/Microsoft_AAD_RegisteredApps/ApplicationsListBlade, enable 'Allow public client flows', and grant Mail.Read delegated permission")
		return
	}
	globalClientID = cfg.ClientID
	go poll(cfg)
}

func poll(cfg Config) {
	statusRunning.Store(true)
	statusConnectedSince.Store(time.Now())
	defer statusRunning.Store(false)

	ctx := context.Background()

	token, err := getToken(ctx, cfg.ClientID)
	if err != nil {
		log.Println("outlook: auth error:", err)
		pushError(err)
		return
	}
	log.Printf("outlook: authenticated (token: %s)", tokenFilePath())

	var folders []folderInfo
	for attempt := 0; attempt < 5; attempt++ {
		folders, err = enumerateFolders(ctx, token)
		if err == nil {
			break
		}
		log.Printf("outlook: enumerate folders error: %v (attempt %d/5, retry in %ds)",
			err, attempt+1, (attempt+1)*10)
		pushError(err)
		time.Sleep(time.Duration(attempt+1) * 10 * time.Second)
		token, err = getToken(ctx, cfg.ClientID)
		if err != nil {
			log.Println("outlook: refresh token during retry:", err)
			return
		}
	}
	if err != nil {
		log.Println("outlook: enumerate folders failed after 5 attempts")
		return
	}
	log.Printf("outlook: found %d folders", len(folders))

	for i := range folders {
		dl, err := initDeltaSync(ctx, token, folders[i].ID)
		if err != nil {
			log.Printf("outlook: init delta for %s: %v", folders[i].Name, err)
			pushError(err)
			continue
		}
		folders[i].DeltaLink = dl
	}
	log.Println("outlook: initial sync complete, starting poll loop")

	for {
		time.Sleep(30 * time.Second)
		token, err := getToken(ctx, cfg.ClientID)
		if err != nil {
			log.Println("outlook: get token:", err)
			pushError(err)
			continue
		}
		for i := range folders {
			if folders[i].DeltaLink == "" {
				continue
			}
			msgs, newDL, err := pollDelta(ctx, token, folders[i].DeltaLink)
			if err != nil {
				log.Printf("outlook: poll %s: %v", folders[i].Name, err)
				pushError(err)
				if strings.Contains(err.Error(), "HTTP 401") {
					if strings.Contains(err.Error(), "JWT is not well formed") {
						log.Printf("outlook: poll %s: token malformed, clearing for re-authentication", folders[i].Name)
						tok = nil
					} else {
						log.Printf("outlook: poll %s: token expired, refreshing", folders[i].Name)
					}
					token, _ = getToken(ctx, cfg.ClientID)
				}
				continue
			}
			folders[i].DeltaLink = newDL
			for _, m := range msgs {
				m.FolderID = folders[i].ID
				m.FolderName = folders[i].Name
				b, _ := json.Marshal(m)
				if err := publish(m); err != nil {
					log.Println("outlook: publish error:", err)
				}
			um, ok := m.toUnified(b)
			if ok {
				publish(um)
			}
			}
			if len(msgs) > 0 {
				log.Printf("outlook: %s: %d new messages", folders[i].Name, len(msgs))
			}
		}
	}
}

// protocol status
var (
	statusRunning        atomic.Bool
	statusAuthStatus     atomic.Value // string
	statusConnectedSince atomic.Value // time.Time
	statusReconnTimes    atomic.Int64
	statusLastErrsMu     sync.Mutex
	statusLastErrs       [3]error
)

func pushError(err error) {
	statusLastErrsMu.Lock()
	statusLastErrs[2] = statusLastErrs[1]
	statusLastErrs[1] = statusLastErrs[0]
	statusLastErrs[0] = err
	statusLastErrsMu.Unlock()
}

func IsRunning() bool         { return statusRunning.Load() }
func AuthStatus() string {
	v := statusAuthStatus.Load()
	if v == nil { return "" }
	return v.(string)
}
func ConnectedSince() time.Time {
	v := statusConnectedSince.Load()
	if v == nil { return time.Time{} }
	return v.(time.Time)
}
func ReconnTimes() int64      { return statusReconnTimes.Load() }
func LastErrs() []error {
	statusLastErrsMu.Lock()
	defer statusLastErrsMu.Unlock()
	var out []error
	for _, e := range statusLastErrs {
		if e != nil { out = append(out, e) }
	}
	return out
}

func (m *messageData) toUnified(raw []byte) (fbshared.UnifiedMessage, bool) {
	um := fbshared.UnifiedMessage{
		Text:      m.BodyPreview,
		MsgFormat: fbshared.FmtText,
		Protocol:  fbshared.ProtoOutlookGraph,
		ChatID:    m.FolderID,
		ChatName:  m.FolderName,
		Username:  m.From,
		UserID:    m.From,
		MsgType:   fbshared.MsgTypeCreate,
		MsgID:     m.ID,
		Timestamp: time.Now().UnixNano(),
	}
	if t, err := time.Parse(time.RFC3339, m.ReceivedDateTime); err == nil {
		um.Timestamp = t.UnixNano()
	}
	um.Raw = raw
	return um, true
}
