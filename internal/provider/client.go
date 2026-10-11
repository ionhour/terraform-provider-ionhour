package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"time"

	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/ionhour/go-sdk/api"
)

const (
	// createAttempts bounds how often a create is re-sent with the same
	// Idempotency-Key while the server reports IDEMPOTENCY_KEY_IN_PROGRESS.
	createAttempts = 3
	// inProgressWait is the pause before re-sending such a create.
	inProgressWait = 2 * time.Second

	// defaultRateLimitBudget is the total time one API call may spend waiting
	// out 429s. A batch apply on a 60 requests/minute plan with parallel
	// resources hits the limit repeatedly; five minutes rides that out
	// without hanging a run forever.
	defaultRateLimitBudget = 5 * time.Minute
	// defaultRetryAfter is used when a 429 carries no Retry-After.
	defaultRetryAfter = time.Second
	// maxJitter spreads the retries of parallel resources so they do not
	// all return in the same second.
	maxJitter = time.Second
)

// apiClient is the provider's view of the Integration API: typed calls that
// return the decoded representation or an error (an *api.APIError for any
// non-2xx response). Every call rides out 429s within rateLimitBudget.
type apiClient struct {
	api *api.Client
	// The fields below are overridable in tests.
	sleep           func(ctx context.Context, d time.Duration) error
	jitter          func() time.Duration
	inProgressWait  time.Duration
	rateLimitBudget time.Duration
}

func newAPIClient(c *api.Client) *apiClient {
	return &apiClient{
		api:             c,
		sleep:           sleepContext,
		jitter:          defaultJitter,
		inProgressWait:  inProgressWait,
		rateLimitBudget: defaultRateLimitBudget,
	}
}

func defaultJitter() time.Duration { return rand.N(maxJitter) }

func sleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// call runs one API operation. The Go client already waits out one 429; when
// the API is still rate limiting, call waits Retry-After plus jitter and
// tries again until rateLimitBudget is spent or ctx ends. A 429 is rejected
// before any handler runs, so repeating any method (including a keyed
// create) is safe.
func (c *apiClient) call(ctx context.Context, op func(ctx context.Context) error) error {
	var waited time.Duration
	for {
		err := op(ctx)
		var apiErr *api.APIError
		if !errors.As(err, &apiErr) || apiErr.Status != http.StatusTooManyRequests {
			return err
		}
		wait := apiErr.RetryAfter
		if wait <= 0 {
			wait = defaultRetryAfter
		}
		wait += c.jitter()
		if waited+wait > c.rateLimitBudget {
			return err
		}
		tflog.Debug(ctx, "Ionhour API rate limited; waiting before retrying", map[string]any{
			"wait": wait.String(), "waited": waited.String(), "code": apiErr.Code,
		})
		if sleepErr := c.sleep(ctx, wait); sleepErr != nil {
			return sleepErr
		}
		waited += wait
	}
}

// do sends one request through call and decodes its typed body. send returns
// the generated response's parts.
func do[T any](ctx context.Context, c *apiClient, send func(ctx context.Context) (*http.Response, []byte, *T, error)) (*T, error) {
	var out *T
	err := c.call(ctx, func(ctx context.Context) error {
		rsp, body, typed, err := send(ctx)
		if err != nil {
			return err
		}
		out, err = decode(c.api, rsp, body, typed)
		return err
	})
	return out, err
}

// doNoContent is do for 204 responses.
func (c *apiClient) doNoContent(ctx context.Context, send func(ctx context.Context) (*http.Response, []byte, error)) error {
	return c.call(ctx, func(ctx context.Context) error {
		rsp, body, err := send(ctx)
		if err != nil {
			return err
		}
		return c.api.CheckResponse(rsp, body)
	})
}

// decode turns a generated response into its typed body. The generated
// client only fills the typed field for JSON content types, so the raw body
// is decoded as a fallback.
func decode[T any](c *api.Client, rsp *http.Response, body []byte, typed *T) (*T, error) {
	if err := c.CheckResponse(rsp, body); err != nil {
		return nil, err
	}
	if typed != nil {
		return typed, nil
	}
	var out T
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("ionhour api: decode response: %w", err)
	}
	return &out, nil
}

// idempotentCreate mints one Idempotency-Key for a logical create and sends
// every attempt with it. The client already retries 5xx/network failures for
// keyed POSTs and call rides out 429s; this loop adds the
// IDEMPOTENCY_KEY_IN_PROGRESS case, where the first attempt is still running
// server-side.
func idempotentCreate[T any](ctx context.Context, c *apiClient, send func(ctx context.Context) (*http.Response, []byte, *T, error)) (*T, error) {
	key, err := api.NewIdempotencyKey()
	if err != nil {
		return nil, err
	}
	ctx = api.WithIdempotencyKey(ctx, key)
	for attempt := 1; ; attempt++ {
		out, err := do(ctx, c, send)
		if attempt >= createAttempts || !hasCode(err, codeIdempotencyInProgress) {
			return out, err
		}
		tflog.Debug(ctx, "create still in progress server-side; re-sending with the same idempotency key",
			map[string]any{"attempt": attempt})
		if sleepErr := c.sleep(ctx, c.inProgressWait); sleepErr != nil {
			return nil, sleepErr
		}
	}
}

func hasCode(err error, code string) bool {
	var apiErr *api.APIError
	return errors.As(err, &apiErr) && apiErr.Code == code
}

// ---- whoami ----

func (c *apiClient) whoami(ctx context.Context) (*api.WhoamiV1Response, error) {
	var out *api.WhoamiV1Response
	err := c.call(ctx, func(ctx context.Context) error {
		var err error
		out, err = c.api.Whoami(ctx)
		return err
	})
	return out, err
}

// ---- projects ----

func (c *apiClient) createProject(ctx context.Context, body api.CreateProjectV1Dto) (*api.ProjectV1Response, error) {
	return idempotentCreate(ctx, c, func(ctx context.Context) (*http.Response, []byte, *api.ProjectV1Response, error) {
		rsp, err := c.api.ProjectsCreateWithResponse(ctx, &api.ProjectsCreateParams{}, body)
		if err != nil {
			return nil, nil, nil, err
		}
		return rsp.HTTPResponse, rsp.Body, rsp.JSON201, nil
	})
}

func (c *apiClient) getProject(ctx context.Context, ref string) (*api.ProjectV1Response, error) {
	return do(ctx, c, func(ctx context.Context) (*http.Response, []byte, *api.ProjectV1Response, error) {
		rsp, err := c.api.ProjectsGetOneWithResponse(ctx, ref)
		if err != nil {
			return nil, nil, nil, err
		}
		return rsp.HTTPResponse, rsp.Body, rsp.JSON200, nil
	})
}

func (c *apiClient) listProjectsByName(ctx context.Context, name string) ([]api.ProjectV1Response, error) {
	limit := 100
	page, err := do(ctx, c, func(ctx context.Context) (*http.Response, []byte, *api.ProjectV1Page, error) {
		rsp, err := c.api.ProjectsListWithResponse(ctx, &api.ProjectsListParams{Name: &name, Limit: &limit})
		if err != nil {
			return nil, nil, nil, err
		}
		return rsp.HTTPResponse, rsp.Body, rsp.JSON200, nil
	})
	if err != nil {
		return nil, err
	}
	// The filter is an exact match server-side; re-check so a looser server
	// can never hand back the wrong project.
	matches := make([]api.ProjectV1Response, 0, len(page.Items))
	for _, p := range page.Items {
		if p.Name == name {
			matches = append(matches, p)
		}
	}
	return matches, nil
}

func (c *apiClient) updateProject(ctx context.Context, ref string, body api.UpdateProjectV1Dto) (*api.ProjectV1Response, error) {
	return do(ctx, c, func(ctx context.Context) (*http.Response, []byte, *api.ProjectV1Response, error) {
		rsp, err := c.api.ProjectsUpdateWithResponse(ctx, ref, body)
		if err != nil {
			return nil, nil, nil, err
		}
		return rsp.HTTPResponse, rsp.Body, rsp.JSON200, nil
	})
}

func (c *apiClient) deleteProject(ctx context.Context, ref string) error {
	return c.doNoContent(ctx, func(ctx context.Context) (*http.Response, []byte, error) {
		rsp, err := c.api.ProjectsRemoveWithResponse(ctx, ref)
		if err != nil {
			return nil, nil, err
		}
		return rsp.HTTPResponse, rsp.Body, nil
	})
}

// ---- checks ----

func (c *apiClient) createCheck(ctx context.Context, body api.CreateCheckV1Dto) (*api.CheckV1Response, error) {
	return idempotentCreate(ctx, c, func(ctx context.Context) (*http.Response, []byte, *api.CheckV1Response, error) {
		rsp, err := c.api.ChecksCreateWithResponse(ctx, &api.ChecksCreateParams{}, body)
		if err != nil {
			return nil, nil, nil, err
		}
		return rsp.HTTPResponse, rsp.Body, rsp.JSON201, nil
	})
}

func (c *apiClient) getCheck(ctx context.Context, ref string) (*api.CheckV1Response, error) {
	return do(ctx, c, func(ctx context.Context) (*http.Response, []byte, *api.CheckV1Response, error) {
		rsp, err := c.api.ChecksGetOneWithResponse(ctx, ref)
		if err != nil {
			return nil, nil, nil, err
		}
		return rsp.HTTPResponse, rsp.Body, rsp.JSON200, nil
	})
}

func (c *apiClient) updateCheck(ctx context.Context, ref string, body api.UpdateCheckV1Dto) (*api.CheckV1Response, error) {
	return do(ctx, c, func(ctx context.Context) (*http.Response, []byte, *api.CheckV1Response, error) {
		rsp, err := c.api.ChecksUpdateWithResponse(ctx, ref, body)
		if err != nil {
			return nil, nil, nil, err
		}
		return rsp.HTTPResponse, rsp.Body, rsp.JSON200, nil
	})
}

func (c *apiClient) deleteCheck(ctx context.Context, ref string) error {
	return c.doNoContent(ctx, func(ctx context.Context) (*http.Response, []byte, error) {
		rsp, err := c.api.ChecksRemoveWithResponse(ctx, ref)
		if err != nil {
			return nil, nil, err
		}
		return rsp.HTTPResponse, rsp.Body, nil
	})
}

// ---- jobs ----

func (c *apiClient) createJob(ctx context.Context, body api.CreateJobV1Dto) (*api.JobV1Response, error) {
	return idempotentCreate(ctx, c, func(ctx context.Context) (*http.Response, []byte, *api.JobV1Response, error) {
		rsp, err := c.api.JobsCreateWithResponse(ctx, &api.JobsCreateParams{}, body)
		if err != nil {
			return nil, nil, nil, err
		}
		return rsp.HTTPResponse, rsp.Body, rsp.JSON201, nil
	})
}

func (c *apiClient) getJob(ctx context.Context, ref string) (*api.JobV1Response, error) {
	return do(ctx, c, func(ctx context.Context) (*http.Response, []byte, *api.JobV1Response, error) {
		rsp, err := c.api.JobsGetOneWithResponse(ctx, ref)
		if err != nil {
			return nil, nil, nil, err
		}
		return rsp.HTTPResponse, rsp.Body, rsp.JSON200, nil
	})
}

func (c *apiClient) updateJob(ctx context.Context, ref string, body api.UpdateJobV1Dto) (*api.JobV1Response, error) {
	return do(ctx, c, func(ctx context.Context) (*http.Response, []byte, *api.JobV1Response, error) {
		rsp, err := c.api.JobsUpdateWithResponse(ctx, ref, body)
		if err != nil {
			return nil, nil, nil, err
		}
		return rsp.HTTPResponse, rsp.Body, rsp.JSON200, nil
	})
}

func (c *apiClient) deleteJob(ctx context.Context, ref string) error {
	return c.doNoContent(ctx, func(ctx context.Context) (*http.Response, []byte, error) {
		rsp, err := c.api.JobsRemoveWithResponse(ctx, ref)
		if err != nil {
			return nil, nil, err
		}
		return rsp.HTTPResponse, rsp.Body, nil
	})
}

// ---- regions ----

func (c *apiClient) listRegions(ctx context.Context) ([]api.RegionV1Response, error) {
	out, err := do(ctx, c, func(ctx context.Context) (*http.Response, []byte, *[]api.RegionV1Response, error) {
		rsp, err := c.api.RegionsListWithResponse(ctx)
		if err != nil {
			return nil, nil, nil, err
		}
		return rsp.HTTPResponse, rsp.Body, rsp.JSON200, nil
	})
	if err != nil {
		return nil, err
	}
	return *out, nil
}
