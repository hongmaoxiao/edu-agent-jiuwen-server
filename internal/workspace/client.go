package workspace

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
)

// SocketBroker calls cmd/broker over its Unix socket; only the server's system user may open that socket.
type SocketBroker struct{ client *http.Client }

func NewSocketBroker(path string) *SocketBroker {
	dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	}
	return &SocketBroker{client: &http.Client{Transport: &http.Transport{DialContext: dial}}}
}

// Request is what the server sends the broker; the broker derives every name from the account id.
type Request struct {
	AccountID string `json:"account_id"`
	Port      int    `json:"port"`
}

func (b *SocketBroker) call(ctx context.Context, action string, accountID string, port int) error {
	body, _ := json.Marshal(Request{AccountID: accountID, Port: port})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://broker/"+action, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := b.client.Do(request)
	if err != nil {
		return err
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("broker %s: status %d", action, response.StatusCode)
	}
	return nil
}

func (b *SocketBroker) Ensure(ctx context.Context, accountID string, port int) error {
	return b.call(ctx, "ensure", accountID, port)
}

func (b *SocketBroker) Stop(ctx context.Context, accountID string, port int) error {
	return b.call(ctx, "stop", accountID, port)
}
