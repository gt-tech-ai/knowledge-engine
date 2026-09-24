package e2ekit

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // pgx stdlib driver for database/sql (worker-effect seed/poll/teardown)

	apperr "github.com/gt-tech-ai/knowledge-engine/go/core/errors"
)

// E2EWorkspacePrefix marks every workspace the endpoint E2E suite creates in the dedicated test org,
// so the pre-run sweep (ReconcileStaleWorkspaces) can find and reclaim orphans a prior run left when a
// SIGKILL (activeDeadlineSeconds) skipped its deferred teardown.
const E2EWorkspacePrefix = "E2E "

// RunID returns a per-run identifier — E2E_RUN_ID (the in-cluster Job sets it to the image tag), or
// "local" when unset — so a run's seeded artifacts are traceable to the deploy that created them. Used
// only to name test artifacts; never a production id.
func RunID() string {
	if v := os.Getenv("E2E_RUN_ID"); v != "" {
		return v
	}
	return "local"
}

// ReconcileStaleWorkspaces deletes endpoint-E2E workspaces (name starting E2EWorkspacePrefix) — and
// every row that FK-references them — in orgID that are older than staleAfter, reclaiming orphans a
// prior run left when its teardown did not complete (a Job killed by activeDeadlineSeconds). It is
// TIME-based, not run-id based, on purpose: both E2E Jobs share the dedicated test org and may run
// concurrently on the same deploy, so reaping only artifacts older than any live run's max lifetime
// (staleAfter) spares the concurrent run's fresh data while still self-healing prior-deploy orphans.
// Idempotent; scoped to the E2E name prefix + the test org, so the seeded baseline graph is never
// touched. Run at suite start.
//
// The deletes run child-before-parent because every workspace-child FK is ON DELETE NO ACTION (no
// cascade): turns→conversations and connector_syncs→connectors are the grandchildren, then the four
// direct children (documents, conversations, connectors, workspace_teams), then the workspaces.
func ReconcileStaleWorkspaces(
	ctx context.Context,
	db *sql.DB,
	orgID string,
	staleAfter time.Duration,
) error {
	cutoff := fmt.Sprintf("%d seconds", int(staleAfter.Seconds()))
	// The stale-workspace id set — repeated as a subquery in each child delete so all deletes share
	// the same ($1 org, $2 name-prefix, $3 cutoff) scope.
	const staleWS = `SELECT id FROM workspaces
		WHERE organization_id = $1 AND name LIKE $2 AND created_at < now() - $3::interval`

	// Ordered deepest-FK-child first; the final entry is the workspaces themselves.
	deletes := []struct{ what, query string }{
		{
			"turns",
			`DELETE FROM turns WHERE conversation_id IN (SELECT id FROM conversations WHERE workspace_id IN (` + staleWS + `))`,
		},
		{
			"conversations",
			`DELETE FROM conversations WHERE workspace_id IN (` + staleWS + `)`,
		},
		{
			"connector syncs",
			`DELETE FROM connector_syncs WHERE connector_id IN (SELECT id FROM connectors WHERE workspace_id IN (` + staleWS + `))`,
		},
		{"documents", `DELETE FROM documents WHERE workspace_id IN (` + staleWS + `)`},
		{"connectors", `DELETE FROM connectors WHERE workspace_id IN (` + staleWS + `)`},
		{
			"workspace teams",
			`DELETE FROM workspace_teams WHERE workspace_id IN (` + staleWS + `)`,
		},
		{
			"workspaces",
			`DELETE FROM workspaces WHERE organization_id = $1 AND name LIKE $2 AND created_at < now() - $3::interval`,
		},
	}
	for _, d := range deletes {
		if _, err := db.ExecContext(
			ctx,
			d.query,
			orgID,
			E2EWorkspacePrefix+"%",
			cutoff,
		); err != nil {
			return apperr.Wrap(
				err,
				apperr.CodeInternal,
				"e2ekit: reconcile stale e2e "+d.what,
			)
		}
	}
	return nil
}

// OpenDB opens the harness's Postgres handle for worker-effect seed/poll/teardown, using the pgx
// stdlib driver already vendored for the migrate service. url is config.DatabaseURL("").
func OpenDB(url string) (*sql.DB, error) {
	db, err := sql.Open("pgx", url)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInternal, "e2ekit: open db")
	}
	return db, nil
}

// PollUntil calls check every 2s until it returns (true, nil) or the deadline (~timeout) elapses,
// returning the last error/miss. It is the worker-effect analogue of PollTraceInTempo: an async
// worker's "endpoint" is a downstream state change, so every worker case polls for that state.
func PollUntil(
	ctx context.Context,
	timeout time.Duration,
	what string,
	check func(context.Context) (bool, error),
) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		ok, err := check(ctx)
		if ok {
			return nil
		}
		lastErr = err
		if time.Now().After(deadline) {
			if lastErr != nil {
				return apperr.Wrap(lastErr, apperr.CodeUnavailable, "e2ekit: "+what)
			}
			return apperr.New(
				apperr.CodeUnavailable,
				"e2ekit: "+what+" did not materialize within "+timeout.String(),
			)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// ResolveInternalIDs returns the seeded (userID, orgID) internal UUIDs for the given org external id
// and user external subject — the tenant-scoped ids the worker events/rows reference (the API headers
// carry external ids; the DB rows carry internal ids). Errors if either row is missing (seed them first).
func ResolveInternalIDs(
	ctx context.Context,
	db *sql.DB,
	orgExternalID, userSub string,
) (userID, orgID string, err error) {
	// users carry external_id (the Auth0 sub) and link to orgs via organization_members, not a column,
	// so resolve each id by its own external id rather than a join.
	if err := db.QueryRowContext(
		ctx,
		`SELECT id FROM users WHERE external_id = $1 AND deleted_at IS NULL LIMIT 1`,
		userSub,
	).Scan(&userID); err != nil {
		return "", "", apperr.Wrap(
			err,
			apperr.CodeNotFound,
			"e2ekit: resolve seeded user (sub="+userSub+")",
		)
	}
	if err := db.QueryRowContext(
		ctx,
		`SELECT id FROM organizations WHERE external_id = $1 LIMIT 1`,
		orgExternalID,
	).Scan(&orgID); err != nil {
		return "", "", apperr.Wrap(
			err,
			apperr.CodeNotFound,
			"e2ekit: resolve seeded org (external="+orgExternalID+")",
		)
	}
	return userID, orgID, nil
}

// SeedWorkspace inserts a per-run workspace owned by orgID and returns its id, so a case has an
// isolated workspace to work in (org-admin sees all org workspaces in dev). The caller tears it down.
func SeedWorkspace(ctx context.Context, db *sql.DB, orgID, name string) (string, error) {
	var id string
	if err := db.QueryRowContext(
		ctx,
		`INSERT INTO workspaces (id,name,organization_id,active,version,created_at,updated_at)
		 VALUES (gen_random_uuid(),$1,$2,true,1,now(),now()) RETURNING id`,
		name,
		orgID,
	).Scan(&id); err != nil {
		return "", apperr.Wrap(err, apperr.CodeInternal, "e2ekit: seed workspace")
	}
	return id, nil
}

// APIClient drives an API's document HTTP endpoints on its internal port with INJECTED dev identity
// headers — the internal lane that bypasses the edge gateway, valid under dev STUB_AUTH. The
// edge/UI real-Auth0 flow is out of scope; this is the harness's internal seed path. It is NOT a production client.
type APIClient struct {
	http      *http.Client
	base      string // e.g. http://localhost:8091
	sub       string // X-User-Sub  (the seeded dev user's external subject)
	org       string // X-Org-ID    (the org external id)
	clearance string // X-Clearance-Level
}

// NewAPIClient builds the injected-dev-header API client at base for the seeded (sub, org) identity.
func NewAPIClient(base, sub, org string) *APIClient {
	return &APIClient{
		base:      base,
		sub:       sub,
		org:       org,
		clearance: "confidential",
		http:      &http.Client{Timeout: 30 * time.Second},
	}
}

// Sub returns the seeded user's external subject (the X-User-Sub the client injects) — a read
// accessor so callers in other packages can resolve the same identity's internal ids.
func (a *APIClient) Sub() string { return a.sub }

// headers returns the identity headers the API auth interceptor consumes under dev STUB_AUTH.
func (a *APIClient) headers() map[string]string {
	return map[string]string{
		"X-User-Sub":        a.sub,
		"X-Org-ID":          a.org,
		"X-Clearance-Level": a.clearance,
		"X-Roles":           "admin",
		"Content-Type":      "application/json",
	}
}

// Presign creates the pending document + presigned PUT URL. Returns the document id, upload URL, and
// the signed content type the caller MUST echo on the PUT.
func (a *APIClient) Presign(
	ctx context.Context,
	workspaceID, filename, classification string,
	sizeBytes int,
) (docID, uploadURL, contentType string, err error) {
	body := map[string]any{
		"orgId":          a.org,
		"filename":       filename,
		"sizeBytes":      sizeBytes,
		"classification": classification,
	}
	var out struct {
		Meta struct {
			DocumentID  string `json:"documentId"`
			UploadURL   string `json:"uploadUrl"`
			ContentType string `json:"contentType"`
		} `json:"meta"`
	}
	if err := a.doJSON(
		ctx,
		http.MethodPost,
		fmt.Sprintf(
			"%s/api/v1/workspaces/%s/documents/upload-url",
			a.base,
			workspaceID,
		),
		body,
		&out,
		nil,
	); err != nil {
		return "", "", "", err
	}
	return out.Meta.DocumentID, out.Meta.UploadURL, out.Meta.ContentType, nil
}

// PutBytes uploads content to a presigned PUT URL, echoing the signed content type (a plain HTTP PUT
// to the object store, no identity headers — the URL is pre-authorized).
func (a *APIClient) PutBytes(
	ctx context.Context,
	url, contentType string,
	content []byte,
) error {
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPut,
		url,
		bytes.NewReader(content),
	)
	if err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "e2ekit: build PUT")
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := a.http.Do(req)
	if err != nil {
		return apperr.Wrap(err, apperr.CodeUnavailable, "e2ekit: S3 PUT")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		return apperr.New(
			apperr.CodeInternal,
			fmt.Sprintf("e2ekit: S3 PUT status %d", resp.StatusCode),
		)
	}
	return nil
}

// Confirm runs the trust-&-verify confirm (the sole ingestion trigger for the direct-PUT path) and
// returns the trace id from the response's traceresponse header so the caller can correlate
// the resulting ingestion in Tempo.
func (a *APIClient) Confirm(
	ctx context.Context,
	workspaceID, docID string,
) (traceID string, err error) {
	var hdr http.Header
	if err := a.doJSON(
		ctx,
		http.MethodPost,
		fmt.Sprintf(
			"%s/api/v1/workspaces/%s/documents/%s/confirm",
			a.base,
			workspaceID,
			docID,
		),
		map[string]any{"orgId": a.org},
		nil,
		&hdr,
	); err != nil {
		return "", err
	}
	return TraceIDFromHeader(hdr.Get("traceresponse")), nil
}

// Status returns the document's current lifecycle status (e.g. DOCUMENT_STATUS_INDEXED).
func (a *APIClient) Status(
	ctx context.Context,
	workspaceID, docID string,
) (string, error) {
	var out struct {
		Data struct {
			Attributes struct {
				Status string `json:"status"`
			} `json:"attributes"`
		} `json:"data"`
	}
	if err := a.doJSON(
		ctx,
		http.MethodGet,
		fmt.Sprintf(
			"%s/api/v1/workspaces/%s/documents/%s?orgId=%s",
			a.base,
			workspaceID,
			docID,
			a.org,
		),
		nil,
		&out,
		nil,
	); err != nil {
		return "", err
	}
	return out.Data.Attributes.Status, nil
}

// doJSON performs a JSON request with the dev identity headers, decoding a JSON response into out (when
// non-nil) and capturing the response header into hdr (when non-nil). A >=300 status is an error.
func (a *APIClient) doJSON(
	ctx context.Context,
	method, url string,
	body, out any,
	hdr *http.Header,
) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return apperr.Wrap(err, apperr.CodeInternal, "e2ekit: marshal request")
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "e2ekit: build request")
	}
	for k, v := range a.headers() {
		req.Header.Set(k, v)
	}
	resp, err := a.http.Do(req)
	if err != nil {
		return apperr.Wrap(err, apperr.CodeUnavailable, "e2ekit: request")
	}
	defer func() { _ = resp.Body.Close() }()
	if hdr != nil {
		*hdr = resp.Header
	}
	if resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return apperr.New(
			apperr.CodeInternal,
			fmt.Sprintf(
				"e2ekit: %s %s → %d: %s",
				method,
				url,
				resp.StatusCode,
				snippet,
			),
		)
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return apperr.Wrap(err, apperr.CodeInternal, "e2ekit: decode response")
		}
	}
	return nil
}
