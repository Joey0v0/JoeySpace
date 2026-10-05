package push

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/yjydist/go-im/internal/model"
	"github.com/yjydist/go-im/internal/rpcauth"
)

var (
	errTaskNotificationClientConfig   = errors.New("invalid task notification client configuration")
	errTaskNotificationClientClosed   = errors.New("task notification client closed")
	errTaskNotificationClientRoute    = errors.New("task notification route unavailable")
	errTaskNotificationClientRequest  = errors.New("invalid task notification request")
	errTaskNotificationClientTransfer = errors.New("task notification transport failed")
	errTaskNotificationClientResponse = errors.New("invalid task notification response")
	errTaskNotificationClientContext  = errors.New("task notification send context ended")
)

type TaskNotificationClientConfig struct {
	Files     rpcauth.CertificateFiles
	WSDNSName string
	Routes    map[string]string
}

type TaskNotificationClient struct {
	routes    map[string]url.URL
	client    *http.Client
	transport *http.Transport
	closed    atomic.Bool
}

func NewTaskNotificationClient(cfg TaskNotificationClientConfig) (*TaskNotificationClient, error) {
	if len(cfg.Routes) == 0 {
		return nil, errTaskNotificationClientConfig
	}
	routes := make(map[string]url.URL, len(cfg.Routes))
	for onlineAddr, origin := range cfg.Routes {
		if !validTaskNotificationHostPort(onlineAddr) {
			return nil, errTaskNotificationClientConfig
		}
		u, err := url.Parse(origin)
		if err != nil || u.Scheme != "https" || !validTaskNotificationHostPort(u.Host) || u.User != nil ||
			u.Path != "" || u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" || u.Opaque != "" || u.String() != origin {
			return nil, errTaskNotificationClientConfig
		}
		routes[onlineAddr] = *u
	}
	tlsConfig, err := rpcauth.NewNotificationClientTLSConfig(cfg.Files, cfg.WSDNSName)
	if err != nil {
		return nil, errTaskNotificationClientConfig
	}
	transport := &http.Transport{
		TLSClientConfig:       tlsConfig,
		TLSHandshakeTimeout:   3 * time.Second,
		ResponseHeaderTimeout: 3 * time.Second,
		MaxIdleConnsPerHost:   2,
	}
	return &TaskNotificationClient{
		routes:    routes,
		transport: transport,
		client: &http.Client{
			Transport: transport,
			Timeout:   3 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

func validTaskNotificationHostPort(value string) bool {
	if value == "" || strings.TrimSpace(value) != value || strings.ContainsAny(value, "/\\?#@%") {
		return false
	}
	host, port, err := net.SplitHostPort(value)
	if err != nil || host == "" || net.JoinHostPort(host, port) != value {
		return false
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 || strconv.Itoa(number) != port {
		return false
	}
	if net.ParseIP(host) != nil {
		return true
	}
	if strings.Contains(host, ":") || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, ch := range label {
			if ch != '-' && (ch < 'a' || ch > 'z') && (ch < 'A' || ch > 'Z') && (ch < '0' || ch > '9') {
				return false
			}
		}
	}
	return true
}

func (c *TaskNotificationClient) Send(ctx context.Context, onlineAddr string, event model.TaskNotificationEvent) (model.TaskNotificationDelivery, error) {
	var empty model.TaskNotificationDelivery
	if c == nil || c.client == nil || ctx == nil {
		return empty, errTaskNotificationClientRequest
	}
	if c.closed.Load() {
		return empty, errTaskNotificationClientClosed
	}
	if ctx.Err() != nil {
		return empty, errTaskNotificationClientContext
	}
	origin, ok := c.routes[onlineAddr]
	if !ok {
		return empty, errTaskNotificationClientRoute
	}
	if event.Validate() != nil {
		return empty, errTaskNotificationClientRequest
	}
	root := origin
	root.Path = model.TaskNotificationPushPath
	root.RawPath = ""
	body, err := json.Marshal(event)
	if err != nil || len(body) > model.TaskNotificationMaxWireBytes {
		return empty, errTaskNotificationClientRequest
	}
	sendCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(sendCtx, http.MethodPost, root.String(), bytes.NewReader(body))
	if err != nil {
		return empty, errTaskNotificationClientRequest
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return empty, errTaskNotificationClientTransfer
	}
	defer resp.Body.Close()
	if sendCtx.Err() != nil {
		return empty, errTaskNotificationClientContext
	}
	if resp.StatusCode != http.StatusOK {
		return empty, errTaskNotificationClientTransfer
	}
	wire, err := io.ReadAll(io.LimitReader(resp.Body, model.TaskNotificationMaxWireBytes+1))
	if err != nil {
		return empty, errTaskNotificationClientTransfer
	}
	if sendCtx.Err() != nil {
		return empty, errTaskNotificationClientContext
	}
	if len(wire) > model.TaskNotificationMaxWireBytes {
		return empty, errTaskNotificationClientResponse
	}
	result, err := decodeTaskNotificationDelivery(wire, event.NotificationID)
	if err != nil {
		return empty, errTaskNotificationClientResponse
	}
	if sendCtx.Err() != nil {
		return empty, errTaskNotificationClientContext
	}
	return result, nil
}

func decodeTaskNotificationDelivery(wire []byte, expectedID int64) (model.TaskNotificationDelivery, error) {
	var delivery model.TaskNotificationDelivery
	if len(wire) == 0 || len(wire) > model.TaskNotificationMaxWireBytes || !utf8.Valid(wire) {
		return delivery, errTaskNotificationClientResponse
	}
	decoder := json.NewDecoder(bytes.NewReader(wire))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return delivery, errTaskNotificationClientResponse
	}
	seen := make(map[string]bool, 2)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return model.TaskNotificationDelivery{}, errTaskNotificationClientResponse
		}
		key, ok := token.(string)
		if !ok || seen[key] {
			return model.TaskNotificationDelivery{}, errTaskNotificationClientResponse
		}
		seen[key] = true
		var raw json.RawMessage
		if decoder.Decode(&raw) != nil {
			return model.TaskNotificationDelivery{}, errTaskNotificationClientResponse
		}
		switch key {
		case "notification_id":
			var text string
			if json.Unmarshal(raw, &text) != nil {
				return model.TaskNotificationDelivery{}, errTaskNotificationClientResponse
			}
			id, err := strconv.ParseInt(text, 10, 64)
			if err != nil || id <= 0 || strconv.FormatInt(id, 10) != text || string(raw) != `"`+text+`"` {
				return model.TaskNotificationDelivery{}, errTaskNotificationClientResponse
			}
			delivery.NotificationID = id
		case "outcome":
			if json.Unmarshal(raw, &delivery.Outcome) != nil {
				return model.TaskNotificationDelivery{}, errTaskNotificationClientResponse
			}
		default:
			return model.TaskNotificationDelivery{}, errTaskNotificationClientResponse
		}
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') || len(seen) != 2 {
		return model.TaskNotificationDelivery{}, errTaskNotificationClientResponse
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF || delivery.Validate(expectedID) != nil {
		return model.TaskNotificationDelivery{}, errTaskNotificationClientResponse
	}
	return delivery, nil
}

func (c *TaskNotificationClient) Close() error {
	if c == nil {
		return nil
	}
	if c.closed.Swap(true) {
		return nil
	}
	if c.transport != nil {
		c.transport.CloseIdleConnections()
	}
	return nil
}
