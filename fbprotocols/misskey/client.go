package misskey

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strings"
	"time"
)

var hc = &http.Client{Timeout: 67 * time.Second}

// misskeyFileName 规范上传文件名到 Misskey 合法名：
// 剥路径、去空白；命中官方非法规则(空 / \ / .. / 长度>200 / "blob")则回退 "untitled"。
func misskeyFileName(name string) string {
	name = filepath.Base(strings.TrimSpace(name))
	if name == "" || name == "." || strings.Contains(name, "\\") ||
		strings.Contains(name, "/") || strings.Contains(name, "..") ||
		len(name) > 200 || name == "blob" {
		return "untitled"
	}
	return name
}

// uploadDriveFile multipart 上传到 Drive 并返回 fileId。
// 分块写入并打印上传进度(约 256KB 一块)。
func uploadDriveFile(host, token string, data []byte, filename string) (string, error) {
	filename = misskeyFileName(filename)
	url := strings.TrimRight(host, "/") + "/api/drive/files/create"
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)

	go func() {
		defer pw.Close()
		defer mw.Close()
		mw.WriteField("i", token)
		mw.WriteField("force", "true")
		mw.WriteField("isSensitive", "false")
		if filename != "" {
			mw.WriteField("name", filename)
		}
		fw, err := mw.CreateFormFile("file", filename)
		if err != nil {
			pw.CloseWithError(err)
			return
		}
		total := len(data)
		sent := 0
		const chunk = 256 << 10
		for sent < total {
			s := chunk
			if left := total - sent; left < s {
				s = left
			}
			if _, err := fw.Write(data[sent : sent+s]); err != nil {
				pw.CloseWithError(err)
				return
			}
			sent += s
			log.Printf("misskey: upload progress %d/%d bytes (%s)", sent, total, filename)
		}
	}()

	req, err := http.NewRequest(http.MethodPost, url, pr)
	if err != nil {
		return "", fmt.Errorf("misskey: upload drive file req: %w", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("misskey: upload drive file: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("misskey: upload drive file HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var df DriveFile
	if err := json.Unmarshal(raw, &df); err != nil {
		return "", fmt.Errorf("misskey: upload drive file decode: %w: %s", err, string(raw))
	}
	if df.ID == "" {
		return "", fmt.Errorf("misskey: upload drive file empty id: %s", string(raw))
	}
	log.Printf("misskey: uploaded %s -> file_id=%s", filename, df.ID)
	return df.ID, nil
}

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

func SendNote(host, token, text, visibility string, fileIds []string) (string, error) {
	var resp noteCreateResp
	err := apiPost(host, "notes/create", noteCreateReq{
		I:          token,
		Text:       text,
		Visibility: visibility,
		FileIds:    fileIds,
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
	if err := apiPost(host, ep, timelineReq{
		I:       token,
		Limit:   20,
		SinceID: sinceID,
	}, &notes); err != nil {
		return nil, err
	}
	for i := range notes {
		notes[i].AccountID = accountId
		notes[i].AccountName = accountName
	}
	return notes, nil
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
