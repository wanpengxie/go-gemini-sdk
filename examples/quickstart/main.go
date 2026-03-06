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

	out, err := gemini.Query(ctx, "用一句话解释 ACP 协议", opts...)
	if err != nil {
		log.Fatalf("query failed: %v", err)
	}

	fmt.Println(out)
}
