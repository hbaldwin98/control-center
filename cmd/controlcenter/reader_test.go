package main

import (
	"testing"

	"github.com/hbaldwin98/control-center/internal/core/browser"
)

func TestConfiguredBrowserReader(t *testing.T) {
	if got := configuredBrowserReader(""); got != nil {
		t.Fatalf("empty URL configured reader %T", got)
	}
	got, ok := configuredBrowserReader("http://reader:8081").(browser.JinaReader)
	if !ok || got.BaseURL != "http://reader:8081" {
		t.Fatalf("configured reader = %#v", got)
	}
}
