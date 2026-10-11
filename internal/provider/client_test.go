package provider

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/oapi-codegen/nullable"

	"github.com/ionhour/go-sdk/api"
)

// newTestClient returns an apiClient against the fake whose provider-level
// sleeps are recorded instead of slept.
func newTestClient(t *testing.T, f *fakeAPI) (*apiClient, *[]time.Duration) {
	t.Helper()
	c, err := api.NewClient(api.WithAPIKey(f.key), api.WithBaseURL(f.baseURL()), api.WithMaxAttempts(maxAttempts))
	if err != nil {
		t.Fatal(err)
	}
	ac := newAPIClient(c)
	var slept []time.Duration
	ac.sleep = func(_ context.Context, d time.Duration) error {
		slept = append(slept, d)
		return nil
	}
	return ac, &slept
}

func mustCreateProject(t *testing.T, c *apiClient, name string) int {
	t.Helper()
	p, err := c.createProject(context.Background(), api.CreateProjectV1Dto{Name: name})
	if err != nil {
		t.Fatal(err)
	}
	return p.Id
}

func TestCreateSendsIdempotencyKey(t *testing.T) {
	f := newFakeAPI(t)
	c, _ := newTestClient(t, f)
	mustCreateProject(t, c, "P")
	reqs := f.requests(http.MethodPost, "/api/v1/projects")
	if len(reqs) != 1 || len(reqs[0].Header.Get("Idempotency-Key")) != 36 {
		t.Fatalf("want one POST with a UUID Idempotency-Key, got %d requests, key %q", len(reqs), reqs[0].Header.Get("Idempotency-Key"))
	}
}

func TestCreateRetryAfterLostResponseDoesNotDuplicate(t *testing.T) {
	f := newFakeAPI(t)
	c, _ := newTestClient(t, f)
	pid := mustCreateProject(t, c, "P")
	f.set(func(f *fakeAPI) { f.failCreateAfterCommit = 1 })

	out, err := c.createCheck(context.Background(), api.CreateCheckV1Dto{
		Name: "c", ProjectId: pid, Config: api.V1HttpCheckConfigDto{Url: "https://x.example.com"},
	})
	if err != nil {
		t.Fatalf("create after a lost response: %v", err)
	}
	reqs := f.requests(http.MethodPost, "/api/v1/checks")
	if len(reqs) != 2 {
		t.Fatalf("want 2 attempts (503 then replay), got %d", len(reqs))
	}
	if k1, k2 := reqs[0].Header.Get("Idempotency-Key"), reqs[1].Header.Get("Idempotency-Key"); k1 == "" || k1 != k2 {
		t.Fatalf("retries must reuse the key: %q vs %q", k1, k2)
	}
	if n := f.activeChecks(); n != 1 {
		t.Fatalf("want exactly 1 check after the retry, got %d", n)
	}
	if out.Id == 0 {
		t.Fatal("replayed create returned no id")
	}
}

func TestCreateInProgressResendsSameKey(t *testing.T) {
	f := newFakeAPI(t)
	c, slept := newTestClient(t, f)
	f.set(func(f *fakeAPI) { f.createInProgress = 1 })

	mustCreateProject(t, c, "P")
	reqs := f.requests(http.MethodPost, "/api/v1/projects")
	if len(reqs) != 2 || reqs[0].Header.Get("Idempotency-Key") != reqs[1].Header.Get("Idempotency-Key") {
		t.Fatalf("want 2 attempts with one key, got %d", len(reqs))
	}
	if len(*slept) != 1 || (*slept)[0] != inProgressWait {
		t.Fatalf("want one %s wait, got %v", inProgressWait, *slept)
	}
}

func TestCreateInProgressGivesUp(t *testing.T) {
	f := newFakeAPI(t)
	c, _ := newTestClient(t, f)
	f.set(func(f *fakeAPI) { f.createInProgress = createAttempts })

	_, err := c.createProject(context.Background(), api.CreateProjectV1Dto{Name: "P"})
	if !hasCode(err, codeIdempotencyInProgress) {
		t.Fatalf("want IDEMPOTENCY_KEY_IN_PROGRESS after %d attempts, got %v", createAttempts, err)
	}
}

// Review fix 2: a batch apply against a 60/min plan must survive 429s. The
// client waits Retry-After once; the provider keeps retrying (Retry-After plus
// jitter) until its budget is spent or the context ends.
func TestRateLimitRetriedWithinBudget(t *testing.T) {
	f := newFakeAPI(t)
	c, slept := newTestClient(t, f)
	c.jitter = func() time.Duration { return 250 * time.Millisecond }
	f.set(func(f *fakeAPI) { f.rateLimitNext = 3 })

	if _, err := c.listRegions(context.Background()); err != nil {
		t.Fatalf("three 429s then 200 must succeed: %v", err)
	}
	if len(*slept) != 1 || (*slept)[0] != 1250*time.Millisecond {
		t.Fatalf("want one provider wait of Retry-After 1s + 250ms jitter, got %v", *slept)
	}
}

func TestRateLimitGivesUpAfterBudget(t *testing.T) {
	f := newFakeAPI(t)
	c, slept := newTestClient(t, f)
	c.jitter = func() time.Duration { return 0 }
	c.rateLimitBudget = 1500 * time.Millisecond
	f.set(func(f *fakeAPI) { f.rateLimitNext = 1000 })

	_, err := c.listRegions(context.Background())
	if !api.IsRateLimited(err) {
		t.Fatalf("want the 429 once the budget is spent, got %v", err)
	}
	if len(*slept) != 1 {
		t.Fatalf("a 1.5s budget allows exactly one 1s wait, got %v", *slept)
	}
	_, detail := describeAPIError("list regions", err)
	if !strings.Contains(detail, "Retry after 1s") || !strings.Contains(detail, codeRateLimitedKey) {
		t.Errorf("rate-limit diagnostic lacks Retry-After guidance: %s", detail)
	}
}

func TestRateLimitStopsWhenContextEnds(t *testing.T) {
	f := newFakeAPI(t)
	c, _ := newTestClient(t, f)
	ctx, cancel := context.WithCancel(context.Background())
	c.sleep = func(ctx context.Context, _ time.Duration) error {
		cancel()
		return ctx.Err()
	}
	f.set(func(f *fakeAPI) { f.rateLimitNext = 1000 })

	if _, err := c.listRegions(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}

func TestRateLimitedCreateKeepsItsIdempotencyKey(t *testing.T) {
	f := newFakeAPI(t)
	c, _ := newTestClient(t, f)
	f.set(func(f *fakeAPI) { f.rateLimitNext = 2 })

	mustCreateProject(t, c, "P")
	reqs := f.requests(http.MethodPost, "/api/v1/projects")
	if len(reqs) != 3 {
		t.Fatalf("want 3 attempts (429, 429, 201), got %d", len(reqs))
	}
	key := reqs[0].Header.Get("Idempotency-Key")
	for i, r := range reqs {
		if r.Header.Get("Idempotency-Key") != key {
			t.Fatalf("attempt %d changed the idempotency key", i+1)
		}
	}
}

func TestDefaultJitterIsBounded(t *testing.T) {
	for i := 0; i < 100; i++ {
		if j := defaultJitter(); j < 0 || j >= maxJitter {
			t.Fatalf("jitter %s outside [0, %s)", j, maxJitter)
		}
	}
}

func TestGetMissingIsNotFound(t *testing.T) {
	f := newFakeAPI(t)
	c, _ := newTestClient(t, f)
	ctx := context.Background()
	if _, err := c.getCheck(ctx, "999"); !api.IsNotFound(err) {
		t.Errorf("getCheck(999) = %v, want 404", err)
	}
	if _, err := c.getJob(ctx, "slug:nope"); !api.IsNotFound(err) {
		t.Errorf("getJob(slug:nope) = %v, want 404", err)
	}
	if err := c.deleteProject(ctx, "999"); !api.IsNotFound(err) {
		t.Errorf("deleteProject(999) = %v, want 404", err)
	}
}

func TestSlugReleasedOnDelete(t *testing.T) {
	f := newFakeAPI(t)
	c, _ := newTestClient(t, f)
	ctx := context.Background()
	pid := mustCreateProject(t, c, "P")
	body := api.CreateJobV1Dto{Name: "j", ProjectId: pid, Slug: nullable.NewNullableWithValue("nightly")}
	j, err := c.createJob(ctx, body)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.createJob(ctx, body); !hasCode(err, codeSlugTaken) {
		t.Fatalf("second create with the same slug: %v, want SLUG_TAKEN", err)
	}
	if err := c.deleteJob(ctx, idRefInt(j.Id)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.createJob(ctx, body); err != nil {
		t.Fatalf("slug must be reusable after delete: %v", err)
	}
}

func TestListProjectsByNameExact(t *testing.T) {
	f := newFakeAPI(t)
	c, _ := newTestClient(t, f)
	mustCreateProject(t, c, "Payments")
	mustCreateProject(t, c, "Payments EU")
	got, err := c.listProjectsByName(context.Background(), "Payments")
	if err != nil || len(got) != 1 || got[0].Name != "Payments" {
		t.Fatalf("listProjectsByName = %+v, %v", got, err)
	}
}

func TestDescribeAPIError(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		wantSummary string
		wantDetail  []string
	}{
		{
			name:        "slug taken",
			err:         &api.APIError{Status: 409, Code: codeSlugTaken, Message: "Slug already in use"},
			wantSummary: "Unable to create Ionhour check (SLUG_TAKEN)",
			wantDetail:  []string{"HTTP 409", "Slug already in use", "terraform import", "slug:<slug>"},
		},
		{
			name:        "plan limit with sorted detail",
			err:         &api.APIError{Status: 403, Code: codePlanLimitExceeded, Message: "limit", Detail: map[string]any{"resource": "checks", "limit": 10, "plan": "PRO"}},
			wantSummary: "Unable to create Ionhour check (PLAN_LIMIT_EXCEEDED)",
			wantDetail:  []string{"Detail: limit=10, plan=PRO, resource=checks", "plan does not allow"},
		},
		{
			name:        "validation list",
			err:         &api.APIError{Status: 400, Code: codeValidationFailed, Message: "a; b", Messages: []string{"a", "b"}},
			wantSummary: "Unable to create Ionhour check (VALIDATION_FAILED)",
			wantDetail:  []string{"\n  - a", "\n  - b"},
		},
		{
			name:        "project not empty",
			err:         &api.APIError{Status: 409, Code: codeProjectNotEmpty, Message: "not empty"},
			wantSummary: "Unable to create Ionhour check (PROJECT_NOT_EMPTY)",
			wantDetail:  []string{"never cascades"},
		},
		{
			name:        "transport error",
			err:         errors.New("dial tcp: connection refused"),
			wantSummary: "Unable to create Ionhour check",
			wantDetail:  []string{"connection refused"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			summary, detail := describeAPIError("create Ionhour check", tt.err)
			if summary != tt.wantSummary {
				t.Errorf("summary = %q, want %q", summary, tt.wantSummary)
			}
			for _, w := range tt.wantDetail {
				if !strings.Contains(detail, w) {
					t.Errorf("detail lacks %q:\n%s", w, detail)
				}
			}
		})
	}
}

func TestAddAPIErrorSlugTakenTargetsSlug(t *testing.T) {
	var diags diag.Diagnostics
	addAPIError(&diags, "create Ionhour job", &api.APIError{Status: 409, Code: codeSlugTaken})
	withPath, ok := diags[0].(diag.DiagnosticWithPath)
	if !ok || withPath.Path().String() != "slug" {
		t.Fatalf("SLUG_TAKEN must point at the slug attribute, got %#v", diags[0])
	}
}

func idRefInt(id int) string { return strconv.Itoa(id) }

// ---- review fix 9: fake fidelity ----

func TestFakeEmptyPatchIsARead(t *testing.T) {
	f := newFakeAPI(t)
	c, _ := newTestClient(t, f)
	ctx := context.Background()
	pid := mustCreateProject(t, c, "P")
	before, err := c.getProject(ctx, idRefInt(pid))
	if err != nil {
		t.Fatal(err)
	}
	after, err := c.updateProject(ctx, idRefInt(pid), api.UpdateProjectV1Dto{})
	if err != nil {
		t.Fatal(err)
	}
	if !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("an empty PATCH must not write: updatedAt %s -> %s", before.UpdatedAt, after.UpdatedAt)
	}

	chk, err := c.createCheck(ctx, api.CreateCheckV1Dto{Name: "c", ProjectId: pid, Config: api.V1HttpCheckConfigDto{Url: "https://x.example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	url := "https://x.example.com"
	same, err := c.updateCheck(ctx, idRefInt(chk.Id), api.UpdateCheckV1Dto{Config: &api.V1HttpCheckConfigPatchDto{Url: &url}})
	if err != nil {
		t.Fatal(err)
	}
	if !same.UpdatedAt.Equal(chk.UpdatedAt) {
		t.Fatal("a PATCH restating only config.url must not write")
	}
}

func TestFakeRejectsForwardedAndProxyHeaders(t *testing.T) {
	f := newFakeAPI(t)
	c, _ := newTestClient(t, f)
	pid := mustCreateProject(t, c, "P")
	for _, name := range []string{"x-forwarded-for", "X-Forwarded-Proto", "proxy-authorization", "Proxy-Connection"} {
		_, err := c.createCheck(context.Background(), api.CreateCheckV1Dto{
			Name: "c", ProjectId: pid,
			Config: api.V1HttpCheckConfigDto{
				Url:           "https://x.example.com",
				CustomHeaders: nullable.NewNullableWithValue(map[string]string{name: "v"}),
			},
		})
		if !hasCode(err, codeValidationFailed) {
			t.Errorf("header %q: want 422 VALIDATION_FAILED, got %v", name, err)
		}
	}
}
