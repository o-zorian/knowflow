package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: healthcheck URL")
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, os.Args[1], nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid healthcheck URL")
		os.Exit(2)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck request failed")
		os.Exit(1)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		fmt.Fprintln(os.Stderr, "healthcheck returned", response.StatusCode)
		os.Exit(1)
	}
}
