package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

type Criterion struct {
	ID       string `json:"id"`
	Text     string `json:"text"`
	Required bool   `json:"required"`
}

type CriterionResult struct {
	ID     string `json:"id"`
	Passed bool   `json:"passed"`
	Reason string `json:"reason"`
}

type GradeResult struct {
	Verdict           string            `json:"verdict"`
	CriteriaResults   []CriterionResult `json:"criteria_results"`
	Reasoning         string            `json:"reasoning"`
	Feedback          string            `json:"feedback"`
	InjectionDetected bool              `json:"injection_detected"`
}

type Client struct {
	base string
	key  string
	http *http.Client
}

func New(base, internalKey string) *Client {
	// No client-level timeout: callers pass a context deadline (grading can be slow on free tiers).
	return &Client{base: base, key: internalKey, http: &http.Client{}}
}

func (c *Client) Rubric(ctx context.Context, title, description string) ([]Criterion, error) {
	var out struct {
		Criteria []Criterion `json:"criteria"`
	}
	in := map[string]string{"title": title, "description": description}
	if err := c.post(ctx, "/rubric", in, &out); err != nil {
		return nil, err
	}
	if len(out.Criteria) == 0 {
		return nil, fmt.Errorf("ai service returned an empty rubric")
	}
	return out.Criteria, nil
}

func (c *Client) Grade(ctx context.Context, description string, criteria []Criterion, submission, submissionType string) (*GradeResult, error) {
	in := map[string]any{
		"description":     description,
		"criteria":        criteria,
		"submission":      submission,
		"submission_type": submissionType,
	}
	var out GradeResult
	if err := c.post(ctx, "/grade", in, &out); err != nil {
		return nil, err
	}
	if out.Verdict != "PASS" && out.Verdict != "FAIL" {
		return nil, fmt.Errorf("ai service returned invalid verdict %q", out.Verdict)
	}
	return &out, nil
}

func (c *Client) post(ctx context.Context, path string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Key", c.key)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("ai service unreachable: %w", err)
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		msg := string(data)
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return fmt.Errorf("ai service %s returned %d: %s", path, resp.StatusCode, msg)
	}
	return json.Unmarshal(data, out)
}
