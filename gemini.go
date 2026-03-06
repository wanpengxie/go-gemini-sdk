package gemini

import (
	"context"
	"strings"
)

// Query runs one prompt roundtrip and returns concatenated model text output.
func Query(ctx context.Context, prompt string, opts ...Option) (string, error) {
	blocks, errs, err := QueryBlocks(ctx, prompt, opts...)
	if err != nil {
		return "", err
	}

	var out strings.Builder

	for blocks != nil || errs != nil {
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
		case block, ok := <-blocks:
			if !ok {
				blocks = nil
				continue
			}
			if block.Kind == BlockKindText {
				out.WriteString(block.Text)
			}
		}
	}
	return out.String(), nil
}

// QueryBlocks runs one prompt roundtrip and streams structured blocks for the turn.
//
// The returned channels are closed when the turn finishes, encounters error, or
// ctx is canceled.
func QueryBlocks(ctx context.Context, prompt string, opts ...Option) (<-chan StreamBlock, <-chan error, error) {
	client := NewClient(opts...)
	if err := client.Connect(ctx); err != nil {
		return nil, nil, err
	}

	outBlocks := make(chan StreamBlock, client.opts.eventBuffer)
	outErrs := make(chan error, 1)

	go func() {
		defer close(outBlocks)
		defer close(outErrs)
		defer func() {
			_ = client.Close()
		}()

		recvBlocks, recvErrs := client.ReceiveBlocksWithErrors()
		sendDone := make(chan error, 1)
		go func() {
			sendDone <- client.Send(ctx, prompt)
			close(sendDone)
		}()

		for recvBlocks != nil || recvErrs != nil || sendDone != nil {
			select {
			case <-ctx.Done():
				outErrs <- ctx.Err()
				return
			case err, ok := <-sendDone:
				if !ok {
					sendDone = nil
					continue
				}
				sendDone = nil
				if err != nil {
					outErrs <- err
					return
				}
			case err, ok := <-recvErrs:
				if !ok {
					recvErrs = nil
					continue
				}
				if err != nil {
					outErrs <- err
					return
				}
			case block, ok := <-recvBlocks:
				if !ok {
					recvBlocks = nil
					continue
				}

				select {
				case <-ctx.Done():
					outErrs <- ctx.Err()
					return
				case outBlocks <- block:
				}

				if block.Kind == BlockKindError {
					outErrs <- turnErrorFromBlock(block)
					return
				}

				if block.Done || block.Kind == BlockKindDone {
					if sendDone != nil {
						select {
						case <-ctx.Done():
							outErrs <- ctx.Err()
							return
						case err, ok := <-sendDone:
							sendDone = nil
							if ok && err != nil {
								outErrs <- err
								return
							}
						}
					}
					return
				}
			}
		}

		if err := client.Err(); err != nil {
			outErrs <- err
		}
	}()

	return outBlocks, outErrs, nil
}

func turnErrorFromBlock(block StreamBlock) error {
	msg := strings.TrimSpace(block.Error)
	if msg == "" {
		msg = "session turn failed"
	}
	return &ProtocolError{
		Method:  methodSessionUpdate,
		Message: msg,
	}
}
