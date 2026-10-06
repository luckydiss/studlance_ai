// Package client implements the worker-side HTTP client for the server's
// /api/worker endpoints (04-api.md): registration, job claiming via
// long-poll, heartbeats and all run/snapshot uploads.
//
// Requests with byte-backed (JSON or empty) bodies are retried on network
// errors and 5xx responses with exponential backoff (2 s, 4 s, 8 s, ...
// capped at 60 s) while the caller's context is alive. Uploads with
// io.Reader bodies are never retried: the reader is consumed by the first
// attempt, so the caller must reopen the source and retry itself. 4xx
// responses are classified into the sentinel errors and typed errors below.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/luckydiss/studlance_ai/internal/httpapi"
)

const (
	backoffInitial = 2 * time.Second
	backoffMax     = 60 * time.Second
)

// ErrStaleLease is returned on 409 responses with code stale_lease: the
// worker's epoch no longer matches the job's lease.
var ErrStaleLease = errors.New("worker client: stale lease")

// ErrUnauthorized is returned on 401 responses (bad worker token).
var ErrUnauthorized = errors.New("worker client: unauthorized")

// ErrNotFound is returned on 404 responses.
var ErrNotFound = errors.New("worker client: not found")

// ConflictError is a 409 response with any code other than stale_lease;
// Message is the server-provided, client-facing message.
type ConflictError struct{ Message string }

func (e *ConflictError) Error() string { return "worker client: conflict: " + e.Message }

// BadRequestError is a 400 or 413 response; Message is server-provided.
type BadRequestError struct{ Message string }

func (e *BadRequestError) Error() string { return "worker client: bad request: " + e.Message }

// Client calls the server's /api/worker endpoints.
type Client struct {
	base  string
	token string
	hc    *http.Client
	sleep func(ctx context.Context, d time.Duration) error
}

// New returns a Client for baseURL (the server root, e.g.
// "http://host:8080") authenticated with the worker token. The underlying
// http.Client has no overall Timeout because claim long-polls up to 25 s;
// deadlines come from the caller's context.
func New(baseURL, token string) *Client {
	return &Client{
		base:  strings.TrimRight(baseURL, "/"),
		token: token,
		hc:    &http.Client{},
		sleep: sleepCtx,
	}
}

// NewForTest is New with an injectable backoff sleep hook; a nil sleep uses
// the real clock.
func NewForTest(baseURL, token string, sleep func(context.Context, time.Duration) error) *Client {
	c := New(baseURL, token)
	if sleep != nil {
		c.sleep = sleep
	}
	return c
}

// sleepCtx waits for d or until ctx is done.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Register implements POST /api/worker/register and returns the worker id.
func (c *Client) Register(ctx context.Context, capabilities []string, info map[string]interface{}) (string, error) {
	if capabilities == nil {
		capabilities = []string{}
	}
	if info == nil {
		info = map[string]interface{}{}
	}
	var out httpapi.WorkerRegisterResponse
	_, err := c.call(ctx, http.MethodPost, c.base+"/api/worker/register",
		httpapi.WorkerRegisterRequest{Capabilities: capabilities, Info: info}, &out)
	if err != nil {
		return "", err
	}
	return out.WorkerId, nil
}

// Claim implements POST /api/worker/claim (long-poll, no client-side
// timeout). It returns (nil, nil) when no job is available (204).
func (c *Client) Claim(ctx context.Context) (*httpapi.Assignment, error) {
	var out httpapi.Assignment
	status, err := c.call(ctx, http.MethodPost, c.base+"/api/worker/claim", nil, &out)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNoContent {
		return nil, nil
	}
	return &out, nil
}

// Heartbeat implements POST /api/worker/jobs/{id}/heartbeat.
func (c *Client) Heartbeat(ctx context.Context, jobID string, epoch int) (httpapi.HeartbeatResponse, error) {
	var out httpapi.HeartbeatResponse
	_, err := c.call(ctx, http.MethodPost, c.jobURL(jobID, "/heartbeat"),
		httpapi.EpochRequest{Epoch: epoch}, &out)
	return out, err
}

// ListInput implements GET /api/worker/jobs/{id}/input.
func (c *Client) ListInput(ctx context.Context, jobID string, epoch int) ([]httpapi.WorkerInputFile, error) {
	var out httpapi.WorkerInputFileList
	if _, err := c.call(ctx, http.MethodGet, epochQuery(c.jobURL(jobID, "/input"), epoch), nil, &out); err != nil {
		return nil, err
	}
	return out.Files, nil
}

// GetInput implements GET /api/worker/jobs/{id}/input/{path} and streams the
// file bytes into w. The file path travels as a single URL segment: slashes
// inside path are sent percent-encoded (%2F) and decoded by the server.
func (c *Client) GetInput(ctx context.Context, jobID string, epoch int, path string, w io.Writer) error {
	// Parse explicitly so url.URL keeps RawPath and the encoded %2F
	// segments survive into the request line.
	u, err := url.Parse(epochQuery(c.jobURL(jobID, "/input/"+url.PathEscape(path)), epoch))
	if err != nil {
		return err
	}
	resp, err := c.do(ctx, http.MethodGet, u.String(), func() io.Reader { return nil }, "", true)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return responseError(resp)
	}
	_, err = io.Copy(w, resp.Body)
	return err
}

// CreateRun implements POST /api/worker/jobs/{id}/runs and returns the run id.
func (c *Client) CreateRun(ctx context.Context, jobID string, req httpapi.CreateRunRequest) (string, error) {
	var out httpapi.CreateRunResponse
	if _, err := c.call(ctx, http.MethodPost, c.jobURL(jobID, "/runs"), req, &out); err != nil {
		return "", err
	}
	return out.RunId, nil
}

// PatchRun implements PATCH /api/worker/jobs/{id}/runs/{run_id}.
func (c *Client) PatchRun(ctx context.Context, jobID, runID string, req httpapi.PatchRunRequest) error {
	_, err := c.call(ctx, http.MethodPatch, c.jobURL(jobID, "/runs/"+url.PathEscape(runID)), req, nil)
	return err
}

// AppendSteps implements POST /api/worker/jobs/{id}/runs/{run_id}/steps.
func (c *Client) AppendSteps(ctx context.Context, jobID, runID string, epoch int, steps []httpapi.NewTraceStep) error {
	_, err := c.call(ctx, http.MethodPost, c.jobURL(jobID, "/runs/"+url.PathEscape(runID)+"/steps"),
		httpapi.AppendStepsRequest{Epoch: epoch, Steps: steps}, nil)
	return err
}

// PutRunLog implements PUT /api/worker/jobs/{id}/runs/{run_id}/log.
func (c *Client) PutRunLog(ctx context.Context, jobID, runID string, epoch int, r io.Reader) error {
	return c.upload(ctx, epochQuery(c.jobURL(jobID, "/runs/"+url.PathEscape(runID)+"/log"), epoch),
		"application/octet-stream", r)
}

// SetState implements POST /api/worker/jobs/{id}/state (JSON-merge).
func (c *Client) SetState(ctx context.Context, jobID string, epoch int, state map[string]interface{}) error {
	_, err := c.call(ctx, http.MethodPost, c.jobURL(jobID, "/state"),
		httpapi.SetStateRequest{Epoch: epoch, State: state}, nil)
	return err
}

// PutSnapshotFile implements PUT
// /api/worker/jobs/{id}/snapshot/{snapshot}/files with the file's path
// relative to out/ as a query parameter.
func (c *Client) PutSnapshotFile(ctx context.Context, jobID, snapshot string, epoch int, path string, r io.Reader) error {
	raw := epochQuery(c.jobURL(jobID, "/snapshot/"+url.PathEscape(snapshot)+"/files"), epoch) +
		"&path=" + url.QueryEscape(path)
	return c.upload(ctx, raw, "application/octet-stream", r)
}

// PutSnapshotPage implements PUT
// /api/worker/jobs/{id}/snapshot/{snapshot}/pages/{document_idx}/{page}.
func (c *Client) PutSnapshotPage(ctx context.Context, jobID, snapshot string, epoch, docIdx, page int, r io.Reader) error {
	return c.upload(ctx, epochQuery(c.jobURL(jobID, "/snapshot/"+url.PathEscape(snapshot)+
		"/pages/"+strconv.Itoa(docIdx)+"/"+strconv.Itoa(page)), epoch), "image/png", r)
}

// PutSnapshotThumb implements PUT
// /api/worker/jobs/{id}/snapshot/{snapshot}/thumbs/{document_idx}/{page}.
func (c *Client) PutSnapshotThumb(ctx context.Context, jobID, snapshot string, epoch, docIdx, page int, r io.Reader) error {
	return c.upload(ctx, epochQuery(c.jobURL(jobID, "/snapshot/"+url.PathEscape(snapshot)+
		"/thumbs/"+strconv.Itoa(docIdx)+"/"+strconv.Itoa(page)), epoch), "image/png", r)
}

// PutRevisionRemark implements PUT
// /api/worker/jobs/{id}/input/revision/{n}/remarks/{idx}.
func (c *Client) PutRevisionRemark(ctx context.Context, jobID string, epoch, n, idx int, r io.Reader) error {
	return c.upload(ctx, epochQuery(c.jobURL(jobID,
		"/input/revision/"+strconv.Itoa(n)+"/remarks/"+strconv.Itoa(idx)), epoch), "image/png", r)
}

// CommitSnapshot implements POST
// /api/worker/jobs/{id}/snapshot/{snapshot}/commit.
func (c *Client) CommitSnapshot(ctx context.Context, jobID, snapshot string, req httpapi.CommitSnapshotRequest) error {
	_, err := c.call(ctx, http.MethodPost, c.jobURL(jobID, "/snapshot/"+url.PathEscape(snapshot)+"/commit"), req, nil)
	return err
}

// Question implements POST /api/worker/jobs/{id}/question.
func (c *Client) Question(ctx context.Context, jobID string, epoch int, text string) error {
	_, err := c.call(ctx, http.MethodPost, c.jobURL(jobID, "/question"),
		httpapi.QuestionRequest{Epoch: epoch, Text: text}, nil)
	return err
}

// Finish implements POST /api/worker/jobs/{id}/finish.
func (c *Client) Finish(ctx context.Context, jobID string, req httpapi.FinishRequest) error {
	_, err := c.call(ctx, http.MethodPost, c.jobURL(jobID, "/finish"), req, nil)
	return err
}

// jobURL builds "<base>/api/worker/jobs/{id}<suffix>".
func (c *Client) jobURL(jobID, suffix string) string {
	return c.base + "/api/worker/jobs/" + url.PathEscape(jobID) + suffix
}

// epochQuery appends the epoch query parameter to rawURL.
func epochQuery(rawURL string, epoch int) string {
	sep := "?"
	if strings.Contains(rawURL, "?") {
		sep = "&"
	}
	return rawURL + sep + "epoch=" + strconv.Itoa(epoch)
}

// call issues a JSON request (nil in means an empty body) and, on a 2xx
// response, decodes the body into out (nil out or 204 skips decoding). It
// returns the response status code.
func (c *Client) call(ctx context.Context, method, rawURL string, in, out interface{}) (int, error) {
	var body []byte
	if in != nil {
		var err error
		body, err = json.Marshal(in)
		if err != nil {
			return 0, err
		}
	}
	resp, err := c.do(ctx, method, rawURL, func() io.Reader {
		if body == nil {
			return nil
		}
		return bytes.NewReader(body)
	}, "application/json", true)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return resp.StatusCode, responseError(resp)
	}
	if out != nil && resp.StatusCode != http.StatusNoContent {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return resp.StatusCode, fmt.Errorf("worker client: decode %s %s: %w", method, rawURL, err)
		}
	}
	return resp.StatusCode, nil
}

// upload PUTs r as the request body in a single attempt: reader bodies
// cannot be recreated, so network errors and 5xx are returned as-is for the
// caller to retry with a fresh reader.
func (c *Client) upload(ctx context.Context, rawURL, contentType string, r io.Reader) error {
	resp, err := c.do(ctx, http.MethodPut, rawURL, func() io.Reader { return r }, contentType, false)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return responseError(resp)
	}
	return nil
}

// do issues one request. While ctx is alive and retry is true, network
// errors and 5xx responses are retried with exponential backoff; newBody
// must return a fresh body for each attempt. The caller owns and must close
// the returned response body.
func (c *Client) do(ctx context.Context, method, rawURL string, newBody func() io.Reader, contentType string, retry bool) (*http.Response, error) {
	backoff := backoffInitial
	var lastErr error
	for {
		req, err := http.NewRequestWithContext(ctx, method, rawURL, newBody())
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		resp, err := c.hc.Do(req)
		switch {
		case err != nil:
			if !retry || ctx.Err() != nil {
				return nil, err
			}
			lastErr = err
		case retry && resp.StatusCode >= http.StatusInternalServerError:
			lastErr = fmt.Errorf("worker client: %s %s: server status %d", method, rawURL, resp.StatusCode)
			resp.Body.Close()
		default:
			return resp, nil
		}
		if err := c.sleep(ctx, backoff); err != nil {
			return nil, lastErr
		}
		backoff = min(backoff*2, backoffMax)
	}
}

// IsRetryable reports whether err is worth retrying: network errors and 5xx
// are transient; 4xx (stale lease, auth, not found, conflict, bad request)
// are not.
func IsRetryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrStaleLease) || errors.Is(err, ErrUnauthorized) || errors.Is(err, ErrNotFound) {
		return false
	}
	var ce *ConflictError
	if errors.As(err, &ce) {
		return false
	}
	var be *BadRequestError
	if errors.As(err, &be) {
		return false
	}
	return true
}

// responseError maps a non-2xx response to the package's error types. The
// body is consumed by the decoding attempt.
func responseError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var er httpapi.ErrorResponse
	_ = json.Unmarshal(body, &er)
	msg := er.Error.Message
	if msg == "" {
		msg = strings.TrimSpace(string(body))
	}
	if msg == "" {
		msg = http.StatusText(resp.StatusCode)
	}
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return ErrUnauthorized
	case http.StatusNotFound:
		return ErrNotFound
	case http.StatusConflict:
		if er.Error.Code == "stale_lease" {
			return ErrStaleLease
		}
		return &ConflictError{Message: msg}
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge:
		return &BadRequestError{Message: msg}
	default:
		return fmt.Errorf("worker client: status %d: %s", resp.StatusCode, msg)
	}
}
