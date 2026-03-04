package main

import (
	"context"
	"fmt"
	"log"
	"time"

	gemini "github.com/wanpengxie/go-gemini-sdk"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	out, err := gemini.Query(ctx, "用一句话解释 ACP 协议")
	if err != nil {
		log.Fatalf("query failed: %v", err)
	}

	fmt.Println(out)
}
