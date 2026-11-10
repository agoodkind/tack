package testenv

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/moby/moby/client"
)

const macRunnerEventStreamLimit int64 = 32 << 20

type macRunnerEventTransport struct {
	transport *http.Transport
}

func (transport *macRunnerEventTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := transport.transport.RoundTrip(request)
	if err != nil {
		slog.ErrorContext(
			request.Context(),
			"testenv.run_mac.event_request_failed",
			slog.String("err", err.Error()),
		)

		return nil, fmt.Errorf("request Docker events: %w", err)
	}

	if strings.HasSuffix(request.URL.Path, "/events") {
		response.Body = &macRunnerBoundedEventBody{
			ReadCloser: response.Body,
			remaining:  macRunnerEventStreamLimit,
		}
	}

	return response, nil
}

type macRunnerBoundedEventBody struct {
	io.ReadCloser
	remaining int64
}

func (body *macRunnerBoundedEventBody) Read(buffer []byte) (int, error) {
	if body.remaining <= 0 {
		return 0, errors.New("docker event stream exceeds 32 MiB")
	}

	if int64(len(buffer)) > body.remaining {
		buffer = buffer[:body.remaining]
	}

	count, err := body.ReadCloser.Read(buffer)
	body.remaining -= int64(count)
	if errors.Is(err, io.EOF) {
		return count, io.EOF
	}

	if err != nil {
		return count, fmt.Errorf("read Docker events: %w", err)
	}

	return count, nil
}

func (execution *macRunnerExecution) connectEvents(socket, version string) error {
	transport := &http.Transport{
		MaxIdleConns:    6,
		IdleConnTimeout: 30 * time.Second,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		},
	}
	docker, err := client.New(
		client.WithHost("unix://"+socket),
		client.WithHTTPClient(&http.Client{
			Transport:     &macRunnerEventTransport{transport: transport},
			CheckRedirect: client.CheckRedirect,
		}),
		client.WithAPIVersion(version),
	)
	if err != nil {
		transport.CloseIdleConnections()
		return macRunnerFailure("create bounded event client", err)
	}

	execution.eventClient = docker
	execution.eventTransport = transport

	return nil
}
