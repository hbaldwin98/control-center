package pluginhost

import (
	"context"
	"errors"
	"testing"

	hostbrowser "github.com/hbaldwin98/control-center/host/browser"
	hostpolicy "github.com/hbaldwin98/control-center/host/policy"
	"github.com/hbaldwin98/control-center/internal/core/browser"
)

type readerBrowserStub struct {
	doc  browser.Document
	err  error
	opts browser.OpenOptions
	url  string
}

func (*readerBrowserStub) Open(context.Context, browser.OpenOptions) (browser.Session, error) {
	panic("unexpected Open")
}

func (*readerBrowserStub) Do(context.Context, browser.OpenOptions, browser.Request) (browser.Resource, error) {
	panic("unexpected Do")
}

func (s *readerBrowserStub) Read(_ context.Context, opts browser.OpenOptions, url string) (browser.Document, error) {
	s.opts = opts
	s.url = url
	return s.doc, s.err
}

func TestBrowserAdapterReadsAndMapsReaderErrors(t *testing.T) {
	const pageURL = "https://shop.example/item"
	stub := &readerBrowserStub{doc: browser.Document{URL: pageURL, Content: "Model X $129"}}
	adapter := browserAdapter{inner: stub}

	doc, err := adapter.Read(context.Background(), hostbrowser.OpenOptions{AllowedHosts: []string{"shop.example"}}, pageURL)
	if err != nil {
		t.Fatal(err)
	}
	if doc.URL != pageURL || doc.Content != "Model X $129" || stub.url != pageURL || len(stub.opts.AllowedHosts) != 1 || stub.opts.AllowedHosts[0] != "shop.example" {
		t.Fatalf("document=%+v url=%q opts=%+v", doc, stub.url, stub.opts)
	}

	stub.err = browser.ErrReader
	if _, err := adapter.Read(context.Background(), hostbrowser.OpenOptions{}, pageURL); !errors.Is(err, hostbrowser.ErrReader) {
		t.Fatalf("reader error = %v", err)
	}
}

func TestDisabledBrowserRejectsRead(t *testing.T) {
	_, err := (disabledBrowser{}).Read(context.Background(), hostbrowser.OpenOptions{}, "https://shop.example/item")
	if !errors.Is(err, hostpolicy.ErrPluginDisabled) {
		t.Fatalf("Read() = %v", err)
	}
}
