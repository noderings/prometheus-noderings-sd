// Package noderings is a small read-only client for the NodeRings API: it
// lists the virtual machines of an organization.
package noderings

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultEndpoint is the public NodeRings API.
const DefaultEndpoint = "https://api.noderings.com"

// Config configures a Client.
type Config struct {
	// Endpoint is the API base URL. Empty means DefaultEndpoint.
	Endpoint string
	// Token is a NodeRings API token (a service account token is recommended).
	Token string
	// OrganizationID, when set, is sent as X-Organization-ID with every request.
	OrganizationID string
	// UserAgent identifies the caller.
	UserAgent string
	// MaxRetries is how many times a failed request is retried. Zero means 3.
	MaxRetries int
	// MinBackoff is the first retry delay. Zero means 500 ms.
	MinBackoff time.Duration
	// HTTPClient overrides the HTTP client (tests).
	HTTPClient *http.Client
	// Logger receives one debug line per request. Nil discards them.
	Logger *slog.Logger
}

// Client calls the NodeRings API. It is safe for concurrent use.
type Client struct {
	base       *url.URL
	token      string
	org        string
	userAgent  string
	maxRetries int
	minBackoff time.Duration
	http       *http.Client
	log        *slog.Logger
}

// New returns a Client.
func New(cfg Config) (*Client, error) {
	endpoint := strings.TrimSpace(cfg.Endpoint)
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}
	base, err := url.Parse(strings.TrimRight(endpoint, "/"))
	if err != nil || base.Scheme == "" || base.Host == "" {
		return nil, fmt.Errorf("invalid NodeRings API URL %q: must be an absolute URL such as %s", cfg.Endpoint, DefaultEndpoint)
	}
	if strings.TrimSpace(cfg.Token) == "" {
		return nil, fmt.Errorf("a NodeRings API token is required")
	}
	c := &Client{
		base:       base,
		token:      strings.TrimSpace(cfg.Token),
		org:        strings.TrimSpace(cfg.OrganizationID),
		userAgent:  cfg.UserAgent,
		maxRetries: cfg.MaxRetries,
		minBackoff: cfg.MinBackoff,
		http:       cfg.HTTPClient,
		log:        cfg.Logger,
	}
	if c.http == nil {
		c.http = &http.Client{Timeout: 60 * time.Second}
	}
	if c.maxRetries <= 0 {
		c.maxRetries = 3
	}
	if c.minBackoff <= 0 {
		c.minBackoff = 500 * time.Millisecond
	}
	if c.log == nil {
		c.log = slog.New(slog.DiscardHandler)
	}
	return c, nil
}

// VirtualMachine is the part of a NodeRings virtual machine that discovery uses.
type VirtualMachine struct {
	ID                          string         `json:"id"`
	Hostname                    string         `json:"hostname"`
	OperationStatus             string         `json:"operationStatus"`
	CPU                         Int64          `json:"cpu"`
	Memory                      Int64          `json:"memory"`  // MiB
	Storage                     Int64          `json:"storage"` // MiB
	ProviderOrganizationID      string         `json:"providerOrganizationId"`
	ProviderName                string         `json:"providerName"`
	IPv4                        string         `json:"ipv4"`
	IPv6                        string         `json:"ipv6"`
	AdditionalIPv4              []AdditionalIP `json:"additionalIpv4"`
	Distro                      string         `json:"distro"`
	RegionID                    string         `json:"regionId"`
	RegionCountry               string         `json:"regionCountry"`
	RegionCity                  string         `json:"regionCity"`
	PlanID                      string         `json:"planId"`
	PlanCode                    string         `json:"planCode"`
	PlanBillingType             string         `json:"planBillingType"`
	Tags                        []string       `json:"tags"`
	FirewallRulesetID           string         `json:"firewallRulesetId"`
	HasActiveBackupSubscription bool           `json:"hasActiveBackupSubscription"`
	InstantAppSlug              string         `json:"instantAppSlug"`
}

// AdditionalIP is an extra IPv4 address of a VM.
type AdditionalIP struct {
	IPAddress string `json:"ipAddress"`
}

// ListVirtualMachines returns every VM in the organization.
func (c *Client) ListVirtualMachines(ctx context.Context) ([]VirtualMachine, error) {
	var out struct {
		VirtualMachines []VirtualMachine `json:"virtualMachines"`
	}
	err := c.get(ctx, "/v1/virtual-machines", url.Values{"page": {"0"}, "pageSize": {"-1"}}, &out)
	return out.VirtualMachines, err
}

func (c *Client) get(ctx context.Context, path string, query url.Values, out any) error {
	u := *c.base
	u.Path = strings.TrimRight(c.base.Path, "/") + path
	u.RawQuery = query.Encode()

	var lastErr error
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), http.NoBody)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set("Accept", "application/json")
		if c.org != "" {
			req.Header.Set("X-Organization-ID", c.org)
		}
		if c.userAgent != "" {
			req.Header.Set("User-Agent", c.userAgent)
		}
		started := time.Now()
		resp, err := c.http.Do(req)
		if err != nil {
			c.log.DebugContext(ctx, "NodeRings API request failed", "path", path, "err", err)
			lastErr = err
			if ctx.Err() != nil || attempt >= c.maxRetries {
				return lastErr
			}
		} else {
			body, readErr := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
			_ = resp.Body.Close()
			if readErr != nil {
				return fmt.Errorf("reading response: %w", readErr)
			}
			c.log.DebugContext(ctx, "NodeRings API request", "path", path,
				"status", resp.StatusCode, "duration_ms", time.Since(started).Milliseconds())
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				if err := json.Unmarshal(bytes.TrimSpace(body), out); err != nil {
					return fmt.Errorf("decoding GET %s response: %w", path, err)
				}
				return nil
			}
			apiErr := decodeError(resp.StatusCode, body)
			lastErr = apiErr
			if !retryable(apiErr) || attempt >= c.maxRetries {
				return apiErr
			}
		}
		if err := sleep(ctx, c.backoff(attempt)); err != nil {
			return lastErr
		}
	}
}

// retryable reports whether a read should be retried: rate limits (but not
// exhausted quotas) and gateway or availability errors.
func retryable(e *Error) bool {
	switch e.HTTPStatus {
	case http.StatusTooManyRequests:
		return !e.IsQuota()
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

func (c *Client) backoff(attempt int) time.Duration {
	const maxBackoff = 10 * time.Second
	d := c.minBackoff << min(attempt, 10)
	if d > maxBackoff || d <= 0 {
		d = maxBackoff
	}
	return d/2 + time.Duration(rand.Int64N(int64(d)/2+1))
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Int64 decodes an integer sent as a JSON number or as a string.
type Int64 int64

// UnmarshalJSON implements json.Unmarshaler.
func (i *Int64) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "" || s == "null" {
		*i = 0
		return nil
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return err
	}
	*i = Int64(v)
	return nil
}

// GameServer is the part of a NodeRings game server that discovery uses.
type GameServer struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	GameName        string `json:"gameName"`
	Variant         string `json:"variant"`
	VersionValue    string `json:"versionValue"`
	AllocationIP    string `json:"allocationIp"`
	AllocationPort  Int64  `json:"allocationPort"`
	Phase           string `json:"phase"`
	PowerState      string `json:"powerState"`
	NodeID          string `json:"nodeId"`
	RegionID        string `json:"regionId"`
	RegionCity      string `json:"regionCity"`
	RegionCountry   string `json:"regionCountry"`
	PlanID          string `json:"planId"`
	PlanCode        string `json:"planCode"`
	PlanBillingType string `json:"planBillingType"`
	ProviderName    string `json:"providerName"`
}

// ListGameServers returns every game server in the organization.
func (c *Client) ListGameServers(ctx context.Context) ([]GameServer, error) {
	var out struct {
		GameServers []GameServer `json:"gameServers"`
	}
	err := c.get(ctx, "/v1/game-servers", url.Values{"pageSize": {"-1"}}, &out)
	return out.GameServers, err
}

// DNSZone is a hosted DNS zone.
type DNSZone struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	State        string `json:"state"`
	DelegationOK bool   `json:"delegationOk"`
}

// DNSRecord is one record set. Name is relative to the zone, "@" for the apex.
type DNSRecord struct {
	Name   string   `json:"name"`
	Type   string   `json:"type"`
	TTL    Int64    `json:"ttl"`
	RData  []string `json:"rdata"`
	System bool     `json:"system"`
}

// ListDNSZones returns every DNS zone in the organization.
func (c *Client) ListDNSZones(ctx context.Context) ([]DNSZone, error) {
	var out struct {
		Zones []DNSZone `json:"zones"`
	}
	err := c.get(ctx, "/v1/dns/zones", url.Values{"page": {"0"}, "pageSize": {"-1"}}, &out)
	return out.Zones, err
}

// ListDNSRecords returns every record set of a zone.
func (c *Client) ListDNSRecords(ctx context.Context, zoneID string) ([]DNSRecord, error) {
	const pageSize = 100
	var all []DNSRecord
	for page := 0; ; page++ {
		var out struct {
			Records    []DNSRecord `json:"records"`
			TotalCount Int64       `json:"totalCount"`
		}
		q := url.Values{"page": {strconv.Itoa(page)}, "pageSize": {strconv.Itoa(pageSize)}}
		if err := c.get(ctx, "/v1/dns/zones/"+url.PathEscape(zoneID)+"/records", q, &out); err != nil {
			return nil, err
		}
		all = append(all, out.Records...)
		if len(out.Records) < pageSize || len(all) >= int(out.TotalCount) {
			return all, nil
		}
	}
}
