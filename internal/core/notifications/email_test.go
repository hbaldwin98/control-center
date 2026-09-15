package notifications

import (
	"strings"
	"testing"
)

func TestEmailChannelParsesTLSSettingsAndFormatsPlainText(t *testing.T) {
	ch, err := newEmailChannel(ChannelConfig{
		ID: "mail", CredentialID: "smtp-password", Settings: map[string]string{
			"server":   "smtp.example.test:587",
			"from":     "Control Center <alerts@example.test>",
			"to":       "me@example.test, Other <other@example.test>",
			"username": "alerts@example.test",
			"security": "starttls",
		},
	}, &fakeCreds{token: "secret"})
	if err != nil {
		t.Fatalf("newEmailChannel: %v", err)
	}
	c, ok := ch.(emailChannel)
	if !ok {
		t.Fatalf("channel = %T, want emailChannel", ch)
	}
	message := formatEmailMessage(c.from, c.recipients, Notification{
		Title: "Saved lot closes soon",
		Body:  "Lot 42\ncloses within 10 minutes",
	})
	for _, want := range []string{
		"From: ",
		"alerts@example.test",
		"me@example.test",
		"other@example.test",
		"Subject: Saved lot closes soon\r\n",
		"Content-Type: text/plain; charset=UTF-8\r\n\r\n",
		"Lot 42\r\ncloses within 10 minutes\r\n",
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("message missing %q:\n%s", want, message)
		}
	}
	if strings.Contains(message, "secret") {
		t.Fatal("SMTP credential leaked into message")
	}
}

func TestEmailSettingsRejectPlaintextOutsideLocalhost(t *testing.T) {
	_, err := parseEmailSettings(ChannelConfig{Settings: map[string]string{
		"server":   "smtp.example.test:25",
		"from":     "alerts@example.test",
		"to":       "me@example.test",
		"security": "plain",
	}})
	if err == nil || !strings.Contains(err.Error(), "localhost") {
		t.Fatalf("error = %v, want localhost restriction", err)
	}
}

func TestEmailSettingsAcceptLocalPlainRelay(t *testing.T) {
	settings, err := parseEmailSettings(ChannelConfig{Settings: map[string]string{
		"server":   "localhost:2525",
		"from":     "alerts@example.test",
		"to":       "me@example.test",
		"security": "plain",
	}})
	if err != nil {
		t.Fatalf("parse local relay: %v", err)
	}
	if settings.server != "localhost:2525" || settings.security != "plain" {
		t.Fatalf("settings = %#v", settings)
	}
}
