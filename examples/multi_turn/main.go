package main

import (
	"context"
	"fmt"
	"log"
	"time"

	gemini "github.com/wanpengxie/go-gemini-sdk"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	client := gemini.NewClient()
	if err := client.Connect(ctx); err != nil {
		log.Fatalf("connect failed: %v", err)
	}
	defer func() {
		_ = client.Close()
	}()

	events, errs := client.ReceiveWithErrors()
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
			case ev, ok := <-events:
				if !ok {
					return
				}
				if ev.Text != "" {
					fmt.Print(ev.Text)
				}
				if ev.Done || ev.Type == gemini.EventTypeCompleted {
					fmt.Println()
					goto nextTurn
				}
			}
		}
	nextTurn:
	}
}
