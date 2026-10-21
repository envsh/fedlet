package main

import (
	_ "embed"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed stfiledir.html
var stfiledirHTML string

func init() {
	http.HandleFunc("/stfile/", handleStFile)
}

// stfEntry 目录列表中的一行。Size/Type/ModTime 均为已格式化的展示字符串。
type stfEntry struct {
	Name    string
	Href    string
	IsDir   bool
	Type    string
	Size    string
	ModTime string
}

type stfCrumb struct {
	Name string
	Href string
}

type stfPage struct {
	Title   string
	Crumbs  []stfCrumb
	Parent  string
	Entries []stfEntry
}

func handleStFile(w http.ResponseWriter, r *http.Request) {
	if syncDir == "" {
		http.NotFound(w, r)
		return
	}
	rel := strings.TrimPrefix(r.URL.Path, "/stfile/")
	rel = path.Clean("/" + rel)[1:]
	root := filepath.Clean(syncDir)
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if abs != root && !strings.HasPrefix(abs, root+string(filepath.Separator)) {
		http.NotFound(w, r)
		return
	}
	fi, err := os.Stat(abs)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if !fi.IsDir() {
		http.ServeFile(w, r, abs)
		return
	}
	renderStDir(w, rel, abs)
}

func renderStDir(w http.ResponseWriter, rel, absDir string) {
	items, err := os.ReadDir(absDir)
	if err != nil {
		log.Printf("stfile: read %s: %v", absDir, err)
		http.Error(w, "read directory failed", http.StatusInternalServerError)
		return
	}
	page := stfPage{Title: "/stfile/" + rel}
	for _, it := range items {
		info, err := it.Info()
		if err != nil {
			continue
		}
		name := it.Name()
		href := path.Join("/stfile", rel, name)
		ent := stfEntry{Name: name, Href: href, IsDir: it.IsDir(), ModTime: info.ModTime().Format("2006-01-02 15:04")}
		if it.IsDir() {
			ent.Type = "dir"
		} else {
			ent.Type = fileTypeLabel(name, filepath.Join(absDir, name))
			ent.Size = formatSize(info.Size())
		}
		page.Entries = append(page.Entries, ent)
	}
	sort.SliceStable(page.Entries, func(i, j int) bool {
		a, b := page.Entries[i], page.Entries[j]
		if a.IsDir != b.IsDir {
			return a.IsDir
		}
		return a.Name < b.Name
	})
	page.Crumbs = stfCrumbs(rel)
	if rel != "" {
		page.Parent = path.Join("/stfile", path.Dir(rel))
		if page.Parent == "/stfile" {
			page.Parent = "/stfile/"
		}
	}
	tmplBody := stfiledirHTML
	for _, dir := range assetDirs() {
		p := filepath.Join(dir, "stfiledir.html")
		if b, err := os.ReadFile(p); err == nil {
			log.Printf("stfile: use template %s (file)", p)
			tmplBody = string(b)
			break
		}
	}
	tmpl, err := template.New("stfiledir").Parse(tmplBody)
	if err != nil {
		log.Printf("stfile: parse template: %v", err)
		http.Error(w, "template error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.Execute(w, page); err != nil {
		log.Printf("stfile: execute template: %v", err)
	}
}

// stfCrumbs 由相对路径生成面包屑,末项不带链接(当前目录)。
func stfCrumbs(rel string) []stfCrumb {
	crumbs := []stfCrumb{{Name: "root", Href: "/stfile/"}}
	if rel == "" {
		return crumbs
	}
	parts := strings.Split(rel, "/")
	acc := ""
	for i, p := range parts {
		acc = path.Join(acc, p)
		href := "/stfile/" + acc + "/"
		if i == len(parts)-1 {
			crumbs = append(crumbs, stfCrumb{Name: p})
		} else {
			crumbs = append(crumbs, stfCrumb{Name: p, Href: href})
		}
	}
	return crumbs
}

// fileTypeLabel 返回简短类型:优先扩展名,无扩展名再退回 fileMIME 的主类型。
func fileTypeLabel(name, abs string) string {
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(name)), ".")
	if ext != "" {
		return ext
	}
	m := fileMIME(abs)
	if i := strings.IndexByte(m, '/'); i >= 0 {
		return m[:i]
	}
	return m
}

// formatSize 将字节数格式化为人类可读字符串。
func formatSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
