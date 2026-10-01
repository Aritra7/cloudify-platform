package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Migration struct {
	ID           string      `json:"id"`
	Status       string      `json:"status"`
	Source       Source      `json:"source"`
	Destination  Destination `json:"destination"`
	CreatedAt    time.Time   `json:"created_at"`
	UpdatedAt    time.Time   `json:"updated_at"`
	AttemptCount int64       `json:"attempt_count"`
}

type Source struct {
	RepositoryURL string `json:"repository_url"`
	Revision      string `json:"revision"`
}

type Destination struct {
	Provider  string `json:"provider"`
	ProjectID string `json:"project_id"`
	Region    string `json:"region"`
	Runtime   string `json:"runtime"`
	Database  string `json:"database,omitempty"`
}

type CreateMigrationRequest struct {
	Source      Source      `json:"source"`
	Destination Destination `json:"destination"`
}

type ManagedResource struct {
	ID                  string          `json:"id"`
	Kind                string          `json:"kind"`
	MigrationID         string          `json:"migration_id"`
	SourcePlanID        string          `json:"source_plan_id"`
	ProjectID           string          `json:"project_id"`
	Region              string          `json:"region"`
	Name                string          `json:"name"`
	Desired             json.RawMessage `json:"desired"`
	Observed            json.RawMessage `json:"observed"`
	State               string          `json:"state"`
	RemediationPolicy   string          `json:"remediation_policy"`
	Lifecycle           string          `json:"lifecycle"`
	Generation          int64           `json:"generation"`
	ObservedGeneration  int64           `json:"observed_generation"`
	RetryCount          int64           `json:"retry_count"`
	DeleteFailure       string          `json:"delete_failure"`
	DeletionRequestedBy string          `json:"deletion_requested_by"`
	DeletionRequestedAt *time.Time      `json:"deletion_requested_at"`
	DeletedAt           *time.Time      `json:"deleted_at"`
	CreatedAt           time.Time       `json:"created_at"`
	UpdatedAt           time.Time       `json:"updated_at"`
}

type APIError struct {
	StatusCode int
	Code       string
	Message    string
}

func (err *APIError) Error() string {
	return fmt.Sprintf("cloudify API returned %d %s: %s", err.StatusCode, err.Code, err.Message)
}

type Client struct {
	endpoint     *url.URL
	token        string
	httpClient   *http.Client
	pollInterval time.Duration
	userAgent    string
}

func NewClient(endpoint, token, userAgent string, httpClient *http.Client) (*Client, error) {
	parsed, err := url.Parse(strings.TrimRight(endpoint, "/"))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("endpoint must be an absolute URL without embedded credentials")
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && isLoopback(parsed.Hostname())) {
		return nil, errors.New("endpoint must use HTTPS unless it targets loopback")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{
		endpoint: parsed, token: token, httpClient: httpClient,
		pollInterval: 2 * time.Second, userAgent: userAgent,
	}, nil
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (client *Client) CreateMigration(
	ctx context.Context, request CreateMigrationRequest, idempotencyKey string,
) (Migration, error) {
	var migration Migration
	err := client.do(ctx, http.MethodPost, "/v1/migrations", request, map[string]string{
		"Idempotency-Key": idempotencyKey,
	}, &migration)
	return migration, err
}

func (client *Client) GetMigration(ctx context.Context, id string) (Migration, error) {
	var migration Migration
	err := client.do(ctx, http.MethodGet, "/v1/migrations/"+url.PathEscape(id), nil, nil, &migration)
	return migration, err
}

func (client *Client) CancelMigration(ctx context.Context, id string) (Migration, error) {
	var migration Migration
	err := client.do(ctx, http.MethodPost, "/v1/migrations/"+url.PathEscape(id)+"/cancel", nil, nil, &migration)
	return migration, err
}

func (client *Client) GetResource(ctx context.Context, id string) (ManagedResource, error) {
	var resource ManagedResource
	err := client.do(ctx, http.MethodGet, "/v1/resources/"+url.PathEscape(id), nil, nil, &resource)
	return resource, err
}

func (client *Client) DeleteResource(ctx context.Context, id string) (ManagedResource, error) {
	var resource ManagedResource
	err := client.do(ctx, http.MethodDelete, "/v1/resources/"+url.PathEscape(id), nil, nil, &resource)
	return resource, err
}

func (client *Client) WaitResourceDeleted(ctx context.Context, id string) (ManagedResource, error) {
	ticker := time.NewTicker(client.pollInterval)
	defer ticker.Stop()
	for {
		resource, err := client.GetResource(ctx, id)
		if err != nil {
			return ManagedResource{}, err
		}
		if resource.Lifecycle == "deleted" {
			return resource, nil
		}
		select {
		case <-ctx.Done():
			return ManagedResource{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (client *Client) WaitMigration(ctx context.Context, id string) (Migration, error) {
	ticker := time.NewTicker(client.pollInterval)
	defer ticker.Stop()
	for {
		migration, err := client.GetMigration(ctx, id)
		if err != nil {
			return Migration{}, err
		}
		switch migration.Status {
		case "succeeded", "failed", "cancelled":
			return migration, nil
		}
		select {
		case <-ctx.Done():
			return Migration{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (client *Client) do(
	ctx context.Context,
	method, path string,
	body any,
	headers map[string]string,
	destination any,
) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}
	requestURL := *client.endpoint
	requestURL.Path = strings.TrimRight(requestURL.Path, "/") + path
	request, err := http.NewRequestWithContext(ctx, method, requestURL.String(), reader)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if client.token != "" {
		request.Header.Set("Authorization", "Bearer "+client.token)
	}
	if client.userAgent != "" {
		request.Header.Set("User-Agent", client.userAgent)
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := client.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("call Cloudify API: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var payload struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		limited := io.LimitReader(response.Body, 1<<20)
		if err := json.NewDecoder(limited).Decode(&payload); err != nil {
			return &APIError{StatusCode: response.StatusCode, Code: "unexpected_response", Message: response.Status}
		}
		return &APIError{StatusCode: response.StatusCode, Code: payload.Error.Code, Message: payload.Error.Message}
	}
	if destination == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(destination); err != nil {
		return fmt.Errorf("decode Cloudify API response: %w", err)
	}
	return nil
}
