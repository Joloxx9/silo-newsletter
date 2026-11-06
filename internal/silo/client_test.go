package silo_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/joloxx9/silo-newsletter/internal/silo"
	"github.com/joloxx9/silo-newsletter/internal/silo/silotest"
)

func newClient(t *testing.T, key string) (*silotest.Fake, *silo.Client, *[]time.Duration) {
	t.Helper()
	fake := silotest.New(t, time.Now())
	client, err := silo.New(fake.URL(), key)
	if err != nil {
		t.Fatal(err)
	}
	var waits []time.Duration
	client.Sleep = func(_ context.Context, delay time.Duration) error { waits = append(waits, delay); return nil }
	return fake, client, &waits
}

func TestCheckUsesOnlyGETAndPagesEverything(t *testing.T) {
	fake, client, waits := newClient(t, silotest.Key)
	accounts, libraries, err := client.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if accounts != 4 || libraries != 2 {
		t.Fatalf("accounts=%d libraries=%d", accounts, libraries)
	}
	if len(*waits) != 1 || (*waits)[0] != time.Second {
		t.Fatalf("429 Retry-After was not honored: %v", *waits)
	}
	for _, request := range fake.Requests() {
		if request.Method != "GET" {
			t.Fatalf("non-GET request: %+v", request)
		}
		if strings.Contains(request.Query, silotest.Key) {
			t.Fatal("API key sent in a query string")
		}
		if strings.HasPrefix(request.Path, "/api/v2/catalog/items/") && request.ProfileID != silotest.ProfileID {
			t.Fatal("item detail read without the key owner's primary profile")
		}
	}
}

func TestRejectedKeyIsReportedWithoutSecrets(t *testing.T) {
	fake, client, _ := newClient(t, "sa_wrong")
	_, _, err := client.Check(context.Background())
	if err == nil || !strings.Contains(err.Error(), "odrzuciło klucz API") {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(err.Error(), "sa_wrong") || strings.Contains(err.Error(), fake.URL()) {
		t.Fatal("error message leaks the key or address")
	}
}

func TestArtworkIsFetchedWithoutKeyAndOnlyFromSilo(t *testing.T) {
	fake, client, _ := newClient(t, silotest.Key)
	data, err := client.Image(context.Background(), "/artwork/x.webp?sig=fictional")
	if err != nil || len(data) == 0 {
		t.Fatalf("artwork download failed: %v", err)
	}
	for _, foreign := range []string{"https://elsewhere.example.net/a.webp", "//elsewhere.example.net/a.webp", "file:///etc/passwd"} {
		if _, err := client.Image(context.Background(), foreign); err == nil {
			t.Fatalf("foreign artwork address %q was fetched", foreign)
		}
	}
	for _, request := range fake.Requests() {
		if strings.HasPrefix(request.Path, "/artwork/") && request.Authorization != "" {
			t.Fatal("API key sent with artwork")
		}
	}
}

func TestHistoryIsScopedToTheAccount(t *testing.T) {
	_, client, _ := newClient(t, silotest.Key)
	now := time.Now()
	entries, err := client.History(context.Background(), now.AddDate(0, 0, -7), now.Add(time.Hour), "2")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected Bob's two plays, got %d", len(entries))
	}
	for _, entry := range entries {
		if entry.UserID != "2" {
			t.Fatal("another account's play was returned")
		}
	}
}
