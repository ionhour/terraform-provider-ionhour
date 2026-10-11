package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/ionhour/go-sdk/api"
)

// Live acceptance target. Point these at a dedicated SIT workspace with a
// SERVICE key (serviceRole MEMBER is enough for create/update; ADMIN for the
// destroy steps) whose plan allows the plan-gated check fields.
const (
	envAccBaseURL = "IONHOUR_ACC_BASE_URL"
	envAccAPIKey  = "IONHOUR_ACC_API_KEY"
)

var testAccProviders = map[string]func() (tfprotov6.ProviderServer, error){
	"ionhour": providerserver.NewProtocol6WithError(New("test")()),
}

// accEnv describes one acceptance target: the in-process contract fake, or a
// live API when fake is nil.
type accEnv struct {
	fake   *fakeAPI
	suffix string // random, lowercase: safe in names and slugs
}

func (e accEnv) name(kind string) string { return "tf-acc-" + kind + "-" + e.suffix }

// onFake runs fn only against the fake (request-level assertions); it is a
// no-op against a live API.
func (e accEnv) onFake(fn func(f *fakeAPI) error) resource.TestCheckFunc {
	return func(*terraform.State) error {
		if e.fake == nil {
			return nil
		}
		return fn(e.fake)
	}
}

type stepsFunc func(t *testing.T, env accEnv) []resource.TestStep

// runAcc runs the same steps against the fake and, when configured, a live
// workspace. resource.Test skips both unless TF_ACC is set.
func runAcc(t *testing.T, steps stepsFunc) {
	t.Run("fake", func(t *testing.T) { runFakeAcc(t, steps) })
	t.Run("live", func(t *testing.T) {
		base, key := os.Getenv(envAccBaseURL), os.Getenv(envAccAPIKey)
		if base == "" || key == "" {
			t.Skipf("set %s and %s to run against a live workspace", envAccBaseURL, envAccAPIKey)
		}
		t.Setenv(envAPIKey, key)
		t.Setenv(envBaseURL, base)
		accTest(t, accEnv{suffix: acctest.RandString(8)}, steps)
	})
}

// runFakeAcc runs steps against the contract fake only (fault injection and
// out-of-band edits a live workspace cannot provide).
func runFakeAcc(t *testing.T, steps stepsFunc) {
	f := newFakeAPI(t)
	t.Setenv(envAPIKey, f.key)
	t.Setenv(envBaseURL, f.baseURL())
	accTest(t, accEnv{fake: f, suffix: acctest.RandString(8)}, steps)
}

func accTest(t *testing.T, env accEnv, steps stepsFunc) {
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProviders,
		CheckDestroy:             checkDestroyed,
		Steps:                    steps(t, env),
	})
}

// accClient talks to whichever target the provider is configured for.
func accClient() (*apiClient, error) {
	c, err := api.NewClient(api.WithMaxAttempts(maxAttempts))
	if err != nil {
		return nil, err
	}
	return newAPIClient(c), nil
}

// checkDestroyed asserts every Ionhour resource in state is gone (404).
func checkDestroyed(s *terraform.State) error {
	c, err := accClient()
	if err != nil {
		return err
	}
	ctx := context.Background()
	for _, rs := range s.RootModule().Resources {
		id := rs.Primary.Attributes["id"]
		switch rs.Type {
		case "ionhour_project":
			_, err = c.getProject(ctx, id)
		case "ionhour_check":
			_, err = c.getCheck(ctx, id)
		case "ionhour_job":
			_, err = c.getJob(ctx, id)
		default:
			continue
		}
		if err == nil {
			return fmt.Errorf("%s %s still exists after destroy", rs.Type, id)
		}
		if !api.IsNotFound(err) {
			return fmt.Errorf("%s %s: %w", rs.Type, id, err)
		}
	}
	return nil
}

// lastBody returns the JSON body of the latest request matching method and
// path prefix.
func (f *fakeAPI) lastBodyMap(method, pathPrefix string) (map[string]any, error) {
	reqs := f.requests(method, pathPrefix)
	if len(reqs) == 0 {
		return nil, fmt.Errorf("no %s %s request recorded", method, pathPrefix)
	}
	var m map[string]any
	if err := json.Unmarshal(reqs[len(reqs)-1].Body, &m); err != nil {
		return nil, fmt.Errorf("decode %s %s body: %w", method, pathPrefix, err)
	}
	return m, nil
}

// expectLastBody compares the latest matching request body with want, after
// dropping the keys in ignore (values the test cannot predict, such as ids).
func expectLastBody(method, pathPrefix, want string, ignore ...string) func(f *fakeAPI) error {
	return func(f *fakeAPI) error {
		got, err := f.lastBodyMap(method, pathPrefix)
		if err != nil {
			return err
		}
		for _, k := range ignore {
			delete(got, k)
		}
		var w map[string]any
		if err := json.Unmarshal([]byte(want), &w); err != nil {
			return fmt.Errorf("bad expectation: %w", err)
		}
		if !reflect.DeepEqual(got, w) {
			gb, _ := json.Marshal(got)
			return fmt.Errorf("%s %s body\n got: %s\nwant: %s", method, pathPrefix, gb, want)
		}
		return nil
	}
}

// expectIdempotencyKeys asserts every create POST under pathPrefix carried
// an Idempotency-Key.
func expectIdempotencyKeys(pathPrefix string) func(f *fakeAPI) error {
	return func(f *fakeAPI) error {
		reqs := f.requests("POST", pathPrefix)
		if len(reqs) == 0 {
			return fmt.Errorf("no POST %s recorded", pathPrefix)
		}
		for i, r := range reqs {
			if r.Header.Get("Idempotency-Key") == "" {
				return fmt.Errorf("POST %s #%d had no Idempotency-Key", pathPrefix, i+1)
			}
		}
		return nil
	}
}

// captureAttr stores an attribute value for comparison in a later step.
func captureAttr(name, key string, dst *string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[name]
		if !ok {
			return fmt.Errorf("%s not in state", name)
		}
		*dst = rs.Primary.Attributes[key]
		return nil
	}
}

func expectAttrUnchanged(name, key string, prev *string) resource.TestCheckFunc {
	return resource.TestCheckResourceAttrWith(name, key, func(v string) error {
		if v != *prev {
			return fmt.Errorf("%s.%s changed from %q to %q", name, key, *prev, v)
		}
		return nil
	})
}

func expectAttrChanged(name, key string, prev *string) resource.TestCheckFunc {
	return resource.TestCheckResourceAttrWith(name, key, func(v string) error {
		if v == *prev {
			return fmt.Errorf("%s.%s still %q, expected a new value", name, key, v)
		}
		return nil
	})
}

func importBySlug(slug string) resource.ImportStateIdFunc {
	return func(*terraform.State) (string, error) { return "slug:" + slug, nil }
}
