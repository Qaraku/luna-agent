package httpapi

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Qaraku/luna-agent/internal/store"
	"github.com/Qaraku/luna-agent/internal/workspace"
)

// 首次追加停在存储锁之外，让第二个请求是否读取旧配置成为可观察事件。
type heldSessionConfig struct {
	*store.Store
	mu                                           sync.Mutex
	writes                                       int
	writing                                      bool
	entered, release, committed, overlappingRead chan struct{}
	overlapOnce                                  sync.Once
}

func (s *heldSessionConfig) Read(id string) (store.Session, error) {
	s.mu.Lock()
	if s.writing {
		s.overlapOnce.Do(func() { close(s.overlappingRead) })
	}
	s.mu.Unlock()
	return s.Store.Read(id)
}

func (s *heldSessionConfig) AppendConfig(id string, record store.ConfigRecord) error {
	s.mu.Lock()
	s.writes++
	first := s.writes == 1
	if first {
		s.writing = true
	}
	s.mu.Unlock()
	if first {
		close(s.entered)
		<-s.release
	} else {
		<-s.committed
	}
	err := s.Store.AppendConfig(id, record)
	if first {
		s.mu.Lock()
		s.writing = false
		s.mu.Unlock()
		close(s.committed)
	}
	return err
}

type sessionRequestBody struct {
	*strings.Reader
	ready chan struct{}
	once  sync.Once
}

func (b *sessionRequestBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	if err == io.EOF {
		b.once.Do(func() { close(b.ready) })
	}
	return n, err
}
func (*sessionRequestBody) Close() error { return nil }

func waitSessionConfigStep(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("session configuration request timed out")
	}
}

func sessionConfigHandler(t *testing.T, sessions Sessions) (http.Handler, string) {
	t.Helper()
	spaces, err := workspace.Open(filepath.Join(t.TempDir(), workspace.FileName))
	if err != nil {
		t.Fatal(err)
	}
	space, err := spaces.Create("project", []string{t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	return New(&fakePlugins{state: everyAllowlistedTool()}, fakeRunner{}, sessions, Info{
		BoundHost: "127.0.0.1:43210", Model: "one", ProviderHost: "provider.test",
		Models: []ModelRef{{Name: "one"}, {Name: "two"}},
	}, WithWorkspaces(spaces)), space.ID
}

func TestConcurrentSessionChoicesPreserveBothFields(t *testing.T) {
	for _, firstField := range []string{"model", "workspace"} {
		t.Run(firstField+" first", func(t *testing.T) {
			disk := newTestStore(t)
			id, err := disk.Create("choices")
			if err != nil {
				t.Fatal(err)
			}
			sessions := &heldSessionConfig{Store: disk, entered: make(chan struct{}), release: make(chan struct{}), committed: make(chan struct{}), overlappingRead: make(chan struct{})}
			h, workspaceID := sessionConfigHandler(t, sessions)
			secondField := "workspace"
			if firstField == "workspace" {
				secondField = "model"
			}
			values := map[string]string{"model": "two", "workspace": workspaceID}
			body := func(field string) string { return "{\"" + field + "\":\"" + values[field] + "\"}" }
			unblock := sync.OnceFunc(func() { close(sessions.release) })
			defer unblock()
			firstDone, secondDone := make(chan struct{}), make(chan struct{})
			var first, second *httptest.ResponseRecorder
			go func() {
				first = request(t, h, http.MethodPost, "/api/sessions/"+id+"/"+firstField, body(firstField), true)
				close(firstDone)
			}()
			waitSessionConfigStep(t, sessions.entered)
			ready := make(chan struct{})
			r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:43210/api/sessions/"+id+"/"+secondField, &sessionRequestBody{Reader: strings.NewReader(body(secondField)), ready: ready})
			r.Host = "127.0.0.1:43210"
			r.Header.Set("Origin", "http://127.0.0.1:43210")
			go func() { second = httptest.NewRecorder(); h.ServeHTTP(second, r); close(secondDone) }()
			waitSessionConfigStep(t, ready)
			select {
			case <-sessions.overlappingRead:
			case <-time.After(100 * time.Millisecond):
			}
			unblock()
			waitSessionConfigStep(t, firstDone)
			waitSessionConfigStep(t, secondDone)
			if first.Code != 200 || second.Code != 200 {
				t.Fatalf("statuses=%d,%d", first.Code, second.Code)
			}
			got, err := disk.Read(id)
			if err != nil {
				t.Fatal(err)
			}
			if got.Config == nil || got.Config.Model != "two" || got.Config.Workspace != workspaceID {
				t.Fatalf("concurrent choices lost a field: %+v", got.Config)
			}
		})
	}
}

type faultySessionConfig struct {
	*store.Store
	failRead, failWrite bool
	writes              int
}

func (s *faultySessionConfig) Read(id string) (store.Session, error) {
	if s.failRead {
		s.failRead = false
		return store.Session{}, errors.New("synthetic configuration read failure")
	}
	return s.Store.Read(id)
}
func (s *faultySessionConfig) AppendConfig(id string, record store.ConfigRecord) error {
	s.writes++
	if s.failWrite {
		s.failWrite = false
		return errors.New("synthetic configuration write failure")
	}
	return s.Store.AppendConfig(id, record)
}

func TestFailedSessionChoiceDoesNotAppendOrBlockRetry(t *testing.T) {
	for _, field := range []string{"model", "workspace"} {
		for _, step := range []string{"read", "write"} {
			t.Run(field+"/"+step, func(t *testing.T) {
				disk := newTestStore(t)
				id, err := disk.Create("retry")
				if err != nil {
					t.Fatal(err)
				}
				sessions := &faultySessionConfig{Store: disk, failRead: step == "read", failWrite: step == "write"}
				h, workspaceID := sessionConfigHandler(t, sessions)
				value := "two"
				if field == "workspace" {
					value = workspaceID
				}
				body := "{\"" + field + "\":\"" + value + "\"}"
				path := "/api/sessions/" + id + "/" + field
				first := request(t, h, http.MethodPost, path, body, true)
				if first.Code != 500 {
					t.Errorf("failed %s returned %d, want 500", step, first.Code)
				}
				if step == "read" && sessions.writes != 0 {
					t.Errorf("read failure attempted %d writes", sessions.writes)
				}
				before, err := disk.Read(id)
				if err != nil {
					t.Fatal(err)
				}
				if before.Config != nil {
					t.Errorf("failure changed config: %+v", before.Config)
				}
				done := make(chan struct{})
				var second *httptest.ResponseRecorder
				go func() { second = request(t, h, http.MethodPost, path, body, true); close(done) }()
				waitSessionConfigStep(t, done)
				if second.Code != 200 {
					t.Fatalf("retry returned %d", second.Code)
				}
			})
		}
	}
}

// 已提交的设置不能被慢客户端占住；后续请求可继续读取、合并和追加。
type sessionConfigResponseGate struct {
	*httptest.ResponseRecorder
	entered, release chan struct{}
	once             sync.Once
}

func (w *sessionConfigResponseGate) Write(data []byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	<-w.release
	return w.ResponseRecorder.Write(data)
}

func TestSlowSessionChoiceResponseDoesNotHoldTransaction(t *testing.T) {
	sessions := newTestStore(t)
	id, err := sessions.Create("slow response")
	if err != nil {
		t.Fatal(err)
	}
	h, workspaceID := sessionConfigHandler(t, sessions)
	response := &sessionConfigResponseGate{ResponseRecorder: httptest.NewRecorder(), entered: make(chan struct{}), release: make(chan struct{})}
	unblock := sync.OnceFunc(func() { close(response.release) })
	defer unblock()
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:43210/api/sessions/"+id+"/model", strings.NewReader("{\"model\":\"two\"}"))
	r.Host = "127.0.0.1:43210"
	r.Header.Set("Origin", "http://127.0.0.1:43210")
	firstDone := make(chan struct{})
	go func() { h.ServeHTTP(response, r); close(firstDone) }()
	waitSessionConfigStep(t, response.entered)
	secondDone := make(chan struct{})
	var second *httptest.ResponseRecorder
	go func() {
		second = request(t, h, http.MethodPost, "/api/sessions/"+id+"/workspace", "{\"workspace\":\""+workspaceID+"\"}", true)
		close(secondDone)
	}()
	waitSessionConfigStep(t, secondDone)
	if second.Code != 200 {
		t.Fatalf("second status=%d", second.Code)
	}
	current, err := sessions.Read(id)
	if err != nil {
		t.Fatal(err)
	}
	if current.Config == nil || current.Config.Model != "two" || current.Config.Workspace != workspaceID {
		t.Fatalf("config=%+v", current.Config)
	}
	unblock()
	waitSessionConfigStep(t, firstDone)
	if response.Code != 200 {
		t.Fatalf("first status=%d", response.Code)
	}
}

func TestInvalidWorkspaceChoiceReleasesTransaction(t *testing.T) {
	sessions := newTestStore(t)
	id, err := sessions.Create("invalid workspace")
	if err != nil {
		t.Fatal(err)
	}
	h, workspaceID := sessionConfigHandler(t, sessions)
	first := request(t, h, http.MethodPost, "/api/sessions/"+id+"/workspace", "{\"workspace\":\"absent\"}", true)
	if first.Code != 404 {
		t.Fatalf("invalid choice status=%d", first.Code)
	}
	before, err := sessions.Read(id)
	if err != nil {
		t.Fatal(err)
	}
	if before.Config != nil {
		t.Fatalf("invalid choice changed configuration: %+v", before.Config)
	}
	done := make(chan struct{})
	var second *httptest.ResponseRecorder
	go func() {
		second = request(t, h, http.MethodPost, "/api/sessions/"+id+"/workspace", "{\"workspace\":\""+workspaceID+"\"}", true)
		close(done)
	}()
	waitSessionConfigStep(t, done)
	if second.Code != 200 {
		t.Fatalf("retry status=%d", second.Code)
	}
}
