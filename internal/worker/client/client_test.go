package client_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/luckydiss/studlance_ai/internal/httpapi"
	"github.com/luckydiss/studlance_ai/internal/worker/client"
)

func writeJSON(w http.ResponseWriter, status int, v string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, v)
}

func checkAuth(t *testing.T, r *http.Request) {
	t.Helper()
	if got := r.Header.Get("Authorization"); got != "Bearer tok" {
		t.Errorf("Authorization = %q, want Bearer tok", got)
	}
}

func TestRegisterClaimHeartbeat(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/worker/register", func(w http.ResponseWriter, r *http.Request) {
		checkAuth(t, r)
		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("register body: %v", err)
		}
		if caps, ok := body["capabilities"].([]interface{}); !ok || len(caps) != 0 {
			t.Errorf("capabilities = %v, want empty array", body["capabilities"])
		}
		if info, ok := body["info"].(map[string]interface{}); !ok || len(info) != 0 {
			t.Errorf("info = %v, want empty object", body["info"])
		}
		writeJSON(w, http.StatusOK, `{"worker_id":"w1"}`)
	})
	var claimCalls int32
	mux.HandleFunc("POST /api/worker/claim", func(w http.ResponseWriter, r *http.Request) {
		checkAuth(t, r)
		if atomic.AddInt32(&claimCalls, 1) == 1 {
			writeJSON(w, http.StatusOK, `{"action":"start","attempt":1,"epoch":3,"job_id":"j1",`+
				`"lease_expires_at":"2025-01-01T00:00:00Z","prompt":"сделай","stage":"draft","version":1}`)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /api/worker/jobs/{id}/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		checkAuth(t, r)
		if id := r.PathValue("id"); id != "j1" {
			t.Errorf("heartbeat id = %q, want j1", id)
		}
		var body struct {
			Epoch int `json:"epoch"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("heartbeat body: %v", err)
		}
		if body.Epoch != 3 {
			t.Errorf("heartbeat epoch = %d, want 3", body.Epoch)
		}
		writeJSON(w, http.StatusOK, `{"lease_expires_at":"2025-01-01T00:01:00Z","cancel":true}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := client.New(srv.URL, "tok")
	ctx := context.Background()

	id, err := c.Register(ctx, nil, nil)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if id != "w1" {
		t.Fatalf("Register id = %q, want w1", id)
	}

	asn, err := c.Claim(ctx)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if asn == nil || asn.JobId != "j1" || asn.Epoch != 3 || asn.Prompt != "сделай" {
		t.Fatalf("Claim assignment = %+v", asn)
	}

	asn, err = c.Claim(ctx)
	if err != nil {
		t.Fatalf("Claim 204: %v", err)
	}
	if asn != nil {
		t.Fatalf("Claim 204 assignment = %+v, want nil", asn)
	}

	hb, err := c.Heartbeat(ctx, "j1", 3)
	if err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	if !hb.Cancel || hb.LeaseExpiresAt.IsZero() {
		t.Fatalf("Heartbeat response = %+v", hb)
	}
}

func TestGetInputPathEscaping(t *testing.T) {
	const wantPath = "revision-2/папка/файл.pdf"
	mux := http.NewServeMux()
	// The {path} wildcard matches exactly one segment: if the client sent
	// decoded slashes this handler would not match at all (404).
	mux.HandleFunc("GET /api/worker/jobs/{id}/input/{path}", func(w http.ResponseWriter, r *http.Request) {
		checkAuth(t, r)
		if got := r.URL.EscapedPath(); !strings.Contains(got, "%2F") {
			t.Errorf("EscapedPath = %q, want %%2F for nested path", got)
		}
		if got := r.PathValue("path"); got != wantPath {
			t.Errorf("PathValue(path) = %q, want %q", got, wantPath)
		}
		if got := r.URL.Query().Get("epoch"); got != "7" {
			t.Errorf("epoch = %q, want 7", got)
		}
		_, _ = io.WriteString(w, "PDF-BYTES")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := client.New(srv.URL, "tok")

	var buf strings.Builder
	if err := c.GetInput(context.Background(), "j1", 7, wantPath, &buf); err != nil {
		t.Fatalf("GetInput: %v", err)
	}
	if buf.String() != "PDF-BYTES" {
		t.Fatalf("GetInput bytes = %q", buf.String())
	}
}

func TestErrorClassification(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/worker/jobs/{id}/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusConflict, `{"error":{"code":"stale_lease","message":"эпоха устарела"}}`)
	})
	mux.HandleFunc("POST /api/worker/jobs/{id}/state", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusConflict, `{"error":{"code":"job_canceled","message":"заказ отменён"}}`)
	})
	mux.HandleFunc("POST /api/worker/register", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusUnauthorized, `{"error":{"code":"unauthorized","message":"bad token"}}`)
	})
	mux.HandleFunc("GET /api/worker/jobs/{id}/input", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusNotFound, `{"error":{"code":"not_found","message":"нет такого"}}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := client.New(srv.URL, "tok")
	ctx := context.Background()

	_, err := c.Heartbeat(ctx, "j1", 1)
	if !errors.Is(err, client.ErrStaleLease) {
		t.Fatalf("stale lease err = %v, want ErrStaleLease", err)
	}

	err = c.SetState(ctx, "j1", 1, map[string]interface{}{"k": "v"})
	var ce *client.ConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("conflict err = %T %v, want ConflictError", err, err)
	}
	if ce.Message != "заказ отменён" {
		t.Fatalf("ConflictError.Message = %q", ce.Message)
	}

	_, err = c.Register(ctx, []string{"tex"}, nil)
	if !errors.Is(err, client.ErrUnauthorized) {
		t.Fatalf("401 err = %v, want ErrUnauthorized", err)
	}

	_, err = c.ListInput(ctx, "j1", 1)
	if !errors.Is(err, client.ErrNotFound) {
		t.Fatalf("404 err = %v, want ErrNotFound", err)
	}
}

func TestRetryOn5xx(t *testing.T) {
	var attempts int32
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/worker/register", func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&attempts, 1) < 3 {
			writeJSON(w, http.StatusInternalServerError, `{"error":{"code":"boom","message":"boom"}}`)
			return
		}
		writeJSON(w, http.StatusOK, `{"worker_id":"w1"}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var sleeps []time.Duration
	c := client.NewForTest(srv.URL, "tok", func(_ context.Context, d time.Duration) error {
		sleeps = append(sleeps, d)
		return nil
	})
	id, err := c.Register(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if id != "w1" {
		t.Fatalf("Register id = %q", id)
	}
	if got := atomic.LoadInt32(&attempts); got != 3 {
		t.Fatalf("attempts = %d, want 3", got)
	}
	if len(sleeps) != 2 || sleeps[0] != 2*time.Second || sleeps[1] != 4*time.Second {
		t.Fatalf("sleeps = %v, want [2s 4s]", sleeps)
	}
}

func TestContextCancelDuringBackoff(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/worker/register", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusInternalServerError, `{"error":{"code":"boom","message":"boom"}}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := client.New(srv.URL, "tok") // real backoff sleep

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := c.Register(ctx, nil, nil)
	if err == nil {
		t.Fatal("Register err = nil, want error")
	}
	if elapsed := time.Since(start); elapsed >= 2*time.Second {
		t.Fatalf("Register took %v, want fast return on ctx cancel", elapsed)
	}
}

func TestUploadNoRetryOnNetworkError(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		_, _ = io.Copy(io.Discard, r.Body)
		// Close the connection without a response: a network error.
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Error("no hijacker")
			return
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		_ = conn.Close()
	}))
	defer srv.Close()

	const payload = "PNG-PNG-PNG"
	cr := &countingReader{r: strings.NewReader(payload)}
	c := client.New(srv.URL, "tok")
	err := c.PutSnapshotFile(context.Background(), "j1", "draft", 1, "a.png", cr)
	if err == nil {
		t.Fatal("PutSnapshotFile err = nil, want network error")
	}
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Fatalf("attempts = %d, want 1 (no retry of reader bodies)", got)
	}
	if cr.bytes != len(payload) {
		t.Fatalf("reader consumed %d bytes, want %d (exactly once)", cr.bytes, len(payload))
	}
}

type countingReader struct {
	r     *strings.Reader
	reads int
	bytes int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if n > 0 {
		c.reads++
		c.bytes += n
	}
	return n, err
}

// TestEndpoints checks the method, path and query of every remaining
// endpoint against a recording stub.
func TestEndpoints(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name       string
		wantMethod string
		wantPath   string
		wantQuery  url.Values
		wantBody   string
		status     int
		resp       string
		call       func(t *testing.T, c *client.Client)
	}{
		{
			name:       "ListInput",
			wantMethod: http.MethodGet,
			wantPath:   "/api/worker/jobs/j1/input",
			wantQuery:  url.Values{"epoch": {"2"}},
			status:     http.StatusOK,
			resp:       `{"files":[{"path":"a.txt","size":3,"sha256":"deadbeef","revision":0}]}`,
			call: func(t *testing.T, c *client.Client) {
				files, err := c.ListInput(ctx, "j1", 2)
				if err != nil {
					t.Fatalf("ListInput: %v", err)
				}
				if len(files) != 1 || files[0].Path != "a.txt" || files[0].Sha256 != "deadbeef" {
					t.Fatalf("ListInput files = %+v", files)
				}
			},
		},
		{
			name:       "CreateRun",
			wantMethod: http.MethodPost,
			wantPath:   "/api/worker/jobs/j1/runs",
			wantQuery:  url.Values{},
			status:     http.StatusCreated,
			resp:       `{"run_id":"r9"}`,
			call: func(t *testing.T, c *client.Client) {
				id, err := c.CreateRun(ctx, "j1", httpapi.CreateRunRequest{
					Epoch: 2, Agent: httpapi.CreateRunRequestAgent("claude"),
					Stage: httpapi.CreateRunRequestStage("draft"), Version: 1, Attempt: 1,
				})
				if err != nil {
					t.Fatalf("CreateRun: %v", err)
				}
				if id != "r9" {
					t.Fatalf("CreateRun id = %q, want r9", id)
				}
			},
		},
		{
			name:       "PatchRun",
			wantMethod: http.MethodPatch,
			wantPath:   "/api/worker/jobs/j1/runs/r9",
			wantQuery:  url.Values{},
			status:     http.StatusNoContent,
			call: func(t *testing.T, c *client.Client) {
				if err := c.PatchRun(ctx, "j1", "r9", httpapi.PatchRunRequest{Epoch: 2}); err != nil {
					t.Fatalf("PatchRun: %v", err)
				}
			},
		},
		{
			name:       "AppendSteps",
			wantMethod: http.MethodPost,
			wantPath:   "/api/worker/jobs/j1/runs/r9/steps",
			wantQuery:  url.Values{},
			status:     http.StatusNoContent,
			call: func(t *testing.T, c *client.Client) {
				err := c.AppendSteps(ctx, "j1", "r9", 2, []httpapi.NewTraceStep{{Seq: 1, Summary: "s", Type: "note"}})
				if err != nil {
					t.Fatalf("AppendSteps: %v", err)
				}
			},
		},
		{
			name:       "PutRunLog",
			wantMethod: http.MethodPut,
			wantPath:   "/api/worker/jobs/j1/runs/r9/log",
			wantQuery:  url.Values{"epoch": {"2"}},
			wantBody:   "LOG-BYTES",
			status:     http.StatusNoContent,
			call: func(t *testing.T, c *client.Client) {
				if err := c.PutRunLog(ctx, "j1", "r9", 2, strings.NewReader("LOG-BYTES")); err != nil {
					t.Fatalf("PutRunLog: %v", err)
				}
			},
		},
		{
			name:       "SetState",
			wantMethod: http.MethodPost,
			wantPath:   "/api/worker/jobs/j1/state",
			wantQuery:  url.Values{},
			status:     http.StatusNoContent,
			call: func(t *testing.T, c *client.Client) {
				if err := c.SetState(ctx, "j1", 2, map[string]interface{}{"k": "v"}); err != nil {
					t.Fatalf("SetState: %v", err)
				}
			},
		},
		{
			name:       "PutSnapshotFile",
			wantMethod: http.MethodPut,
			wantPath:   "/api/worker/jobs/j1/snapshot/draft/files",
			wantQuery:  url.Values{"epoch": {"2"}, "path": {"preview/записка.pdf"}},
			wantBody:   "DOC",
			status:     http.StatusNoContent,
			call: func(t *testing.T, c *client.Client) {
				err := c.PutSnapshotFile(ctx, "j1", "draft", 2, "preview/записка.pdf", strings.NewReader("DOC"))
				if err != nil {
					t.Fatalf("PutSnapshotFile: %v", err)
				}
			},
		},
		{
			name:       "PutSnapshotPage",
			wantMethod: http.MethodPut,
			wantPath:   "/api/worker/jobs/j1/snapshot/v2/pages/1/3",
			wantQuery:  url.Values{"epoch": {"2"}},
			wantBody:   "PNG",
			status:     http.StatusNoContent,
			call: func(t *testing.T, c *client.Client) {
				if err := c.PutSnapshotPage(ctx, "j1", "v2", 2, 1, 3, strings.NewReader("PNG")); err != nil {
					t.Fatalf("PutSnapshotPage: %v", err)
				}
			},
		},
		{
			name:       "PutSnapshotThumb",
			wantMethod: http.MethodPut,
			wantPath:   "/api/worker/jobs/j1/snapshot/v2/thumbs/0/2",
			wantQuery:  url.Values{"epoch": {"2"}},
			wantBody:   "PNG",
			status:     http.StatusNoContent,
			call: func(t *testing.T, c *client.Client) {
				if err := c.PutSnapshotThumb(ctx, "j1", "v2", 2, 0, 2, strings.NewReader("PNG")); err != nil {
					t.Fatalf("PutSnapshotThumb: %v", err)
				}
			},
		},
		{
			name:       "PutRevisionRemark",
			wantMethod: http.MethodPut,
			wantPath:   "/api/worker/jobs/j1/input/revision/2/remarks/5",
			wantQuery:  url.Values{"epoch": {"2"}},
			wantBody:   "PNG",
			status:     http.StatusNoContent,
			call: func(t *testing.T, c *client.Client) {
				if err := c.PutRevisionRemark(ctx, "j1", 2, 2, 5, strings.NewReader("PNG")); err != nil {
					t.Fatalf("PutRevisionRemark: %v", err)
				}
			},
		},
		{
			name:       "CommitSnapshot",
			wantMethod: http.MethodPost,
			wantPath:   "/api/worker/jobs/j1/snapshot/draft/commit",
			wantQuery:  url.Values{},
			status:     http.StatusNoContent,
			call: func(t *testing.T, c *client.Client) {
				err := c.CommitSnapshot(ctx, "j1", "draft", httpapi.CommitSnapshotRequest{
					Epoch:     2,
					Documents: []httpapi.SnapshotDocument{{Idx: 0, Title: "t", Kind: "docx", FilePath: "t.docx"}},
				})
				if err != nil {
					t.Fatalf("CommitSnapshot: %v", err)
				}
			},
		},
		{
			name:       "Question",
			wantMethod: http.MethodPost,
			wantPath:   "/api/worker/jobs/j1/question",
			wantQuery:  url.Values{},
			status:     http.StatusNoContent,
			call: func(t *testing.T, c *client.Client) {
				if err := c.Question(ctx, "j1", 2, "как?"); err != nil {
					t.Fatalf("Question: %v", err)
				}
			},
		},
		{
			name:       "Finish",
			wantMethod: http.MethodPost,
			wantPath:   "/api/worker/jobs/j1/finish",
			wantQuery:  url.Values{},
			status:     http.StatusNoContent,
			call: func(t *testing.T, c *client.Client) {
				err := c.Finish(ctx, "j1", httpapi.FinishRequest{Epoch: 2, Outcome: httpapi.FinishRequestOutcome("ok")})
				if err != nil {
					t.Fatalf("Finish: %v", err)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotMethod, gotPath, gotBody string
			var gotQuery url.Values
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				checkAuth(t, r)
				gotMethod, gotPath, gotQuery = r.Method, r.URL.Path, r.URL.Query()
				b, _ := io.ReadAll(r.Body)
				gotBody = string(b)
				if tc.resp != "" {
					writeJSON(w, tc.status, tc.resp)
					return
				}
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()
			tc.call(t, client.New(srv.URL, "tok"))
			if gotMethod != tc.wantMethod {
				t.Errorf("method = %q, want %q", gotMethod, tc.wantMethod)
			}
			if gotPath != tc.wantPath {
				t.Errorf("path = %q, want %q", gotPath, tc.wantPath)
			}
			for k := range tc.wantQuery {
				if got := gotQuery.Get(k); got != tc.wantQuery.Get(k) {
					t.Errorf("query %s = %q, want %q", k, got, tc.wantQuery.Get(k))
				}
			}
			if tc.wantBody != "" && gotBody != tc.wantBody {
				t.Errorf("body = %q, want %q", gotBody, tc.wantBody)
			}
		})
	}
}
