package migrations

import "testing"

func TestCanTransition(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		from Status
		to   Status
		want bool
	}{
		{name: "queued starts", from: StatusQueued, to: StatusRunning, want: true},
		{name: "queued cancels", from: StatusQueued, to: StatusCancelled, want: true},
		{name: "running succeeds", from: StatusRunning, to: StatusSucceeded, want: true},
		{name: "running requests cancellation", from: StatusRunning, to: StatusCancelling, want: true},
		{name: "cancellation completes", from: StatusCancelling, to: StatusCancelled, want: true},
		{name: "terminal cannot restart", from: StatusSucceeded, to: StatusRunning, want: false},
		{name: "cannot skip running", from: StatusQueued, to: StatusSucceeded, want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := CanTransition(test.from, test.to); got != test.want {
				t.Fatalf("CanTransition(%q, %q) = %v, want %v", test.from, test.to, got, test.want)
			}
		})
	}
}
