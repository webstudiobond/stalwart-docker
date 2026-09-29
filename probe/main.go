// Package main implements a zero-dependency healthcheck probe that validates
// Stalwart mail server availability against its internal HTTP liveness endpoint.
package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"
)

const defaultEndpoint = "http://127.0.0.1:8080/healthz/live"

const defaultTimeout = 3 * time.Second

func defaultGetTransport() http.RoundTripper {
	return http.DefaultTransport
}

var (
	getTransport = defaultGetTransport
	exitFunc     = os.Exit
)

func defaultCreateRequest(ctx context.Context, method, endpoint string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, fmt.Errorf("creating HTTP request: %w", err)
	}
	return req, nil
}

var createRequest = defaultCreateRequest

func defaultDoRequest(ctx context.Context, endpoint string) (statusCode int, err error) {
	parsedURL, err := url.Parse(endpoint)
	if err != nil {
		return 0, fmt.Errorf("parsing probe endpoint: %w", err)
	}

	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return 0, fmt.Errorf("unsupported probe protocol scheme: %s", parsedURL.Scheme)
	}

	req, err := createRequest(ctx, http.MethodGet, parsedURL.String(), http.NoBody)
	if err != nil {
		return 0, fmt.Errorf("creating probe request: %w", err)
	}

	client := &http.Client{
		Timeout:   defaultTimeout,
		Transport: getTransport(),
	}

	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("executing probe request: %w", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("closing response body: %w", closeErr)
		}
	}()

	if _, drainErr := io.Copy(io.Discard, resp.Body); drainErr != nil {
		return 0, fmt.Errorf("reading response body: %w", drainErr)
	}

	return resp.StatusCode, nil
}

var doRequest = defaultDoRequest

// CheckEndpoint queries the target endpoint using HTTP GET to verify service liveness.
func CheckEndpoint(ctx context.Context, endpoint string) int {
	statusCode, err := doRequest(ctx, endpoint)
	if err != nil || statusCode != http.StatusOK {
		return 1
	}
	return 0
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	exitFunc(CheckEndpoint(ctx, defaultEndpoint))
}
