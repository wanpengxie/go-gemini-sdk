// Package gemini provides a Claude-style ACP client SDK for Gemini CLI.
//
// The public API is centered around typed Message streams:
//
//	msgs, errs := gemini.Query(ctx, "Explain ACP")
//	for msg := range msgs {
//		switch m := msg.(type) {
//		case *gemini.AssistantMessage:
//			_ = m
//		case *gemini.ResultMessage:
//			_ = m
//		}
//	}
//	if err := <-errs; err != nil {
//		panic(err)
//	}
//
// Stateful clients submit turns with Client.Query and receive a dedicated
// TurnHandle instead of exposing raw session/update event streams.
package gemini
