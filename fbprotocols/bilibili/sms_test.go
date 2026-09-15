package bilibili

import (
	"net/url"
	"strings"
	"testing"
)

func TestSmsFormPreview(t *testing.T) {
	form := url.Values{}
	form.Set("cid", "86")
	form.Set("tel", "13800138000")
	form.Set("source", "main_web")
	form.Set("token", "tok")
	form.Set("challenge", "chl")
	form.Set("validate", "val")
	form.Set("seccode", "val|jordan")
	got := smsFormPreview(form)
	for _, want := range []string{
		"cid=86", "tel=13800138000", "source=main_web",
		"token=tok", "challenge=chl", "validate=val", "seccode=val|jordan",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("smsFormPreview missing %q:\n%s", want, got)
		}
	}
}

func TestSmsSendFormSeccodeAuto(t *testing.T) {
	smsRememberCaptcha("tok", "gtid", "chl")
	defer smsRememberCaptcha("", "", "")

	form := smsSendForm("13800138000", "val", "chl", "")
	if got := form.Get("seccode"); got != "val|jordan" {
		t.Fatalf("seccode auto: got %q want %q", got, "val|jordan")
	}
	if got := form.Get("token"); got != "tok" {
		t.Fatalf("token: got %q want tok", got)
	}
	if form.Get("validate") != "val" || form.Get("challenge") != "chl" {
		t.Fatalf("captcha fields missing: %v", form)
	}

	explicit := smsSendForm("13800138000", "val", "chl", "custom")
	if got := explicit.Get("seccode"); got != "custom" {
		t.Fatalf("explicit seccode: got %q want custom", got)
	}
}

func TestSmsSendFormNoCaptchaWhenIncomplete(t *testing.T) {
	form := smsSendForm("13800138000", "", "", "")
	if _, ok := form["challenge"]; ok {
		t.Fatalf("challenge present without validate: %v", form)
	}
	if _, ok := form["validate"]; ok {
		t.Fatalf("validate present when empty: %v", form)
	}
	if _, ok := form["seccode"]; ok {
		t.Fatalf("seccode present when empty: %v", form)
	}
}
