package gemini

import (
	"context"
	"strings"
)

// Query runs one prompt roundtrip and returns concatenated model text output.
func Query(ctx context.Context, prompt string, opts ...Option) (string, error) {
	client := NewClient(opts...)
	if err := client.Connect(ctx); err != nil {
		return "", err
	}
	defer func() {
		_ = client.Close()
	}()

	if err := client.Send(ctx, prompt); err != nil {
		return "", err
	}

	events, errs := client.ReceiveWithErrors()
	var out strings.Builder

	for events != nil || errs != nil {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case err, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			if err != nil {
				return "", err
			}
		case ev, ok := <-events:
			if !ok {
				events = nil
				continue
			}

			if ev.Type == EventTypeMessage || ev.Type == EventTypeMessageChunk {
				out.WriteString(ev.Text)
			}
			if ev.Done || ev.Type == EventTypeCompleted {
				return out.String(), nil
			}
		}
	}

	if err := client.Err(); err != nil {
		return "", err
	}
	return out.String(), nil
}
