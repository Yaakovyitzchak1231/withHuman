package webhookdelivery

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

const (
	UserAgent            = "withHuman-webhooks/1"
	MaximumResponseBytes = 64 * 1024
	DefaultTimeout       = 5 * time.Second
	dialTimeout          = 10 * time.Second
)

// ErrResponseTooLarge means the endpoint answered with more than the
// response limit; the answer is discarded unread.
var ErrResponseTooLarge = errors.New("webhook response exceeds the size limit")

// Delivery is one signed POST. Timeout bounds the attempt from connect to
// the full response.
type Delivery struct {
	URL            string
	Secret         string
	Body           []byte
	Timeout        time.Duration
	DeliveryID     string
	IdempotencyKey string
}

// Response is what came back. A transport failure returns an error and a
// Response carrying only the duration.
type Response struct {
	StatusCode int
	Body       []byte
	Duration   time.Duration
}

type resolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

// Client delivers signed payloads under the egress policy. Destinations
// are resolved before dialing, every resolved address must pass the
// policy, and the validated address is dialed directly so a DNS answer
// cannot change between check and connect. When the environment names a
// proxy for the destination, the proxy makes the egress and pinning does
// not apply; the static URL policy still does.
type Client struct {
	policy   Policy
	pinned   *http.Transport
	proxied  *http.Transport
	resolver resolver
	now      func() time.Time
}

func NewClient(policy Policy) *Client {
	client := &Client{policy: policy, resolver: net.DefaultResolver, now: time.Now}
	dialer := &net.Dialer{Timeout: dialTimeout}
	client.pinned = &http.Transport{
		Proxy: nil, DialContext: client.dialPinned(dialer),
		TLSHandshakeTimeout: dialTimeout, ForceAttemptHTTP2: true,
		MaxIdleConns: 16, IdleConnTimeout: time.Minute,
	}
	client.proxied = &http.Transport{
		Proxy: http.ProxyFromEnvironment, DialContext: dialer.DialContext,
		TLSHandshakeTimeout: dialTimeout, ForceAttemptHTTP2: true,
		MaxIdleConns: 16, IdleConnTimeout: time.Minute,
	}
	return client
}

func (c *Client) dialPinned(dialer *net.Dialer) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		var candidates []net.IP
		if ip := net.ParseIP(host); ip != nil {
			candidates = []net.IP{ip}
		} else {
			resolved, err := c.resolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, err
			}
			for _, entry := range resolved {
				candidates = append(candidates, entry.IP)
			}
		}
		if len(candidates) == 0 {
			return nil, fmt.Errorf("%s resolved to no addresses", host)
		}
		for _, ip := range candidates {
			if !c.policy.AllowIP(ip) {
				return nil, fmt.Errorf("%s resolves to a blocked address: %w", host, ErrDestinationBlocked)
			}
		}
		var lastErr error
		for _, ip := range candidates {
			conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return conn, nil
			}
			lastErr = err
		}
		return nil, lastErr
	}
}

// Deliver posts the body once. Redirects are not followed: a 3xx comes
// back as the response. The body is read through the response limit.
func (c *Client) Deliver(ctx context.Context, delivery Delivery) (Response, error) {
	target, err := c.policy.ValidateURL(delivery.URL)
	if err != nil {
		return Response{}, err
	}
	timeout := delivery.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	attemptContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(attemptContext, http.MethodPost, target.String(), bytes.NewReader(delivery.Body))
	if err != nil {
		return Response{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", UserAgent)
	request.Header.Set(SignatureHeader, Sign(delivery.Secret, c.now().UTC(), delivery.Body))
	if delivery.DeliveryID != "" {
		request.Header.Set(DeliveryIDHeader, delivery.DeliveryID)
	}
	if delivery.IdempotencyKey != "" {
		request.Header.Set(IdempotencyKeyHeader, delivery.IdempotencyKey)
	}
	transport := c.pinned
	if proxyURL, proxyErr := http.ProxyFromEnvironment(request); proxyErr == nil && proxyURL != nil {
		transport = c.proxied
	}
	httpClient := &http.Client{
		Transport:     transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	started := time.Now()
	response, err := httpClient.Do(request)
	if err != nil {
		return Response{Duration: time.Since(started)}, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, MaximumResponseBytes+1))
	duration := time.Since(started)
	if err != nil {
		return Response{StatusCode: response.StatusCode, Duration: duration}, err
	}
	if len(body) > MaximumResponseBytes {
		return Response{StatusCode: response.StatusCode, Duration: duration}, ErrResponseTooLarge
	}
	return Response{StatusCode: response.StatusCode, Body: body, Duration: duration}, nil
}

// IsTimeout reports whether a delivery error was the attempt deadline.
func IsTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}
