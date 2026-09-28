package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeWriteDirs 是"允许写入的目录存在哪里"在这个层看到的形状：它持有当前列表、记下每
// 次被要求写什么、是否应该失败。它不知道这个列表存在哪个文件里。
//
// 它也像真存储那样归一化（去掉末尾的 /）并去重：PUT 的答案必须是"存下来之后重新读到的"
// 那份，假存储不做这件事的话，这条要求在这个 seam 上根本看不出区别。
type fakeWriteDirs struct {
	dirs []string
	err  error

	written [][]string
}

func (p *fakeWriteDirs) Dirs() ([]string, error) {
	if p.err != nil {
		return nil, p.err
	}
	if p.dirs == nil {
		return []string{}, nil
	}
	return append([]string{}, p.dirs...), nil
}

func (p *fakeWriteDirs) SetDirs(dirs []string) error {
	if p.err != nil {
		return p.err
	}
	p.written = append(p.written, append([]string{}, dirs...))
	next := make([]string, 0, len(dirs))
	seen := map[string]bool{}
	for _, dir := range dirs {
		normalised := strings.TrimSuffix(dir, "/")
		if normalised == "" {
			normalised = "/"
		}
		if seen[normalised] {
			continue
		}
		seen[normalised] = true
		next = append(next, normalised)
	}
	p.dirs = next
	return nil
}

// handlerWithWriteDirs 与 handlerWithPreference 同构：接上记录"允许写入的目录"的
// seam。不传偏好时就是不接，与这个 seam 出现之前的服务器一样。
func handlerWithWriteDirs(t *testing.T, pref WriteDirPreference) http.Handler {
	t.Helper()
	opts := []Option{}
	if pref != nil {
		opts = append(opts, WithWriteDirs(pref))
	}
	p := &fakePlugins{state: pluginState("text_transform")}
	return New(p, fakeRunner{}, newTestStore(t), Info{BoundHost: "127.0.0.1:43210", Model: "fake-model", ProviderHost: "provider.test", WebDir: "../../web"}, opts...)
}

func putWriteDirs(t *testing.T, h http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	return request(t, h, http.MethodPut, writeDirsPath, body, true)
}

// decodeWriteDirs 只读契约里那一份字段：答案里多出别的东西不该让测试继续跑。
func decodeWriteDirs(t *testing.T, data []byte) []string {
	t.Helper()
	var view struct {
		Dirs []string `json:"dirs"`
	}
	if err := json.Unmarshal(data, &view); err != nil {
		t.Fatalf("decode %s: %v", data, err)
	}
	return view.Dirs
}

// GET 返回偏好持有的那份列表。
func TestWriteDirsAreListed(t *testing.T) {
	pref := &fakeWriteDirs{dirs: []string{"/home/j/notes", "/tmp/scratch"}}
	w := request(t, handlerWithWriteDirs(t, pref), http.MethodGet, writeDirsPath, "", false)
	if w.Code != 200 {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body)
	}
	if got := strings.Join(decodeWriteDirs(t, w.Body.Bytes()), ","); got != "/home/j/notes,/tmp/scratch" {
		t.Fatalf("dirs = %q, want the stored list", got)
	}
}

// 没接上偏好时 GET 明确报错，说清是什么没接上：空表是一个真实的选择（"哪里都不许写"），
// 与"根本没接上"分不开的话，页面会展示一个用户从没做过的决定。
func TestWriteDirsWithoutAPreferenceAreRefused(t *testing.T) {
	w := request(t, handlerWithWriteDirs(t, nil), http.MethodGet, writeDirsPath, "", false)
	if w.Code != 500 {
		t.Fatalf("status = %d, want 500 without a preference (body = %s)", w.Code, w.Body)
	}
	body := w.Body.String()
	if !strings.Contains(body, `"error"`) || !strings.Contains(body, "no write directory preference is configured") {
		t.Fatalf("body = %s, want a sentence naming what is missing", body)
	}
}

// PUT 把整份列表写下来，并以"存下来之后重新读到的"那份作答，而不是回显请求：存储会归一
// 化、去重，回显输入会让页面显示一份下一次运行并不遵守的列表。
func TestSavingWriteDirsAnswersWhatWasStored(t *testing.T) {
	pref := &fakeWriteDirs{dirs: []string{"/old"}}
	h := handlerWithWriteDirs(t, pref)

	w := putWriteDirs(t, h, `{"dirs":["/home/j/notes/","/home/j/notes","/data"]}`)
	if w.Code != 200 {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body)
	}
	if len(pref.written) != 1 {
		t.Fatalf("wrote %d times, want the whole list once", len(pref.written))
	}
	if got := strings.Join(pref.written[0], ","); got != "/home/j/notes/,/home/j/notes,/data" {
		t.Fatalf("written = %q, want exactly the submitted entries", got)
	}
	answer := decodeWriteDirs(t, w.Body.Bytes())
	if got := strings.Join(answer, ","); got != "/home/j/notes,/data" {
		t.Fatalf("answer = %q, want the list as stored, not as submitted", got)
	}
	// 请求里的 /old 没有被保留：页面拥有整份列表，一个它不再点名的目录就是被撤掉了。
	if strings.Contains(strings.Join(answer, ","), "/old") {
		t.Fatalf("answer = %q, want the list replaced rather than patched", answer)
	}
	// 重新读一次拿到的是同一份：答案与存储没有两套说法。
	readBack := decodeWriteDirs(t, request(t, h, http.MethodGet, writeDirsPath, "", false).Body.Bytes())
	if strings.Join(readBack, ",") != strings.Join(answer, ",") {
		t.Fatalf("GET = %q, PUT answered %q", readBack, answer)
	}
}

// 非绝对路径的条目被拒，400 且点名那一条，偏好一个字节没被写：悄悄丢掉它会让用户以为
// 自己加进去的目录已经允许写入。
func TestANonAbsoluteWriteDirIsRefusedByName(t *testing.T) {
	pref := &fakeWriteDirs{dirs: []string{"/kept"}}
	w := putWriteDirs(t, handlerWithWriteDirs(t, pref), `{"dirs":["/home/j/notes","relative/path","./also/relative"]}`)
	if w.Code != 400 {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body)
	}
	if !strings.Contains(w.Body.String(), "relative/path") {
		t.Fatalf("body = %s, want the offending entry named", w.Body)
	}
	if len(pref.written) != 0 {
		t.Fatalf("a refused save wrote %q", pref.written)
	}
	if got := strings.Join(pref.dirs, ","); got != "/kept" {
		t.Fatalf("stored dirs = %q, want them untouched", got)
	}
}

// 空条目也被点名，而不是当成"没有这一条"。
func TestAnEmptyWriteDirIsRefused(t *testing.T) {
	pref := &fakeWriteDirs{}
	w := putWriteDirs(t, handlerWithWriteDirs(t, pref), `{"dirs":["/home/j/notes","  "]}`)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "empty") {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body)
	}
	if len(pref.written) != 0 {
		t.Fatalf("a refused save wrote %q", pref.written)
	}
}

// 写失败就是这次请求失败：500 带上存储给的原因，列表一点没改——页面不能告诉用户目录已经
// 允许写入了。
func TestAFailedWriteDirSaveIsReported(t *testing.T) {
	pref := &fakeWriteDirs{dirs: []string{"/kept"}, err: errors.New("write settings.yaml: permission denied")}
	w := putWriteDirs(t, handlerWithWriteDirs(t, pref), `{"dirs":["/home/j/notes"]}`)
	if w.Code != 500 {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body)
	}
	if !strings.Contains(w.Body.String(), "permission denied") {
		t.Fatalf("body = %s, want the reason", w.Body)
	}
	if got := strings.Join(pref.dirs, ","); got != "/kept" {
		t.Fatalf("stored dirs = %q, want them unchanged", got)
	}
}

// 严格解码：不认识的字段拒，不是单个 JSON 对象也拒。一个被忽略的字段意味着页面与这一层
// 对同一份请求的理解已经分开了，而请求回的是 200。
func TestWriteDirsBodyIsDecodedStrictly(t *testing.T) {
	pref := &fakeWriteDirs{}
	h := handlerWithWriteDirs(t, pref)
	for _, body := range []string{
		`{"dirs":["/home/j/notes"],"extra":true}`,
		`{"dirs":["/home/j/notes"]}{"dirs":[]}`,
		`["/home/j/notes"]`,
	} {
		w := putWriteDirs(t, h, body)
		if w.Code != 400 {
			t.Fatalf("body %s: status = %d, want 400", body, w.Code)
		}
	}
	if len(pref.written) != 0 {
		t.Fatalf("a rejected body wrote %q", pref.written)
	}
}

// 允许写入哪些目录就是写入工具遵守的东西，所以它是与保存 provider 同类的变更：外来页面
// 不能替用户做这个决定。
func TestSavingWriteDirsNeedsTheBoundOrigin(t *testing.T) {
	pref := &fakeWriteDirs{dirs: []string{"/kept"}}
	w := request(t, handlerWithWriteDirs(t, pref), http.MethodPut, writeDirsPath, `{"dirs":["/home/j/notes"]}`, false)
	if w.Code != 403 {
		t.Fatalf("status = %d, want 403 without the bound Origin (body = %s)", w.Code, w.Body)
	}
	if len(pref.written) != 0 {
		t.Fatalf("a request without the bound Origin wrote %q", pref.written)
	}
}

// 读不走 Origin 守卫，写只认 PUT。
func TestWriteDirsMethodsAreEnforced(t *testing.T) {
	h := handlerWithWriteDirs(t, &fakeWriteDirs{})
	if w := request(t, h, http.MethodPost, writeDirsPath, `{}`, true); w.Code != 405 {
		t.Fatalf("POST status = %d, want 405", w.Code)
	}
	if w := request(t, h, http.MethodGet, writeDirsPath, "", false); w.Code != 200 {
		t.Fatalf("GET status = %d, want 200 without an Origin (body = %s)", w.Code, w.Body)
	}
}
