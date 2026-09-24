package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"runtime"
	"time"
)

// 服务/协议发现公告：仿 KDE Connect kdeconnect.identity 结构，
// 携带节点标识 + 能力 + 已开启协议列表。
type DiscoveryAdvertisement struct {
	Type     string              `json:"type"`
	PeerID   string              `json:"peer_id"`
	Name     string              `json:"name"`
	Addr     string              `json:"addr"`
	Stamp    int64               `json:"timestamp"`
	Services []DiscoveredService `json:"services"`
}

type DiscoveredService struct {
	Name       string         `json:"name"`
	Ctypes     []string       `json:"ctypes,omitempty"`
	Running    bool           `json:"running"`
	CanSend    bool           `json:"can_send"`
	CanReceive bool           `json:"can_receive"`
	Attrs      map[string]any `json:"attrs,omitempty"`
}

const discoveryType = "fedlet.service.discovery"
const discoveryTopic = "service/discovery"
const discoveryInterval = 61 * time.Second

func init() {
	http.HandleFunc("/api/service/discovery", handleServiceDiscovery)
}

// lastErrShort 取最近一条错误摘要，避免 []error 直接序列化。
func lastErrShort(errs []error) string {
	if n := len(errs); n > 0 {
		s := errs[0].Error()
		if len(s) > 120 {
			return s[:120]
		}
		return s
	}
	return ""
}

// clipboardDesktopKind 运行时检测本机是否有可用桌面剪贴板：
// 安卓/其它 → 无；linux 及 BSD → 依 WAYLAND_DISPLAY / DISPLAY 判定 wayland|x11；darwin/windows → 桌面 OS 直接可用。
func clipboardDesktopKind() (ok bool, kind string) {
	switch runtime.GOOS {
	case "android":
		return false, ""
	case "linux", "dragonfly", "freebsd", "netbsd", "openbsd":
		if os.Getenv("WAYLAND_DISPLAY") != "" {
			return true, "wayland"
		}
		if os.Getenv("DISPLAY") != "" {
			return true, "x11"
		}
		return false, ""
	case "darwin", "windows":
		return true, ""
	default:
		return false, ""
	}
}

// buildDiscovery 汇总当前节点与已开启协议。
func buildDiscovery() DiscoveryAdvertisement {
	adv := DiscoveryAdvertisement{
		Type:   discoveryType,
		Stamp:  time.Now().Unix(),
		PeerID: simSelf.PeerID,
		Name:   simSelf.Name,
		Addr:   simSelf.Address,
	}
	if adv.PeerID == "" {
		adv.PeerID = currentPeerID
	}
	for _, info := range ProtocolStatuses() {
		st := info.Status()
		s := DiscoveredService{
			Name:       info.Name,
			Ctypes:     info.Ctypes,
			Running:    st.Running,
			CanSend:    info.Capacities.CanSend,
			CanReceive: info.Capacities.CanReceive,
			Attrs: map[string]any{
				"cap_send":        info.Capacities.CanSend,
				"cap_receive":     info.Capacities.CanReceive,
				"has_sendfn":      info.SendFn != nil,
				"has_redactfn":    info.RedactFn != nil,
				"has_dlmediafn":   info.DlMediaFn != nil,
				"auth_status":     st.AuthStatus,
				"connected_since": st.ConnectedSince.Format(time.RFC3339),
				"reconn_times":    st.ReconnTimes,
			},
		}
		if msg := lastErrShort(st.LastErrs); msg != "" {
			s.Attrs["last_err"] = msg
		}
		adv.Services = append(adv.Services, s)
	}
	if ok, kind := clipboardDesktopKind(); ok {
		attrs := map[string]any{
			"cap_send":     false,
			"cap_receive":  true,
			"has_sendfn":   false,
			"has_redactfn": false,
			"formats":      []string{"text", "image"},
			"attach_types": []string{"image"},
			"note":         "local desktop clipboard mirroring",
		}
		if kind != "" {
			attrs["desktop"] = kind
		}
		adv.Services = append(adv.Services, DiscoveredService{
			Name:       "clipboard",
			Ctypes:     []string{"clipboard"},
			Running:    ok,
			CanSend:    false,
			CanReceive: true,
			Attrs:      attrs,
		})
	}
	return adv
}

// 被动拉取：GET /api/service/discovery
func handleServiceDiscovery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(buildDiscovery()); err != nil {
		log.Printf("svcdiscovery: encode: %v", err)
	}
}

// 定时发布：每 60s 经 p2pin/gossip 广播一份公告。
func poll_service_discovery() {
	for {
		time.Sleep(discoveryInterval)
		data, err := json.Marshal(buildDiscovery())
		if err != nil {
			log.Printf("svcdiscovery: marshal: %v", err)
			continue
		}
		if err := publishBytes(discoveryTopic, data); err != nil {
			log.Printf("svcdiscovery: publish: %v", err)
		}
	}
}