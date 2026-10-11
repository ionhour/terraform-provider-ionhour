package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeAPI is an in-memory implementation of the Integration API v1 contract
// for projects, checks, jobs, regions and whoami
// (specs/026-monitoring-as-code/contracts/v1-resources.md). It is strict where
// the provider could go wrong: unknown keys are 400 (forbidNonWhitelisted),
// null is rejected on non-nullable top-level keys, config.url is immutable,
// slugs are unique per workspace and released on delete, header names are
// lowercased, projects refuse to delete while non-empty, and creates honour
// Idempotency-Key replay.
type fakeAPI struct {
	srv *httptest.Server
	key string

	mu       sync.Mutex
	nextID   int
	clock    time.Time
	projects map[int]*fakeProject
	checks   map[int]*fakeCheck
	jobs     map[int]*fakeJob
	idem     map[string]fakeIdem
	log      []fakeRequest

	servedRegions  []string
	defaultRegions []string

	// Behaviour switches and fault injection (guarded by mu).
	withholdPingToken     bool
	createInProgress      int // answer N creates with 409 IDEMPOTENCY_KEY_IN_PROGRESS (nothing stored)
	failCreateAfterCommit int // commit N creates, then answer 503 (response "lost")
	rateLimitNext         int // answer N requests with 429 Retry-After: 1
}

type fakeRequest struct {
	Method string
	Path   string
	Header http.Header
	Body   []byte
}

type fakeIdem struct {
	bodyHash string
	id       int
}

type fakeProject struct {
	id                   int
	name                 string
	description          *string
	createdAt, updatedAt time.Time
	deleted              bool
}

type fakeCheck struct {
	id, projectID        int
	checkType            string
	slug                 *string
	name                 string
	interval, grace      int
	mute                 bool
	down, late, status   string
	url, method          string
	headers              map[string]string
	timeoutMs            int
	statusMin, statusMax int
	regions              []string
	follow, block        bool
	maxRedirects         int
	failAfter, resolveOK int
	latency              *int
	sslDays              int
	bodyHas, bodyHasNot  *string
	createdAt, updatedAt time.Time
	deleted              bool
}

type fakeJob struct {
	id, projectID        int
	slug                 *string
	name                 string
	interval, grace      int
	mute                 bool
	down, late, status   string
	token                string
	createdAt, updatedAt time.Time
	deleted              bool
}

const fakeAPIKey = "ionh_test_0123456789abcdef"

func newFakeAPI(t *testing.T) *fakeAPI {
	t.Helper()
	f := &fakeAPI{
		key:            fakeAPIKey,
		nextID:         100,
		clock:          time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC),
		projects:       map[int]*fakeProject{},
		checks:         map[int]*fakeCheck{},
		jobs:           map[int]*fakeJob{},
		idem:           map[string]fakeIdem{},
		servedRegions:  []string{"eu-central-1", "us-east-1", "ap-southeast-1"},
		defaultRegions: []string{"eu-central-1", "us-east-1"},
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAPI) baseURL() string { return f.srv.URL + "/api" }

// ---- inspection helpers for tests ----

func (f *fakeAPI) requests(method, pathPrefix string) []fakeRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []fakeRequest
	for _, r := range f.log {
		if r.Method == method && strings.HasPrefix(r.Path, pathPrefix) {
			out = append(out, r)
		}
	}
	return out
}

func (f *fakeAPI) activeChecks() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.checks {
		if !c.deleted {
			n++
		}
	}
	return n
}

// set runs fn under the lock (fault injection, out-of-band edits).
func (f *fakeAPI) set(fn func(f *fakeAPI)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

// addCheckOutOfBand simulates a dashboard-created check in a project.
func (f *fakeAPI) addCheckOutOfBand(projectID int) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.newCheckDefaults(projectID, "dashboard check", "https://dashboard.example.com")
	f.checks[c.id] = c
	return c.id
}

func (f *fakeAPI) deleteOutOfBand(kind string, id int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch kind {
	case "check":
		if c := f.checks[id]; c != nil {
			c.deleted, c.slug = true, nil
		}
	case "job":
		if j := f.jobs[id]; j != nil {
			j.deleted, j.slug = true, nil
		}
	case "project":
		if p := f.projects[id]; p != nil {
			p.deleted = true
		}
	}
}

// ---- HTTP plumbing ----

func (f *fakeAPI) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log = append(f.log, fakeRequest{Method: r.Method, Path: r.URL.Path, Header: r.Header.Clone(), Body: body})

	if r.Header.Get("Authorization") != "Bearer "+f.key {
		writeError(w, http.StatusUnauthorized, "INVALID_KEY", "Invalid API key")
		return
	}
	if f.rateLimitNext > 0 {
		f.rateLimitNext--
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusTooManyRequests, "RATE_LIMIT_EXCEEDED_KEY", "Rate limit exceeded")
		return
	}

	route, ok := strings.CutPrefix(r.URL.Path, "/api/v1/")
	if !ok {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Cannot "+r.Method+" "+r.URL.Path)
		return
	}
	parts := strings.Split(route, "/")
	switch {
	case route == "whoami" && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{
			"workspaceId": 42, "workspaceName": "Acceptance", "permissionLevel": "READ_WRITE",
			"principalKind": "service", "projectedRole": "ADMIN", "apiKeyPrefix": "ionh_tes…",
		})
	case route == "regions" && r.Method == http.MethodGet:
		out := make([]map[string]any, 0, len(f.servedRegions))
		for _, id := range f.servedRegions {
			out = append(out, map[string]any{"id": id, "displayName": strings.ToUpper(id), "flag": "🏳", "enabled": true})
		}
		writeJSON(w, http.StatusOK, out)
	case len(parts) == 1 && r.Method == http.MethodGet && parts[0] == "projects":
		f.listProjects(w, r)
	case len(parts) == 1 && r.Method == http.MethodPost:
		f.create(w, r, parts[0], body)
	case len(parts) == 2:
		f.byID(w, r, parts[0], parts[1], body)
	default:
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Cannot "+r.Method+" "+r.URL.Path)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code string, message any) {
	writeJSON(w, status, map[string]any{
		"statusCode": status, "error": http.StatusText(status), "code": code, "message": message,
	})
}

func (f *fakeAPI) tick() time.Time {
	f.clock = f.clock.Add(time.Second)
	return f.clock
}

func (f *fakeAPI) newID() int {
	f.nextID++
	return f.nextID
}

// ---- create with idempotency ----

func (f *fakeAPI) create(w http.ResponseWriter, r *http.Request, kind string, body []byte) {
	if kind != "projects" && kind != "checks" && kind != "jobs" {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "unknown route")
		return
	}
	if f.createInProgress > 0 {
		f.createInProgress--
		writeError(w, http.StatusConflict, "IDEMPOTENCY_KEY_IN_PROGRESS", "A request with this key is in flight")
		return
	}
	key := r.Header.Get("Idempotency-Key")
	scope := kind + " " + key
	sum := sha256.Sum256(body)
	hash := hex.EncodeToString(sum[:])
	if key != "" {
		if prev, ok := f.idem[scope]; ok {
			if prev.bodyHash != hash {
				writeError(w, http.StatusUnprocessableEntity, "IDEMPOTENCY_KEY_REUSED", "Key reused with a different body")
				return
			}
			w.Header().Set("Idempotency-Replayed", "true")
			f.writeResource(w, http.StatusCreated, kind, prev.id)
			return
		}
	}

	in, err := decodeObject(body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "VALIDATION_FAILED", []string{err.Error()})
		return
	}
	var (
		id     int
		status int
		code   string
		msg    any
	)
	switch kind {
	case "projects":
		id, status, code, msg = f.createProject(in)
	case "checks":
		id, status, code, msg = f.createCheck(in)
	case "jobs":
		id, status, code, msg = f.createJob(in)
	}
	if code != "" {
		writeError(w, status, code, msg)
		return
	}
	if key != "" {
		f.idem[scope] = fakeIdem{bodyHash: hash, id: id}
	}
	if f.failCreateAfterCommit > 0 {
		f.failCreateAfterCommit--
		writeError(w, http.StatusServiceUnavailable, "HTTP_503", "upstream timeout")
		return
	}
	f.writeResource(w, http.StatusCreated, kind, id)
}

func (f *fakeAPI) writeResource(w http.ResponseWriter, status int, kind string, id int) {
	switch kind {
	case "projects":
		writeJSON(w, status, f.projectJSON(f.projects[id]))
	case "checks":
		writeJSON(w, status, f.checkJSON(f.checks[id]))
	case "jobs":
		writeJSON(w, status, f.jobJSON(f.jobs[id]))
	}
}

// ---- by id: GET / PATCH / DELETE ----

func (f *fakeAPI) byID(w http.ResponseWriter, r *http.Request, kind, ref string, body []byte) {
	id, found := f.resolve(kind, ref)
	if !found {
		writeError(w, http.StatusNotFound, "NOT_FOUND", fmt.Sprintf("%s %s not found", kind, ref))
		return
	}
	switch r.Method {
	case http.MethodGet:
		f.writeResource(w, http.StatusOK, kind, id)
	case http.MethodDelete:
		f.remove(w, kind, id)
	case http.MethodPatch:
		in, err := decodeObject(body)
		if err != nil {
			writeError(w, http.StatusBadRequest, "VALIDATION_FAILED", []string{err.Error()})
			return
		}
		var (
			status int
			code   string
			msg    any
		)
		switch kind {
		case "projects":
			status, code, msg = f.patchProject(f.projects[id], in)
		case "checks":
			status, code, msg = f.patchCheck(f.checks[id], in)
		case "jobs":
			status, code, msg = f.patchJob(f.jobs[id], in)
		}
		if code != "" {
			writeError(w, status, code, msg)
			return
		}
		f.writeResource(w, http.StatusOK, kind, id)
	default:
		writeError(w, http.StatusNotFound, "NOT_FOUND", "unsupported method")
	}
}

func (f *fakeAPI) resolve(kind, ref string) (int, bool) {
	slug, bySlug := strings.CutPrefix(ref, "slug:")
	n, numErr := strconv.Atoi(ref)
	switch kind {
	case "projects":
		p, ok := f.projects[n]
		return n, numErr == nil && ok && !p.deleted
	case "checks":
		for id, c := range f.checks {
			if !c.deleted && ((bySlug && c.slug != nil && *c.slug == slug) || (!bySlug && numErr == nil && id == n)) {
				return id, true
			}
		}
	case "jobs":
		for id, j := range f.jobs {
			if !j.deleted && ((bySlug && j.slug != nil && *j.slug == slug) || (!bySlug && numErr == nil && id == n)) {
				return id, true
			}
		}
	}
	return 0, false
}

func (f *fakeAPI) remove(w http.ResponseWriter, kind string, id int) {
	switch kind {
	case "projects":
		for _, c := range f.checks {
			if !c.deleted && c.projectID == id {
				writeError(w, http.StatusConflict, "PROJECT_NOT_EMPTY", "Project still has active checks or jobs")
				return
			}
		}
		for _, j := range f.jobs {
			if !j.deleted && j.projectID == id {
				writeError(w, http.StatusConflict, "PROJECT_NOT_EMPTY", "Project still has active checks or jobs")
				return
			}
		}
		f.projects[id].deleted = true
	case "checks":
		f.checks[id].deleted, f.checks[id].slug = true, nil
	case "jobs":
		f.jobs[id].deleted, f.jobs[id].slug = true, nil
	}
	w.WriteHeader(http.StatusNoContent)
}

func (f *fakeAPI) listProjects(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	ids := make([]int, 0, len(f.projects))
	for id, p := range f.projects {
		if !p.deleted && (name == "" || p.name == name) {
			ids = append(ids, id)
		}
	}
	sort.Ints(ids)
	items := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		items = append(items, f.projectJSON(f.projects[id]))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items, "total": len(items), "page": 1, "itemCount": len(items), "pageCount": 1, "limit": 100,
	})
}

// ---- projects ----

func (f *fakeAPI) createProject(in *jsonObject) (int, int, string, any) {
	in.only("name", "description")
	name := in.str("name", true)
	desc := in.optStr("description")
	if errs := in.errors(); errs != nil {
		return 0, http.StatusBadRequest, "VALIDATION_FAILED", errs
	}
	now := f.tick()
	p := &fakeProject{id: f.newID(), name: name, description: desc, createdAt: now, updatedAt: now}
	f.projects[p.id] = p
	return p.id, 0, "", nil
}

func (f *fakeAPI) patchProject(p *fakeProject, in *jsonObject) (int, string, any) {
	in.only("name", "description")
	in.noNull("name")
	name, hasName := in.str("name", false), in.has("name")
	desc, hasDesc := in.optStr("description"), in.has("description")
	if errs := in.errors(); errs != nil {
		return http.StatusBadRequest, "VALIDATION_FAILED", errs
	}
	if len(in.m) == 0 {
		return 0, "", nil // an empty PATCH is a read
	}
	if hasName {
		p.name = name
	}
	if hasDesc {
		p.description = desc
	}
	p.updatedAt = f.tick()
	return 0, "", nil
}

func (f *fakeAPI) projectJSON(p *fakeProject) map[string]any {
	return map[string]any{
		"id": p.id, "name": p.name, "description": p.description,
		"createdAt": p.createdAt.Format(time.RFC3339Nano), "updatedAt": p.updatedAt.Format(time.RFC3339Nano),
	}
}

func (f *fakeAPI) projectActive(id int) bool {
	p, ok := f.projects[id]
	return ok && !p.deleted
}

func (f *fakeAPI) slugTaken(slug string, selfKind string, selfID int) bool {
	for id, c := range f.checks {
		if !c.deleted && c.slug != nil && *c.slug == slug && !(selfKind == "checks" && id == selfID) {
			return true
		}
	}
	for id, j := range f.jobs {
		if !j.deleted && j.slug != nil && *j.slug == slug && !(selfKind == "jobs" && id == selfID) {
			return true
		}
	}
	return false
}

// ---- checks ----

var (
	topCheckKeys  = []string{"name", "slug", "projectId", "checkType", "intervalSeconds", "graceSeconds", "muteNotifications", "downSeverity", "lateSeverity", "config"}
	httpConfigKey = []string{"url", "method", "customHeaders", "timeoutMs", "expectedStatusMin", "expectedStatusMax", "regions", "followRedirects", "maxRedirects", "blockHttpDowngrade", "failAfterConsecutive", "resolveAfterConsecutiveSuccess", "latencyWarnMs", "sslExpiryWarnDays", "bodyContains", "bodyNotContains"}
	fakeSlugRe    = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)
	reservedFake  = map[string]bool{
		"host": true, "authorization": true, "cookie": true, "connection": true, "content-length": true,
		"transfer-encoding": true, "__proto__": true, "constructor": true, "prototype": true,
	}
)

func (f *fakeAPI) newCheckDefaults(projectID int, name, url string) *fakeCheck {
	now := f.tick()
	return &fakeCheck{
		id: f.newID(), projectID: projectID, checkType: "HTTP", name: name, interval: 300, grace: 5,
		down: "P1", late: "P3", status: "NEW", url: url, method: "GET", timeoutMs: 8000,
		statusMin: 200, statusMax: 399, follow: true, maxRedirects: 3, block: true,
		failAfter: 3, resolveOK: 2, sslDays: 14, createdAt: now, updatedAt: now,
	}
}

func (f *fakeAPI) createCheck(in *jsonObject) (int, int, string, any) {
	in.only(topCheckKeys...)
	name := in.str("name", true)
	projectID := in.integer("projectId", true)
	cfg := in.object("config", true)
	if cfg != nil {
		cfg.only(httpConfigKey...)
		cfg.str("url", true)
	}
	if errs := in.errors(); errs != nil {
		return 0, http.StatusBadRequest, "VALIDATION_FAILED", errs
	}
	if ct := in.str("checkType", false); in.has("checkType") && ct != "HTTP" {
		return 0, http.StatusUnprocessableEntity, "CHECK_TYPE_NOT_SUPPORTED", "Only HTTP checks are supported"
	}
	if !f.projectActive(projectID) {
		return 0, http.StatusNotFound, "NOT_FOUND", "project not found"
	}
	c := f.newCheckDefaults(projectID, name, cfg.str("url", true))
	if status, code, msg := f.applyCheck(c, in, cfg, true); code != "" {
		f.nextID-- // nothing stored
		return 0, status, code, msg
	}
	f.checks[c.id] = c
	return c.id, 0, "", nil
}

func (f *fakeAPI) patchCheck(c *fakeCheck, in *jsonObject) (int, string, any) {
	in.only(topCheckKeys...)
	for _, k := range topCheckKeys {
		if k != "slug" {
			in.noNull(k)
		}
	}
	cfg := in.object("config", false)
	if cfg != nil {
		cfg.only(httpConfigKey...)
		cfg.noNull("url")
	}
	if errs := in.errors(); errs != nil {
		return http.StatusBadRequest, "VALIDATION_FAILED", errs
	}
	if cfg != nil && cfg.has("url") && cfg.str("url", false) != c.url {
		return http.StatusUnprocessableEntity, "VALIDATION_FAILED", []string{"config.url is immutable"}
	}
	if emptyCheckPatch(in, cfg) {
		return 0, "", nil // no keys, or only config.url restated: a read
	}
	if in.has("projectId") && !f.projectActive(in.integer("projectId", false)) {
		return http.StatusNotFound, "NOT_FOUND", "project not found"
	}
	next := *c
	if status, code, msg := f.applyCheck(&next, in, cfg, false); code != "" {
		return status, code, msg
	}
	next.updatedAt = f.tick()
	*c = next
	return 0, "", nil
}

// applyCheck writes the keys present in the body onto c. On PATCH, null
// inside config clears nullable fields and resets defaulted ones.
func (f *fakeAPI) applyCheck(c *fakeCheck, in, cfg *jsonObject, creating bool) (int, string, any) {
	if in.has("name") {
		c.name = in.str("name", false)
	}
	if in.has("slug") {
		c.slug = in.optStr("slug")
		if c.slug != nil {
			if !fakeSlugRe.MatchString(*c.slug) {
				return http.StatusBadRequest, "VALIDATION_FAILED", []string{"slug must match ^[a-z0-9_-]{1,64}$"}
			}
			if f.slugTaken(*c.slug, "checks", c.id) {
				return http.StatusConflict, "SLUG_TAKEN", "Slug already in use in this workspace"
			}
		}
	}
	if in.has("projectId") {
		c.projectID = in.integer("projectId", false)
	}
	intField(in, "intervalSeconds", &c.interval, 300)
	intField(in, "graceSeconds", &c.grace, 5)
	if in.has("muteNotifications") {
		c.mute = in.boolean("muteNotifications")
	}
	if in.has("downSeverity") {
		c.down = in.str("downSeverity", false)
	}
	if in.has("lateSeverity") {
		c.late = in.str("lateSeverity", false)
	}
	if cfg == nil {
		return 0, "", nil
	}
	if cfg.has("method") {
		c.method = "GET"
		if !cfg.isNull("method") {
			c.method = cfg.str("method", false)
		}
	}
	if cfg.has("customHeaders") {
		c.headers = nil
		if !cfg.isNull("customHeaders") {
			raw := cfg.strMap("customHeaders")
			c.headers = map[string]string{}
			for k, v := range raw {
				lk := strings.ToLower(k)
				reserved := reservedFake[lk] || strings.HasPrefix(lk, "x-forwarded-") || strings.HasPrefix(lk, "proxy-")
				if _, dup := c.headers[lk]; dup || reserved {
					return http.StatusUnprocessableEntity, "VALIDATION_FAILED", []string{"invalid header " + k}
				}
				c.headers[lk] = v
			}
		}
	}
	intField(cfg, "timeoutMs", &c.timeoutMs, 8000)
	intField(cfg, "expectedStatusMin", &c.statusMin, 200)
	intField(cfg, "expectedStatusMax", &c.statusMax, 399)
	intField(cfg, "maxRedirects", &c.maxRedirects, 3)
	intField(cfg, "failAfterConsecutive", &c.failAfter, 3)
	intField(cfg, "resolveAfterConsecutiveSuccess", &c.resolveOK, 2)
	intField(cfg, "sslExpiryWarnDays", &c.sslDays, 14)
	boolField(cfg, "followRedirects", &c.follow, true)
	boolField(cfg, "blockHttpDowngrade", &c.block, true)
	if cfg.has("latencyWarnMs") {
		c.latency = nil
		if !cfg.isNull("latencyWarnMs") {
			v := cfg.integer("latencyWarnMs", false)
			c.latency = &v
		}
	}
	if cfg.has("bodyContains") {
		c.bodyHas = cfg.optStr("bodyContains")
	}
	if cfg.has("bodyNotContains") {
		c.bodyHasNot = cfg.optStr("bodyNotContains")
	}
	if cfg.has("regions") {
		c.regions = nil
		if !cfg.isNull("regions") {
			regions := cfg.strList("regions")
			if len(regions) == 0 {
				return http.StatusBadRequest, "VALIDATION_FAILED", []string{"regions must not be empty"}
			}
			for _, r := range regions {
				if !contains(f.servedRegions, r) {
					return http.StatusUnprocessableEntity, "VALIDATION_FAILED", []string{"region " + r + " is not served"}
				}
			}
			c.regions = regions
		}
	}
	if c.statusMin > c.statusMax {
		return http.StatusUnprocessableEntity, "VALIDATION_FAILED", []string{"expectedStatusMin > expectedStatusMax"}
	}
	if errs := cfg.errors(); errs != nil {
		return http.StatusBadRequest, "VALIDATION_FAILED", errs
	}
	return 0, "", nil
}

func (f *fakeAPI) checkJSON(c *fakeCheck) map[string]any {
	regions := c.regions
	if len(regions) == 0 {
		regions = f.defaultRegions
	}
	var headers any
	if c.headers != nil {
		headers = c.headers
	}
	var latency any
	if c.latency != nil {
		latency = *c.latency
	}
	return map[string]any{
		"id": c.id, "slug": c.slug, "name": c.name, "projectId": c.projectID, "checkType": c.checkType,
		"intervalSeconds": c.interval, "graceSeconds": c.grace, "muteNotifications": c.mute,
		"downSeverity": c.down, "lateSeverity": c.late, "paused": c.status == "PAUSED", "status": c.status,
		"config": map[string]any{
			"url": c.url, "method": c.method, "customHeaders": headers, "timeoutMs": c.timeoutMs,
			"expectedStatusMin": c.statusMin, "expectedStatusMax": c.statusMax, "regions": regions,
			"followRedirects": c.follow, "maxRedirects": c.maxRedirects, "blockHttpDowngrade": c.block,
			"failAfterConsecutive": c.failAfter, "resolveAfterConsecutiveSuccess": c.resolveOK,
			"latencyWarnMs": latency, "sslExpiryWarnDays": c.sslDays, "bodyContains": c.bodyHas,
			"bodyNotContains": c.bodyHasNot, "ipFamily": "AUTO", "skipTlsVerification": false,
			"maxResponseBytes": 262144,
		},
		"createdAt": c.createdAt.Format(time.RFC3339Nano), "updatedAt": c.updatedAt.Format(time.RFC3339Nano),
	}
}

// ---- jobs ----

var jobKeys = []string{"name", "slug", "projectId", "intervalSeconds", "graceSeconds", "muteNotifications", "downSeverity", "lateSeverity"}

func (f *fakeAPI) createJob(in *jsonObject) (int, int, string, any) {
	in.only(jobKeys...)
	name := in.str("name", true)
	projectID := in.integer("projectId", true)
	if errs := in.errors(); errs != nil {
		return 0, http.StatusBadRequest, "VALIDATION_FAILED", errs
	}
	if !f.projectActive(projectID) {
		return 0, http.StatusNotFound, "NOT_FOUND", "project not found"
	}
	now := f.tick()
	j := &fakeJob{
		id: f.newID(), projectID: projectID, name: name, interval: 3600, grace: 300,
		down: "P1", late: "P3", status: "NEW", createdAt: now, updatedAt: now,
	}
	j.token = fmt.Sprintf("tok%016d", j.id)
	if status, code, msg := f.applyJob(j, in); code != "" {
		f.nextID--
		return 0, status, code, msg
	}
	f.jobs[j.id] = j
	return j.id, 0, "", nil
}

func (f *fakeAPI) patchJob(j *fakeJob, in *jsonObject) (int, string, any) {
	in.only(jobKeys...)
	for _, k := range jobKeys {
		if k != "slug" {
			in.noNull(k)
		}
	}
	if errs := in.errors(); errs != nil {
		return http.StatusBadRequest, "VALIDATION_FAILED", errs
	}
	if len(in.m) == 0 {
		return 0, "", nil // an empty PATCH is a read
	}
	if in.has("projectId") && !f.projectActive(in.integer("projectId", false)) {
		return http.StatusNotFound, "NOT_FOUND", "project not found"
	}
	next := *j
	if status, code, msg := f.applyJob(&next, in); code != "" {
		return status, code, msg
	}
	next.updatedAt = f.tick()
	*j = next
	return 0, "", nil
}

func (f *fakeAPI) applyJob(j *fakeJob, in *jsonObject) (int, string, any) {
	if in.has("name") {
		j.name = in.str("name", false)
	}
	if in.has("slug") {
		j.slug = in.optStr("slug")
		if j.slug != nil {
			if !fakeSlugRe.MatchString(*j.slug) {
				return http.StatusBadRequest, "VALIDATION_FAILED", []string{"slug must match ^[a-z0-9_-]{1,64}$"}
			}
			if f.slugTaken(*j.slug, "jobs", j.id) {
				return http.StatusConflict, "SLUG_TAKEN", "Slug already in use in this workspace"
			}
		}
	}
	if in.has("projectId") {
		j.projectID = in.integer("projectId", false)
	}
	intField(in, "intervalSeconds", &j.interval, 3600)
	intField(in, "graceSeconds", &j.grace, 300)
	if in.has("muteNotifications") {
		j.mute = in.boolean("muteNotifications")
	}
	if in.has("downSeverity") {
		j.down = in.str("downSeverity", false)
	}
	if in.has("lateSeverity") {
		j.late = in.str("lateSeverity", false)
	}
	if errs := in.errors(); errs != nil {
		return http.StatusBadRequest, "VALIDATION_FAILED", errs
	}
	return 0, "", nil
}

func (f *fakeAPI) jobJSON(j *fakeJob) map[string]any {
	var token, url any
	if !f.withholdPingToken {
		token = j.token
		url = "https://signal.example.test/api/signals/ping/" + j.token
	}
	return map[string]any{
		"id": j.id, "slug": j.slug, "name": j.name, "projectId": j.projectID,
		"intervalSeconds": j.interval, "graceSeconds": j.grace, "muteNotifications": j.mute,
		"downSeverity": j.down, "lateSeverity": j.late, "paused": j.status == "PAUSED", "status": j.status,
		"pingToken": token, "pingUrl": url,
		"createdAt": j.createdAt.Format(time.RFC3339Nano), "updatedAt": j.updatedAt.Format(time.RFC3339Nano),
	}
}

// ---- strict JSON object decoding ----

// jsonObject distinguishes absent, null and present keys, and collects
// validation errors the way the API's ValidationPipe reports them.
type jsonObject struct {
	prefix string
	m      map[string]json.RawMessage
	errs   *[]string
}

func decodeObject(body []byte) (*jsonObject, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil || m == nil {
		return nil, fmt.Errorf("body must be a JSON object")
	}
	return &jsonObject{m: m, errs: &[]string{}}, nil
}

func (o *jsonObject) fail(format string, args ...any) {
	*o.errs = append(*o.errs, o.prefix+fmt.Sprintf(format, args...))
}

func (o *jsonObject) errors() []string {
	if len(*o.errs) == 0 {
		return nil
	}
	return *o.errs
}

func (o *jsonObject) has(k string) bool { _, ok := o.m[k]; return ok }

func (o *jsonObject) isNull(k string) bool { return o.has(k) && string(o.m[k]) == "null" }

func (o *jsonObject) only(allowed ...string) {
	for k := range o.m {
		if !contains(allowed, k) {
			o.fail("property %s should not exist", k)
		}
	}
}

func (o *jsonObject) noNull(k string) {
	if o.isNull(k) {
		o.fail("%s must not be null", k)
	}
}

func (o *jsonObject) decode(k string, v any, required bool) bool {
	raw, ok := o.m[k]
	if !ok || string(raw) == "null" {
		if required {
			o.fail("%s is required", k)
		}
		return false
	}
	if err := json.Unmarshal(raw, v); err != nil {
		o.fail("%s has the wrong type", k)
		return false
	}
	return true
}

func (o *jsonObject) str(k string, required bool) string {
	var s string
	o.decode(k, &s, required)
	return s
}

func (o *jsonObject) optStr(k string) *string {
	var s string
	if o.decode(k, &s, false) {
		return &s
	}
	return nil
}

func (o *jsonObject) integer(k string, required bool) int {
	var n int
	o.decode(k, &n, required)
	return n
}

func (o *jsonObject) boolean(k string) bool {
	var b bool
	o.decode(k, &b, false)
	return b
}

func (o *jsonObject) strMap(k string) map[string]string {
	var m map[string]string
	o.decode(k, &m, false)
	return m
}

func (o *jsonObject) strList(k string) []string {
	var l []string
	o.decode(k, &l, false)
	return l
}

func (o *jsonObject) object(k string, required bool) *jsonObject {
	var m map[string]json.RawMessage
	if !o.decode(k, &m, required) {
		return nil
	}
	return &jsonObject{prefix: o.prefix + k + ".", m: m, errs: o.errs}
}

// emptyCheckPatch reports a check PATCH that writes nothing: no keys, or a
// config holding only the (already verified equal) url.
func emptyCheckPatch(in, cfg *jsonObject) bool {
	if len(in.m) == 0 {
		return true
	}
	if len(in.m) != 1 || cfg == nil {
		return false
	}
	return len(cfg.m) == 0 || (len(cfg.m) == 1 && cfg.has("url"))
}

// intField applies a present key: null resets to def, a value is stored.
func intField(o *jsonObject, k string, dst *int, def int) {
	if !o.has(k) {
		return
	}
	if o.isNull(k) {
		*dst = def
		return
	}
	*dst = o.integer(k, false)
}

func boolField(o *jsonObject, k string, dst *bool, def bool) {
	if !o.has(k) {
		return
	}
	if o.isNull(k) {
		*dst = def
		return
	}
	*dst = o.boolean(k)
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
