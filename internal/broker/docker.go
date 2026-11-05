package broker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
)

// dockerClient speaks the Docker Engine API over its Unix socket; no SDK, only the few calls the broker needs.
type dockerClient struct{ http *http.Client }

const apiVersion = "/v1.47"

func newDockerClient(socket string) *dockerClient {
	dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}
	return &dockerClient{http: &http.Client{Transport: &http.Transport{DialContext: dial}}}
}

// call sends one request and fails unless the status is one of allowed; out, if set, receives a 2xx JSON body.
func (d *dockerClient) call(ctx context.Context, method, path string, body, out any, allowed ...int) error {
	_, err := d.do(ctx, method, path, body, out, allowed)
	return err
}

// get reads an object; a 404 reports found=false instead of an error.
func (d *dockerClient) get(ctx context.Context, path string, out any) (bool, error) {
	status, err := d.do(ctx, "GET", path, nil, out, []int{200, 404})
	return status == 200, err
}

func (d *dockerClient) do(ctx context.Context, method, path string, body, out any, allowed []int) (int, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		reader = bytes.NewReader(raw)
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://docker"+apiVersion+path, reader)
	if err != nil {
		return 0, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := d.http.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return response.StatusCode, err
	}
	if !slices.Contains(allowed, response.StatusCode) {
		var message struct{ Message string }
		json.Unmarshal(data, &message)
		return response.StatusCode, fmt.Errorf("docker %s %s: %d %s", method, path, response.StatusCode, message.Message)
	}
	if out != nil && len(data) > 0 && response.StatusCode < 300 {
		return response.StatusCode, json.Unmarshal(data, out)
	}
	return response.StatusCode, nil
}
