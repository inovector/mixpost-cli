package mixpost

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// Call is the shorthand used by most commands.
func (c *Client) Call(ctx context.Context, method, path string, query url.Values, body any) (*Response, error) {
	return c.Do(ctx, Request{Method: method, Path: path, Query: query, Body: body})
}

// Get performs a GET on a workspace-scoped or instance-level path.
func (c *Client) Get(ctx context.Context, path string, query url.Values) (*Response, error) {
	return c.Call(ctx, http.MethodGet, path, query, nil)
}

// Page is one page of a Laravel paginated collection.
type Page struct {
	Data  []json.RawMessage `json:"data"`
	Links struct {
		Next string `json:"next"`
	} `json:"links"`
	Meta json.RawMessage `json:"meta"`
}

// Paginate walks every page of a collection, following links.next, and returns
// a single {"data": [...], "meta": {...}} document. A limit of 0 means "all".
func (c *Client) Paginate(ctx context.Context, path string, query url.Values, limit int) ([]byte, error) {
	var (
		all       []json.RawMessage
		firstMeta json.RawMessage
		pages     int
		next      string
	)

	for {
		var req Request
		if next == "" {
			req = Request{Method: http.MethodGet, Path: path, Query: query}
		} else {
			req = Request{Method: http.MethodGet, AbsoluteURL: next}
		}

		resp, err := c.Do(ctx, req)
		if err != nil {
			return nil, err
		}

		var page Page
		if err := json.Unmarshal(resp.Body, &page); err != nil {
			// Not a paginated collection; hand the payload back untouched.
			if pages == 0 {
				return resp.Body, nil
			}
			return nil, fmt.Errorf("decode page %d: %w", pages+1, err)
		}
		pages++
		if firstMeta == nil {
			firstMeta = page.Meta
		}

		for _, item := range page.Data {
			all = append(all, item)
			if limit > 0 && len(all) >= limit {
				return buildCollection(all, firstMeta, pages)
			}
		}

		if page.Links.Next == "" {
			break
		}
		next = page.Links.Next
	}

	return buildCollection(all, firstMeta, pages)
}

func buildCollection(items []json.RawMessage, firstMeta json.RawMessage, pages int) ([]byte, error) {
	if items == nil {
		items = []json.RawMessage{}
	}
	meta := map[string]any{"fetched": len(items), "pages": pages}
	if len(firstMeta) > 0 {
		var m map[string]any
		if err := json.Unmarshal(firstMeta, &m); err == nil {
			if total, ok := m["total"]; ok {
				meta["total"] = total
			}
		}
	}
	return json.Marshal(map[string]any{"data": items, "meta": meta})
}

// DecodeInto unmarshals a response body into v.
func (r *Response) DecodeInto(v any) error {
	if len(r.Body) == 0 {
		return nil
	}
	return json.Unmarshal(r.Body, v)
}

// Account is the subset of an account the CLI resolves names against.
type Account struct {
	ID       int    `json:"id"`
	UUID     string `json:"uuid"`
	Name     string `json:"name"`
	Username string `json:"username"`
	Provider string `json:"provider"`
}

// Accounts lists the social accounts in the current workspace.
func (c *Client) Accounts(ctx context.Context) ([]Account, error) {
	resp, err := c.Get(ctx, "accounts", nil)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Data []Account `json:"data"`
	}
	if err := resp.DecodeInto(&payload); err != nil {
		return nil, fmt.Errorf("decode accounts: %w", err)
	}
	return payload.Data, nil
}

// Tag is the subset of a tag the CLI resolves names against.
type Tag struct {
	ID       int    `json:"id"`
	UUID     string `json:"uuid"`
	Name     string `json:"name"`
	HexColor string `json:"hex_color"`
}

// Tags lists the tags in the current workspace.
func (c *Client) Tags(ctx context.Context) ([]Tag, error) {
	resp, err := c.Get(ctx, "tags", nil)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Data []Tag `json:"data"`
	}
	if err := resp.DecodeInto(&payload); err != nil {
		return nil, fmt.Errorf("decode tags: %w", err)
	}
	return payload.Data, nil
}

// CreateTag creates a tag and returns it.
func (c *Client) CreateTag(ctx context.Context, name, hexColor string) (*Tag, error) {
	resp, err := c.Call(ctx, http.MethodPost, "tags", nil, map[string]any{
		"name": name, "hex_color": hexColor,
	})
	if err != nil {
		return nil, err
	}
	var tag Tag
	if err := resp.DecodeInto(&tag); err != nil {
		return nil, fmt.Errorf("decode tag: %w", err)
	}
	return &tag, nil
}

// Workspace is a workspace the token's owner belongs to, as returned by
// GET /workspaces.
type Workspace struct {
	ID    int    `json:"id"`
	UUID  string `json:"uuid"`
	Name  string `json:"name"`
	Pivot struct {
		Role       string `json:"role"`
		CanApprove bool   `json:"can_approve"`
		JoinedAt   string `json:"joined_at"`
	} `json:"pivot"`
}

// Workspaces lists the workspaces the token can reach. The endpoint is
// instance-level, so it works before any workspace has been chosen.
func (c *Client) Workspaces(ctx context.Context) ([]Workspace, error) {
	resp, err := c.Do(ctx, Request{Method: http.MethodGet, Path: "workspaces", Instance: true})
	if err != nil {
		return nil, err
	}
	var payload struct {
		Data []Workspace `json:"data"`
	}
	if err := resp.DecodeInto(&payload); err != nil {
		return nil, fmt.Errorf("decode workspaces: %w", err)
	}
	return payload.Data, nil
}
