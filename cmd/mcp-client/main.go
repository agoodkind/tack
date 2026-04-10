// mcp-client bridges Claude Code's stdio transport to the Tack MCP HTTP server.
// It reads JSON-RPC messages from stdin, POSTs them to the configured endpoint,
// and writes responses to stdout. MCP session ID is tracked across requests.
package main

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
)

func main() {
	url := os.Getenv("TACK_URL")
	if url == "" {
		url = "https://tack.home.goodkind.io/mcp"
	}
	token := os.Getenv("TACK_TOKEN")
	if token == "" {
		token = "tack_ZBF9kJA7W7VXPvkV3G99Qdzc_xBAoSvhI_kcoeDNK7I"
	}

	logf("[init] url=%s token_len=%d", url, len(token))

	c := &client{
		url:   url,
		token: token,
		http:  &http.Client{},
	}

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4*1024*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		logf("[stdin] %s", truncate(line, 120))
		if err := c.forward(line); err != nil {
			logf("[error] %v", err)
			fmt.Fprintf(os.Stderr, "tack-mcp-client: %v\n", err)
		}
	}
	logf("[eof] stdin closed")
}

type client struct {
	url   string
	token string
	http  *http.Client

	mu        sync.Mutex
	sessionID string
}

func (c *client) forward(body string) error {
	req, err := http.NewRequest("POST", c.url, strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+c.token)

	c.mu.Lock()
	sid := c.sessionID
	c.mu.Unlock()
	if sid != "" {
		req.Header.Set("Mcp-Session-Id", sid)
	}

	logf("[http] POST %s session=%s", c.url, sid)
	resp, err := c.http.Do(req)
	if err != nil {
		logf("[http] error: %v", err)
		return err
	}
	defer resp.Body.Close()

	logf("[http] %d content-type=%s", resp.StatusCode, resp.Header.Get("Content-Type"))

	if newSID := resp.Header.Get("Mcp-Session-Id"); newSID != "" {
		c.mu.Lock()
		c.sessionID = newSID
		c.mu.Unlock()
		logf("[session] id=%s", newSID)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	ct := resp.Header.Get("Content-Type")
	if strings.Contains(ct, "text/event-stream") {
		for _, line := range strings.Split(string(data), "\n") {
			if after, ok := strings.CutPrefix(line, "data: "); ok && after != "" {
				logf("[stdout] %s", truncate(after, 120))
				fmt.Fprintln(os.Stdout, after)
			}
		}
	} else {
		s := strings.TrimSpace(string(data))
		if s != "" {
			logf("[stdout] %s", truncate(s, 120))
			fmt.Fprintln(os.Stdout, s)
		}
	}
	return nil
}

func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "tack-mcp-client: "+format+"\n", args...)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
