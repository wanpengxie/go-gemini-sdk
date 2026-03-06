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

	blocks, errs, err := gemini.QueryBlocks(ctx, "解释一下 ACP 的 turn 概念", opts...)
	if err != nil {
		log.Fatalf("query blocks failed: %v", err)
	}

	for blocks != nil || errs != nil {
		select {
		case err, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			if err != nil {
				log.Fatalf("stream error: %v", err)
			}
		case block, ok := <-blocks:
			if !ok {
				blocks = nil
				continue
			}
			switch block.Kind {
			case gemini.BlockKindText, gemini.BlockKindThinking:
				if block.Text != "" {
					fmt.Print(block.Text)
				}
			case gemini.BlockKindToolCall:
				fmt.Printf("\n[tool_call] %s (%s)\n", block.ToolName, block.ToolCallID)
			case gemini.BlockKindToolResult:
				fmt.Printf("\n[tool_result] %s (%s)\n", block.ToolName, block.ToolCallID)
			case gemini.BlockKindDone:
				fmt.Println()
			}
		}
	}
}
