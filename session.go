package tonconnect

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/kevinburke/nacl"
	"github.com/kevinburke/nacl/box"
	"github.com/tmaxmax/go-sse"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"sync/atomic"
)

type Session struct {
	ID            nacl.Key      `json:"id"`
	PrivateKey    nacl.Key      `json:"private_key"`
	ClientID      nacl.Key      `json:"client_id,omitempty"`
	BridgeURL     string        `json:"brdige_url,omitempty"`
	LastEventID   atomic.Uint64 `json:"last_event_id,string,omitempty"`
	LastRequestID atomic.Uint64 `json:"last_request_id,string,omitempty"`
}

type BridgeMessageOptions struct {
	TTL   string
	Topic string
}

type BridgeMessageOption = func(*BridgeMessageOptions)

func NewSession() (*Session, error) {
	id, pk, err := box.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("tonconnect: failed to generate key pair: %w", err)
	}

	s := &Session{ID: id, PrivateKey: pk}
	s.LastRequestID.Store(1)
	s.LastEventID.Store(1)

	return s, nil
}

func (s *Session) connectToBridge(ctx context.Context, bridgeURL string, msgs chan<- bridgeMessage) error {
	if s.ID == nil || s.PrivateKey == nil {
		return fmt.Errorf("tonconnect: session key pair is empty")
	}

	u, err := url.Parse(bridgeURL)
	if err != nil {
		return fmt.Errorf("tonconnect: failed to parse bridge URL: %w", err)
	}

	u = u.JoinPath("/events")
	q := u.Query()
	q.Set("client_id", hex.EncodeToString((*s.ID)[:]))
	if s.LastEventID.Load() > 0 {
		q.Set("last_event_id", strconv.FormatUint(s.LastEventID.Load(), 10))
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), http.NoBody)
	if err != nil {
		return fmt.Errorf("tonconnect: failed to initialize HTTP request: %w", err)
	}

	client := sse.DefaultClient
	client.ResponseValidator = func(response *http.Response) error {
		if response.StatusCode == http.StatusServiceUnavailable {
			return ErrBridgeUnavailable
		}
		return sse.DefaultValidator(response)
	}
	conn := client.NewConnection(req)
	unsub := conn.SubscribeEvent("message", func(e sse.Event) {
		var bmsg struct {
			From    string `json:"from"`
			Message []byte `json:"message"`
		}
		id, err := strconv.ParseUint(e.LastEventID, 10, 64)
		if err != nil {
			log.Panicf("got non-int event id %q: %v", e.LastEventID, err)
		}
		s.LastEventID.Store(id)

		if err := json.Unmarshal([]byte(e.Data), &bmsg); err == nil {
			var msg walletMessage
			if clientID, err := s.decrypt(bmsg.From, bmsg.Message, &msg); err == nil {
				msgs <- bridgeMessage{BrdigeURL: bridgeURL, From: clientID, Message: msg}
			}
		}
	})
	defer unsub()

	if err := conn.Connect(); !(errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
		return fmt.Errorf("tonconnect: failed to connect to bridge: %w", err)
	}

	return nil
}

func (s *Session) sendMessage(ctx context.Context, msg any, topic string, options ...BridgeMessageOption) error {
	if s.ID == nil || s.PrivateKey == nil || s.ClientID == nil || s.BridgeURL == "" {
		return fmt.Errorf("tonconnect: session not established")
	}

	opts := &BridgeMessageOptions{TTL: "300"}
	for _, opt := range options {
		opt(opts)
	}

	u, err := url.Parse(s.BridgeURL)
	if err != nil {
		return fmt.Errorf("tonconnect: failed to parse bridge URL: %w", err)
	}

	u = u.JoinPath("/message")
	q := u.Query()
	q.Set("client_id", hex.EncodeToString((*s.ID)[:]))
	q.Set("to", hex.EncodeToString((*s.ClientID)[:]))
	if opts.TTL != "" {
		q.Set("ttl", opts.TTL)
	}
	if topic != "" {
		q.Set("topic", topic)
	}
	u.RawQuery = q.Encode()

	data, err := s.encrypt(msg)
	if err != nil {
		return err
	}

	body := bytes.NewBuffer([]byte(base64.StdEncoding.EncodeToString(data)))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), body)
	req.Header.Set("Content-Type", "text/plain")
	if err != nil {
		return fmt.Errorf("tonconnect: failed to initialize HTTP request: %w", err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("tonconnect: failed to send message: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		// TODO: parse response body according to https://github.com/ton-connect/bridge implementation
		return fmt.Errorf("tonconnect: failed to send message")
	}

	return nil
}

func (s *Session) encrypt(msg any) ([]byte, error) {
	data, err := json.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("tonconnect: failed to marshal message to encrypt: %w", err)
	}

	return box.EasySeal(data, s.ClientID, s.PrivateKey), nil
}

func (s *Session) decrypt(from string, msg []byte, v any) (nacl.Key, error) {
	clientID, err := nacl.Load(from)
	if err != nil {
		return clientID, fmt.Errorf("tonconnect: failed to load client ID: %w", err)
	}

	if s.ClientID != nil && !bytes.Equal((*s.ClientID)[:], (*clientID)[:]) {
		return clientID, fmt.Errorf("tonconnect: session and bridge message client IDs don't match")
	}

	data, err := box.EasyOpen(msg, clientID, s.PrivateKey)
	if err != nil {
		return clientID, fmt.Errorf("tonconnect: failed to decrypt bridge message: %w", err)
	}

	err = json.Unmarshal(data, v)
	if err != nil {
		return clientID, fmt.Errorf("tonconnect: failed to unmarshal decrypted data: %w", err)
	}

	return clientID, nil
}

func WithTTL(ttl uint64) BridgeMessageOption {
	return func(opts *BridgeMessageOptions) {
		opts.TTL = strconv.FormatUint(ttl, 10)
	}
}
