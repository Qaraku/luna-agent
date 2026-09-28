package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Qaraku/luna-agent/internal/provider"
)

// 夹具只使用合成值；第一条 Write 悬停在提交前，让旧快照合并可以确定地重现。
type heldProviderWrite struct {
	mu                                           sync.Mutex
	file                                         provider.File
	writes                                       int
	writing                                      bool
	entered, release, committed, overlappingRead chan struct{}
	overlapOnce                                  sync.Once
}

func newHeldProviderWrite() *heldProviderWrite {
	return &heldProviderWrite{
		file: provider.File{Active: "alpha", Providers: map[string]provider.Endpoint{
			"alpha": {BaseURL: "https://example.test/v1", APIKey: "synthetic-original", Model: "initial"},
		}},
		entered: make(chan struct{}), release: make(chan struct{}),
		committed: make(chan struct{}), overlappingRead: make(chan struct{}),
	}
}

func (p *heldProviderWrite) Read() (provider.File, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.writing {
		p.overlapOnce.Do(func() { close(p.overlappingRead) })
	}
	return p.file.Trimmed(), nil
}

func (p *heldProviderWrite) Write(file provider.File) error {
	if err := file.Validate(); err != nil {
		return err
	}
	p.mu.Lock()
	p.writes++
	first := p.writes == 1
	if first {
		p.writing = true
	}
	p.mu.Unlock()
	if first {
		close(p.entered)
		<-p.release
	} else {
		// 第二条保存必定在第一条之后落盘，不能靠随机完成顺序掩盖旧快照问题。
		<-p.committed
	}
	p.mu.Lock()
	p.file = file.Trimmed()
	if first {
		p.writing = false
	}
	p.mu.Unlock()
	if first {
		close(p.committed)
	}
	return nil
}

type providerReadyBody struct {
	*strings.Reader
	ready chan struct{}
	once  sync.Once
}

func (b *providerReadyBody) Read(data []byte) (int, error) {
	n, err := b.Reader.Read(data)
	if err == io.EOF {
		b.once.Do(func() { close(b.ready) })
	}
	return n, err
}
func (*providerReadyBody) Close() error { return nil }

func waitProviderStep(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("provider request did not reach its checkpoint")
	}
}

func TestConcurrentProviderSaveKeepsTheLatestCommittedKey(t *testing.T) {
	file := newHeldProviderWrite()
	h := handlerWithProvider(t, file, Info{})
	unblock := sync.OnceFunc(func() { close(file.release) })
	defer unblock()
	firstDone, secondDone := make(chan struct{}), make(chan struct{})
	var first, second *httptest.ResponseRecorder
	go func() {
		first = putProvider(t, h, oneProvider("alpha", "https://example.test/v1", "synthetic-rotated", "initial"))
		close(firstDone)
	}()
	waitProviderStep(t, file.entered)
	body, err := json.Marshal(oneProvider("alpha", "https://example.test/v1", "", "updated-model"))
	if err != nil {
		t.Fatal(err)
	}
	ready := make(chan struct{})
	r := httptest.NewRequest(http.MethodPut, "http://127.0.0.1:43210/api/provider", &providerReadyBody{Reader: strings.NewReader(string(body)), ready: ready})
	r.Host = "127.0.0.1:43210"
	r.Header.Set("Origin", "http://127.0.0.1:43210")
	go func() {
		second = httptest.NewRecorder()
		h.ServeHTTP(second, r)
		close(secondDone)
	}()
	waitProviderStep(t, ready)
	// 请求体已读完；旧实现立即读旧快照，新实现只等待保存锁。
	select {
	case <-file.overlappingRead:
	case <-time.After(100 * time.Millisecond):
	}
	unblock()
	waitProviderStep(t, firstDone)
	waitProviderStep(t, secondDone)
	if first.Code != 200 || second.Code != 200 {
		t.Fatalf("save status: first=%d second=%d", first.Code, second.Code)
	}
	current, err := file.Read()
	if err != nil {
		t.Fatal(err)
	}
	if current.Providers["alpha"].APIKey != "synthetic-rotated" {
		t.Error("a blank key restored a value from before the preceding save")
	}
	if current.Providers["alpha"].Model != "updated-model" {
		t.Error("the second save lost its explicit model selection")
	}
}

type failingProviderSave struct {
	fakeProviderFile
	failRead, failWrite bool
}

func (p *failingProviderSave) Read() (provider.File, error) {
	if p.failRead {
		p.failRead = false
		return provider.File{}, errors.New("synthetic read failure")
	}
	return p.fakeProviderFile.Read()
}
func (p *failingProviderSave) Write(file provider.File) error {
	if p.failWrite {
		p.failWrite = false
		return errors.New("synthetic write failure")
	}
	return p.fakeProviderFile.Write(file)
}

func TestFailedProviderSaveDoesNotBlockTheNextSave(t *testing.T) {
	for _, step := range []string{"read", "merge", "write"} {
		t.Run(step, func(t *testing.T) {
			file := &failingProviderSave{fakeProviderFile: fakeProviderFile{stored: newHeldProviderWrite().file}, failRead: step == "read", failWrite: step == "write"}
			h := handlerWithProvider(t, file, Info{})
			name := "alpha"
			if step == "merge" {
				name = ""
			}
			first := putProvider(t, h, oneProvider(name, "https://example.test/v1", "", "m"))
			expected := http.StatusBadRequest
			if step == "read" {
				expected = http.StatusInternalServerError
			}
			if first.Code != expected {
				t.Fatalf("first status=%d, want %d", first.Code, expected)
			}
			done := make(chan struct{})
			var second *httptest.ResponseRecorder
			go func() {
				second = putProvider(t, h, oneProvider("alpha", "https://example.test/v1", "", "m"))
				close(done)
			}()
			waitProviderStep(t, done)
			if second.Code != http.StatusOK {
				t.Fatalf("next save status=%d", second.Code)
			}
		})
	}
}

type providerResponseGate struct {
	*httptest.ResponseRecorder
	entered, release chan struct{}
	once             sync.Once
}

func (w *providerResponseGate) Write(data []byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	<-w.release
	return w.ResponseRecorder.Write(data)
}

func TestSlowProviderResponseDoesNotHoldTheSaveTransaction(t *testing.T) {
	file := newHeldProviderWrite()
	close(file.release)
	h := handlerWithProvider(t, file, Info{})
	responseRelease := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(responseRelease) })
	defer unblock()
	first := &providerResponseGate{ResponseRecorder: httptest.NewRecorder(), entered: make(chan struct{}), release: responseRelease}
	data, err := json.Marshal(oneProvider("alpha", "https://example.test/v1", "synthetic-rotated", "initial"))
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPut, "http://127.0.0.1:43210/api/provider", strings.NewReader(string(data)))
	r.Host = "127.0.0.1:43210"
	r.Header.Set("Origin", "http://127.0.0.1:43210")
	firstDone := make(chan struct{})
	go func() { h.ServeHTTP(first, r); close(firstDone) }()
	waitProviderStep(t, first.entered)
	nextDone := make(chan struct{})
	var next *httptest.ResponseRecorder
	go func() {
		next = putProvider(t, h, oneProvider("alpha", "https://example.test/v1", "", "next-model"))
		close(nextDone)
	}()
	select {
	case <-nextDone:
	case <-time.After(time.Second):
		t.Error("HTTP response held the configuration save lock")
	}
	unblock()
	waitProviderStep(t, firstDone)
	waitProviderStep(t, nextDone)
	if first.Code != 200 || next.Code != 200 {
		t.Errorf("save status: first=%d second=%d", first.Code, next.Code)
	}
}
