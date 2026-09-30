package worker

import (
	"context"
	"strings"
	"testing"
)

func TestRedactingSink(t *testing.T) {
	t.Parallel()

	recorder := &recordingSink{}
	sink := RedactingSink{Next: recorder}
	message := "password=hunter2 API_KEY: abc123 Authorization: Bearer token-value safe=value"
	if err := sink.Emit(context.Background(), Event{Message: message}); err != nil {
		t.Fatalf("emit: %v", err)
	}
	got := recorder.events[0].Message
	for _, secret := range []string{"hunter2", "abc123", "token-value"} {
		if strings.Contains(got, secret) {
			t.Fatalf("redacted output still contains %q: %s", secret, got)
		}
	}
	if !strings.Contains(got, "safe=value") {
		t.Fatalf("redactor removed non-secret content: %s", got)
	}
}
