package fedpubhttp

import (
	"fmt"
	"bytes"
	"net/http"
)

func Publish(channel string, data []byte) error {
	url := fmt.Sprintf("http://127.0.0.1:4004/p2pin/send?topic=%s", channel)
	resp, err := http.Post(url, "application/octet-stream", bytes.NewReader(data))
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}
