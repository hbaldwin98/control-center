package notifications

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCloudflareChannelCarriesNotificationsAndSubscriptionOperations(t *testing.T) {
	var gotNotification map[string]any
	var gotSubscription PushSubscription
	var removed string
	var auths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/notify":
			auths = append(auths, r.Header.Get("Authorization"))
			if r.Header.Get("Idempotency-Key") != "send-1" {
				t.Errorf("idempotency key = %q", r.Header.Get("Idempotency-Key"))
			}
			if err := json.NewDecoder(r.Body).Decode(&gotNotification); err != nil {
				t.Errorf("notification body: %v", err)
			}
		case "/vapid-public-key":
			_ = json.NewEncoder(w).Encode(map[string]string{"publicKey": "BAbc"})
			return
		case "/subscriptions":
			auths = append(auths, r.Header.Get("Authorization"))
			if err := json.NewDecoder(r.Body).Decode(&gotSubscription); err != nil {
				t.Errorf("subscription body: %v", err)
			}
		case "/subscriptions/remove":
			auths = append(auths, r.Header.Get("Authorization"))
			var req struct {
				Endpoint string `json:"endpoint"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("remove body: %v", err)
			}
			removed = req.Endpoint
		default:
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	ch, err := newCloudflareChannel(ChannelConfig{
		ID: "phone", CredentialID: "worker-token", Settings: map[string]string{"endpoint": server.URL},
	}, &fakeCreds{token: "shared-secret"}, server.Client())
	if err != nil {
		t.Fatalf("newCloudflareChannel: %v", err)
	}
	if err := ch.Send(context.Background(), Delivery{
		Notification:   Notification{ID: "notification-1", Title: "Ending soon", Body: "Lot closes in 10 minutes", URL: "/bidrl/lot/1001"},
		IdempotencyKey: "send-1",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if gotNotification["title"] != "Ending soon" || gotNotification["body"] != "Lot closes in 10 minutes" || gotNotification["url"] != "/bidrl/lot/1001" {
		t.Fatalf("notification = %#v", gotNotification)
	}

	registrar, ok := ch.(PushRegistrar)
	if !ok {
		t.Fatal("cloudflare channel does not register push subscriptions")
	}
	key, err := registrar.PublicKey(context.Background())
	if err != nil || key != "BAbc" {
		t.Fatalf("public key = %q, err %v", key, err)
	}
	sub := PushSubscription{
		Endpoint: "https://push.example.test/send?token=abc",
		Keys:     PushSubscriptionKeys{P256DH: "AQ", Auth: "Ag"},
	}
	if err := registrar.RegisterSubscription(context.Background(), sub); err != nil {
		t.Fatalf("register: %v", err)
	}
	if gotSubscription.Endpoint != sub.Endpoint || gotSubscription.Keys != sub.Keys {
		t.Fatalf("subscription = %#v", gotSubscription)
	}
	if err := registrar.UnregisterSubscription(context.Background(), sub.Endpoint); err != nil {
		t.Fatalf("unregister: %v", err)
	}
	if removed != sub.Endpoint {
		t.Fatalf("removed endpoint = %q", removed)
	}
	for _, auth := range auths {
		if auth != "Bearer shared-secret" {
			t.Fatalf("authorization = %q", auth)
		}
	}
}

func TestValidatePushSubscriptionRejectsUnsafeValues(t *testing.T) {
	valid := PushSubscription{
		Endpoint: "https://push.example.test/send",
		Keys:     PushSubscriptionKeys{P256DH: "AQ", Auth: "Ag"},
	}
	for name, sub := range map[string]PushSubscription{
		"http endpoint": {Endpoint: "http://push.example.test/send", Keys: valid.Keys},
		"missing key":   {Endpoint: valid.Endpoint, Keys: PushSubscriptionKeys{P256DH: "AQ"}},
		"bad base64":    {Endpoint: valid.Endpoint, Keys: PushSubscriptionKeys{P256DH: "!", Auth: "Ag"}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validatePushSubscription(sub); err != ErrInvalidPushSubscription {
				t.Fatalf("error = %v", err)
			}
		})
	}
}
