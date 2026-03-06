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

	messages, errs := client.ReceiveMessagesWithErrors()
	prompts := []string{
		"你好，请介绍一下你自己。",
		"把上一句压缩成 10 个字以内。",
		"再给一个更正式的版本。",
	}

	for _, prompt := range prompts {
		if err := client.Send(ctx, prompt); err != nil {
			log.Fatalf("send failed: %v", err)
		}

		fmt.Printf("\n>>> %s\n", prompt)
		for {
			select {
			case err := <-errs:
				if err != nil {
					log.Fatalf("receive error: %v", err)
				}
			case msg, ok := <-messages:
				if !ok {
					return
				}
				switch msg.Kind {
				case gemini.BlockKindText, gemini.BlockKindThinking:
					if msg.Text != "" {
						fmt.Print(msg.Text)
					}
				case gemini.BlockKindToolCall:
					fmt.Printf("\n[tool_call] name=%s id=%s\n", msg.ToolName, msg.ToolCallID)
				case gemini.BlockKindToolResult:
					fmt.Printf("\n[tool_result] name=%s id=%s\n", msg.ToolName, msg.ToolCallID)
				case gemini.BlockKindError:
					log.Fatalf("model error: %s", msg.Error)
				}
				if msg.Done || msg.Kind == gemini.BlockKindDone {
					fmt.Println()
					goto nextTurn
				}
			}
		}
	nextTurn:
	}
}
