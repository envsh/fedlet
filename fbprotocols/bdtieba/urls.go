package bdtieba

import (
	"fmt"
	"net/url"
)

// ThreadURLs returns the new-layout, legacy WAP and touch thread URLs.
func ThreadURLs(tid int64) (newURL, oldURL, touchURL string) {
	newURL = fmt.Sprintf("https://tieba.baidu.com/p/%d", tid)
	oldURL = fmt.Sprintf("https://tieba.baidu.com/mo/q/m?kz=%d", tid)
	touchURL = fmt.Sprintf("https://waptieba.baidu.com/p/%d?lp=5028&mo_device=1", tid)
	return
}

// ForumURL returns the forum home page URL.
func ForumURL(name string) string {
	return "https://tieba.baidu.com/f?kw=" + url.QueryEscape(name) + "&ie=utf-8"
}