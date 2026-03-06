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
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	opts := make([]gemini.Option, 0, 1)
	if bin := os.Getenv("GEMINI_BINARY"); bin != "" {
		opts = append(opts, gemini.WithBinaryPath(bin))
	}
	client := gemini.NewClient(opts...)
	if err := client.Connect(ctx); err != nil {
		log.Fatalf("connect failed: %v", err)
	}
	defer func() {
		_ = client.Close()
	}()
	prompts := []string{
		"你好，请介绍一下你自己。",
		"把上一句压缩成 10 个字以内。",
		"再给一个更正式的版本。",
	}

	for _, prompt := range prompts {
		turn, err := client.Query(ctx, prompt)
		if err != nil {
			log.Fatalf("query failed: %v", err)
		}
		messages, errs := turn.Messages(), turn.Errors()

		fmt.Printf("\n>>> %s\n", prompt)
		for {
			select {
			case err, ok := <-errs:
				if !ok {
					errs = nil
					continue
				}
				if err != nil {
					log.Fatalf("receive error: %v", err)
				}
			case msg, ok := <-messages:
				if !ok {
					goto nextTurn
				}
				if renderTurnMessage(msg) {
					fmt.Println()
					goto nextTurn
				}
			}
		}
	nextTurn:
	}
}

func renderTurnMessage(msg gemini.Message) bool {
	switch m := msg.(type) {
	case *gemini.AssistantMessage:
		for _, block := range m.Content {
			switch b := block.(type) {
			case *gemini.TextBlock:
				if b.Text != "" {
					fmt.Print(b.Text)
				}
			case *gemini.ThinkingBlock:
				if b.Thinking != "" {
					fmt.Print(b.Thinking)
				}
			case *gemini.ToolUseBlock:
				fmt.Printf("\n[tool_call] name=%s id=%s\n", b.Name, b.ID)
			case *gemini.ToolResultBlock:
				fmt.Printf("\n[tool_result] name=%s id=%s\n", b.Name, b.ToolUseID)
			}
		}
	case *gemini.ResultMessage:
		if m.IsError {
			log.Fatalf("model error: %s", m.Error)
		}
		return true
	}
	return false
}
