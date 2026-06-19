// HTTP fetcher for OCI registry traffic and IPSW downloads. Built on Go's
// net/http: the response body is streamed to the caller in 16 MiB chunks.
//
// A fresh client with no cookie jar is used so cookies are never carried
// between requests — Harbor expects a CSRF token whenever the HTTP client
// carries a session cookie and fails otherwise (cirruslabs/tart#295). The
// default transport honours the HTTP(S)_PROXY / NO_PROXY environment variables.
//go:build darwin

package fetcher

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"sync"
)

// fetcherBufferFlushSize is the streaming chunk size (16 MiB).
const fetcherBufferFlushSize = 16 * 1024 * 1024

// fetcherClient is the shared client; no cookie jar (see package doc).
var fetcherClient = sync.OnceValue(func() *http.Client {
	return &http.Client{}
})

// FetchRequest describes an HTTP request in Go-native terms.
type FetchRequest struct {
	URL    string
	Method string      // "" defaults to GET
	Header http.Header // optional request headers (multi-value)
	Body   []byte      // optional request body
}

// FetchResponse carries the response metadata (the body arrives via the chunk
// channel returned alongside it).
type FetchResponse struct {
	StatusCode    int
	Header        http.Header
	ContentLength int64
}

// FetchChunk is one element of the streamed response body. A chunk with Err set
// terminates the stream early.
type FetchChunk struct {
	Data []byte
	Err  error
}

// FetcherFetch performs the request and streams the response body. The chunk
// channel is closed after the final chunk. viaFile is accepted for parity with
// the previous URLSession-based implementation but has no effect (the body is
// streamed directly rather than spooled to a temporary file).
func FetcherFetch(ctx context.Context, req FetchRequest, viaFile bool) (<-chan FetchChunk, *FetchResponse, error) {
	_ = viaFile

	method := req.Method
	if method == "" {
		method = http.MethodGet
	}
	var body io.Reader
	if req.Body != nil {
		body = bytes.NewReader(req.Body)
	}
	httpReq, err := http.NewRequestWithContext(ctx, method, req.URL, body)
	if err != nil {
		return nil, nil, err
	}
	for key, values := range req.Header {
		for _, value := range values {
			httpReq.Header.Add(key, value)
		}
	}

	resp, err := fetcherClient().Do(httpReq)
	if err != nil {
		return nil, nil, err
	}

	response := &FetchResponse{
		StatusCode:    resp.StatusCode,
		Header:        resp.Header,
		ContentLength: resp.ContentLength,
	}

	chunks := make(chan FetchChunk)
	go func() {
		defer close(chunks)
		defer resp.Body.Close()

		buffer := make([]byte, fetcherBufferFlushSize)
		for {
			n, err := resp.Body.Read(buffer)
			if n > 0 {
				chunk := append([]byte(nil), buffer[:n]...)
				select {
				case chunks <- FetchChunk{Data: chunk}:
				case <-ctx.Done():
					return
				}
			}
			if err == io.EOF {
				return
			}
			if err != nil {
				select {
				case chunks <- FetchChunk{Err: err}:
				case <-ctx.Done():
				}
				return
			}
		}
	}()

	return chunks, response, nil
}
