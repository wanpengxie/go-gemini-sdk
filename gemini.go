package gemini

import "context"

// Query runs one prompt roundtrip and streams typed messages for the turn.
func Query(ctx context.Context, prompt string, opts ...Option) (<-chan Message, <-chan error) {
	client := NewClient(opts...)
	outMessages := make(chan Message, client.opts.eventBuffer)
	outErrs := make(chan error, 1)

	go func() {
		defer close(outMessages)
		defer close(outErrs)
		defer func() {
			_ = client.Close()
		}()

		if err := client.Connect(ctx); err != nil {
			outErrs <- err
			return
		}
		turn, err := client.Query(ctx, prompt)
		if err != nil {
			outErrs <- err
			return
		}

		messages := turn.Messages()
		errs := turn.Errors()
		for messages != nil || errs != nil {
			select {
			case <-ctx.Done():
				outErrs <- ctx.Err()
				return
			case err, ok := <-errs:
				if !ok {
					errs = nil
					continue
				}
				if err != nil {
					outErrs <- err
					return
				}
			case msg, ok := <-messages:
				if !ok {
					messages = nil
					continue
				}
				select {
				case <-ctx.Done():
					outErrs <- ctx.Err()
					return
				case outMessages <- msg:
				}
			}
		}
	}()

	return outMessages, outErrs
}
