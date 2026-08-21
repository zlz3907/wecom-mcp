package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/zhonglizhi/wecom-mcp-v2/internal/mcp"
)

func main() {
	configPath := flag.String("config", "", "absolute instance configuration path")
	flag.Parse()
	if *configPath == "" {
		fmt.Fprintln(os.Stderr, "--config is required")
		os.Exit(2)
	}
	server := mcp.New(*configPath)
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)
	for scanner.Scan() {
		result := server.Handle(context.Background(), scanner.Bytes())
		if result == nil {
			continue
		}
		encoded, _ := json.Marshal(result)
		fmt.Println(string(encoded))
	}
}
