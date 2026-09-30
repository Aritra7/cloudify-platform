package worker

import (
	"context"
	"regexp"
)

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(password|token|secret|api[_-]?key)(\s*[:=]\s*)([^\s,;]+)`),
	regexp.MustCompile(`(?i)(authorization\s*:\s*bearer\s+)([^\s,;]+)`),
}

// RedactingSink removes common credential forms before delegating persistence.
type RedactingSink struct {
	Next EventSink
}

func (sink RedactingSink) Emit(ctx context.Context, event Event) error {
	for index, pattern := range secretPatterns {
		replacement := "${1}[REDACTED]"
		if index == 0 {
			replacement = "${1}${2}[REDACTED]"
		}
		event.Message = pattern.ReplaceAllString(event.Message, replacement)
	}
	return sink.Next.Emit(ctx, event)
}
