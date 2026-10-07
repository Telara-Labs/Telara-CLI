package api

import "context"

// MyPolicy is one of the caller's own policies (GET /v1/cli/policies).
type MyPolicy struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Active      bool   `json:"active"`
}

// MyPoliciesResponse lists the caller's own policies; ActivePolicyID is empty
// when they run on the Default.
type MyPoliciesResponse struct {
	Policies       []MyPolicy `json:"policies"`
	ActivePolicyID string     `json:"active_policy_id"`
}

// ListMyPolicies returns the caller's own policies and which one is active.
func (c *Client) ListMyPolicies(ctx context.Context) (*MyPoliciesResponse, error) {
	var resp MyPoliciesResponse
	if err := c.do(ctx, "GET", "/v1/cli/policies", nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// UseMyPolicy makes one of the caller's own policies (by name or id) govern
// every session they open.
func (c *Client) UseMyPolicy(ctx context.Context, nameOrID string) (string, error) {
	var resp struct {
		Name string `json:"name"`
	}
	if err := c.do(ctx, "PUT", "/v1/cli/policies/active", map[string]string{"policy": nameOrID}, &resp); err != nil {
		return "", err
	}
	return resp.Name, nil
}

// UseDefaultPolicy clears the caller's pick: back to the Default.
func (c *Client) UseDefaultPolicy(ctx context.Context) error {
	return c.do(ctx, "DELETE", "/v1/cli/policies/active", nil, nil)
}
