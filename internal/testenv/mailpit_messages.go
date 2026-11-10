package testenv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"
)

const (
	// mailpitRequestTimeout bounds one Mailpit API request.
	mailpitRequestTimeout = 10 * time.Second
	// mailpitPageLimit is the most messages one list request returns. A
	// mailbox larger than this fails [MailpitFixture.Messages].
	mailpitPageLimit = 1000
	// mailpitMaxResponseBytes bounds one API response body.
	mailpitMaxResponseBytes = 16 << 20
)

// MailpitMessage is one message the SMTP server accepted.
type MailpitMessage struct {
	// To lists the addresses of the To header.
	To      []string
	Subject string
	// Text is the plain-text body.
	Text string
}

// mailpitAddress is one address of a Mailpit message record.
type mailpitAddress struct {
	Address string `json:"Address"`
}

// mailpitList is the part of the GET /api/v1/messages response the fixture
// reads.
type mailpitList struct {
	Total    int `json:"total"`
	Messages []struct {
		ID string `json:"ID"`
	} `json:"messages"`
}

// mailpitDetail is the part of the GET /api/v1/message/{id} response the
// fixture reads.
type mailpitDetail struct {
	To      []mailpitAddress `json:"To"`
	Subject string           `json:"Subject"`
	Text    string           `json:"Text"`
}

// Messages returns every stored message, newest first.
func (f MailpitFixture) Messages(ctx context.Context) ([]MailpitMessage, error) {
	body, err := f.request(ctx, http.MethodGet, "/api/v1/messages?limit="+strconv.Itoa(mailpitPageLimit))
	if err != nil {
		wrapped := fmt.Errorf("list Mailpit messages: %w", err)
		slog.ErrorContext(ctx, "testenv.mailpit.list_failed", slog.String("err", wrapped.Error()))
		return nil, wrapped
	}
	var list mailpitList
	if err := json.Unmarshal(body, &list); err != nil {
		slog.ErrorContext(ctx, "testenv.mailpit.list_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("decode Mailpit message list: %w", err)
	}
	if list.Total > len(list.Messages) {
		return nil, fmt.Errorf("the Mailpit server stores %d messages, more than the %d one list request returns", list.Total, len(list.Messages))
	}
	messages := make([]MailpitMessage, 0, len(list.Messages))
	for _, summary := range list.Messages {
		message, err := f.message(ctx, summary.ID)
		if err != nil {
			return nil, err
		}
		messages = append(messages, message)
	}
	return messages, nil
}

// message reads the recipients, subject, and plain-text body of one message.
func (f MailpitFixture) message(ctx context.Context, id string) (MailpitMessage, error) {
	body, err := f.request(ctx, http.MethodGet, "/api/v1/message/"+id)
	if err != nil {
		wrapped := fmt.Errorf("read Mailpit message %s: %w", id, err)
		slog.ErrorContext(ctx, "testenv.mailpit.read_failed", slog.String("err", wrapped.Error()))
		return MailpitMessage{}, wrapped
	}
	var detail mailpitDetail
	if err := json.Unmarshal(body, &detail); err != nil {
		slog.ErrorContext(ctx, "testenv.mailpit.read_failed", slog.String("err", err.Error()))
		return MailpitMessage{}, fmt.Errorf("decode Mailpit message %s: %w", id, err)
	}
	recipients := make([]string, 0, len(detail.To))
	for _, address := range detail.To {
		recipients = append(recipients, address.Address)
	}
	return MailpitMessage{To: recipients, Subject: detail.Subject, Text: detail.Text}, nil
}

// DeleteAll deletes every stored message.
func (f MailpitFixture) DeleteAll(ctx context.Context) error {
	if _, err := f.request(ctx, http.MethodDelete, "/api/v1/messages"); err != nil {
		wrapped := fmt.Errorf("delete Mailpit messages: %w", err)
		slog.ErrorContext(ctx, "testenv.mailpit.delete_failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	return nil
}

// waitForMailpit polls the readiness endpoint until it answers 200 or ctx
// ends.
func waitForMailpit(ctx context.Context, fixture MailpitFixture) error {
	for {
		_, lastErr := fixture.request(ctx, http.MethodGet, "/readyz")
		if lastErr == nil {
			return nil
		}
		slog.DebugContext(ctx, "testenv.mailpit.probe", slog.String("reason", lastErr.Error()))
		if !sleepOrDone(ctx) {
			wrapped := fmt.Errorf("wait for Mailpit at %s: %s: %w", fixture.APIBaseURL, lastErr.Error(), ctx.Err())
			slog.ErrorContext(ctx, "testenv.mailpit.wait_failed", slog.String("err", wrapped.Error()))
			return wrapped
		}
	}
}

// request sends one API request without a body and returns the response
// body. A status other than 200 is an error that includes the body.
func (f MailpitFixture) request(ctx context.Context, method, path string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, method, f.APIBaseURL+path, nil)
	if err != nil {
		slog.DebugContext(ctx, "testenv.mailpit.request_failed", slog.String("reason", err.Error()))
		return nil, errors.New("build Mailpit request " + method + " " + path + ": " + err.Error())
	}
	response, err := (&http.Client{Timeout: mailpitRequestTimeout}).Do(request)
	if err != nil {
		slog.DebugContext(ctx, "testenv.mailpit.unavailable", slog.String("reason", err.Error()))
		return nil, errors.New("send Mailpit request " + method + " " + path + ": " + err.Error())
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, mailpitMaxResponseBytes))
	if err != nil {
		slog.DebugContext(ctx, "testenv.mailpit.response_unreadable", slog.String("reason", err.Error()))
		return nil, errors.New("read Mailpit response to " + method + " " + path + ": " + err.Error())
	}
	if response.StatusCode != http.StatusOK {
		return nil, errors.New("the Mailpit server answered " + method + " " + path + " with " + response.Status + ": " + string(body))
	}
	return body, nil
}
