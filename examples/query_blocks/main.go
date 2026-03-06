package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	gemini "github.com/wanpengxie/go-gemini-sdk"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	opts := make([]gemini.Option, 0, 1)
	if bin := os.Getenv("GEMINI_BINARY"); bin != "" {
		opts = append(opts, gemini.WithBinaryPath(bin))
	}

	messages, errs := gemini.Query(ctx, "解释一下 ACP 的 turn 概念", opts...)

	for messages != nil || errs != nil {
		select {
		case err, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			if err != nil {
				log.Fatalf("stream error: %v", err)
			}
		case msg, ok := <-messages:
			if !ok {
				messages = nil
				continue
			}
			if renderQueryMessage(msg) {
				fmt.Println()
			}
		}
	}
}

func renderQueryMessage(msg gemini.Message) bool {
	switch m := msg.(type) {
	case *gemini.AssistantMessage:
		for _, block := range m.Content {
			switch b := block.(type) {
			case *gemini.TextBlock:
				fmt.Print(b.Text)
			case *gemini.ThinkingBlock:
				fmt.Print(b.Thinking)
			case *gemini.ToolUseBlock:
				fmt.Printf("\n[tool_call] %s (%s)\n", b.Name, b.ID)
			case *gemini.ToolResultBlock:
				fmt.Printf("\n[tool_result] %s (%s)\n", b.Name, b.ToolUseID)
			}
		}
	case *gemini.ResultMessage:
		return true
	}
	return false
}
