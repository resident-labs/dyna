package webpush

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
)

const (
	defaultTTL  = 24 * time.Hour
	sendTimeout = 15 * time.Second
	sendWorkers = 8
)

// Options are the per-message push headers.
type Options struct {
	TTL     time.Duration // how long the push service keeps the message; 0 means 24 hours
	Urgency string        // "normal" (default) or "high"
	Topic   string        // optional; at most 32 base64url characters; replaces a waiting message with the same topic
}

// Result is the outcome of one send in SendAll.
type Result struct {
	DeviceID string
	Err      error
}

// StatusError is a push service answer that is not a success. It names the
// host only.
type StatusError struct {
	Host string
	Code int
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("webpush: %s answered HTTP %d", e.Host, e.Code)
}

// ErrGone means the push service dropped the subscription (HTTP 404 or 410).
// The store has already deleted it.
var ErrGone = errors.New("webpush: subscription is gone")

// Sender posts encrypted messages to push services.
type Sender struct {
	store  *Store
	vapid  *VAPID
	client *http.Client
}

// NewSender makes a Sender. Its HTTP client follows no redirects and times out
// after 15 seconds.
func NewSender(store *Store, vapid *VAPID) *Sender {
	return &Sender{store: store, vapid: vapid, client: &http.Client{
		Timeout:       sendTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (o Options) check() error {
	switch o.Urgency {
	case "", "normal", "high":
	default:
		return errors.New("webpush: urgency must be normal or high")
	}
	if len(o.Topic) > 32 {
		return errors.New("webpush: topic is over 32 characters")
	}
	for _, c := range []byte(o.Topic) {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return errors.New("webpush: topic must use the base64url alphabet")
		}
	}
	if o.TTL < 0 {
		return errors.New("webpush: TTL is negative")
	}
	return nil
}

// Send encrypts payload for the device and posts it.
func (s *Sender) Send(ctx context.Context, deviceID string, payload []byte, opts Options) error {
	e, ok, err := s.store.Get(deviceID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	return s.send(ctx, e, payload, opts)
}

// SendAll sends to every subscription. payload picks the bytes for each one,
// so it can honor Entry.Names. The result list has one item per subscription,
// sorted by device ID. A store read failure returns one result with no device ID.
func (s *Sender) SendAll(ctx context.Context, payload func(Entry) []byte, opts Options) []Result {
	entries, err := s.store.List()
	if err != nil {
		return []Result{{Err: err}}
	}
	res := make([]Result, len(entries))
	sem := make(chan struct{}, sendWorkers)
	var wg sync.WaitGroup
	for i, e := range entries {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			body := payload(e)
			if body == nil {
				res[i] = Result{DeviceID: e.DeviceID}
				return
			}
			res[i] = Result{DeviceID: e.DeviceID, Err: s.send(ctx, e, body, opts)}
		}()
	}
	wg.Wait()
	return res
}

func (s *Sender) send(ctx context.Context, e Entry, payload []byte, opts Options) error {
	if err := opts.check(); err != nil {
		return err
	}
	if len(payload) > MaxPayload {
		return errors.New("webpush: payload is over 3000 bytes")
	}
	if err := s.store.allow(e.Endpoint); err != nil {
		return err
	}
	u, err := url.Parse(e.Endpoint)
	if err != nil {
		return errors.New("webpush: bad endpoint")
	}
	host := u.Hostname()
	pub, auth, err := parseKeys(e.P256dh, e.Auth)
	if err != nil {
		return err
	}
	body, err := encryptFresh(payload, pub, auth)
	if err != nil {
		return err
	}
	authz, err := s.vapid.Authorization(e.Endpoint)
	if err != nil {
		return err
	}
	ttl := opts.TTL
	if ttl == 0 {
		ttl = defaultTTL
	}
	urgency := opts.Urgency
	if urgency == "" {
		urgency = "normal"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.Endpoint, bytes.NewReader(body))
	if err != nil {
		return errors.New("webpush: bad endpoint")
	}
	req.Header.Set("Authorization", authz)
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("TTL", strconv.FormatInt(int64(ttl/time.Second), 10))
	req.Header.Set("Urgency", urgency)
	if opts.Topic != "" {
		req.Header.Set("Topic", opts.Topic)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		// url.Error holds the full URL; keep the host only.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("webpush: post to %s: %w", host, err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		if err := s.store.remove(e.DeviceID, e.Endpoint); err != nil {
			return err
		}
		return fmt.Errorf("%w (%s answered HTTP %d)", ErrGone, host, resp.StatusCode)
	}
	return &StatusError{Host: host, Code: resp.StatusCode}
}
