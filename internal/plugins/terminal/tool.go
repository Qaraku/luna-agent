package terminal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Qaraku/luna-agent/internal/fileread"
	"github.com/Qaraku/luna-agent/internal/plugin"
	jsonschema "github.com/eino-contrib/jsonschema"
)

const (
	// DefaultTimeout 是 timeout_s 缺省时一条命令可以运行的时间。
	DefaultTimeout = 30 * time.Second
	// MaxTimeout 是 timeout_s 的上限。超过上限的取值一律拒绝，而不是静默改小：
	// 模型提了 10 分钟却只跑 2 分钟，是工具在替它说谎。
	MaxTimeout = 120 * time.Second
	// MaxOutputBytes 是 stdout 与 stderr 合计保留的字节上限。两路分开捕获，但共享
	// 这一个预算，所以“输出太多”是一件事而不是两件；越过上限的字节仍然被读走并计数，
	// 结果里会说清保留了多少、还有多少没有给出。
	MaxOutputBytes = 32 << 10

	// killGrace 是杀掉进程组之后等待 Wait 返回的上限。整组被杀时 Wait 通常立刻返回；
	// 留这个上限是为了防一条逃出进程组的命令一直占着输出管道，让一次调用永远等下去。
	killGrace = 2 * time.Second
)

// runDescription 是模型可见的工具描述：说清它跑什么、在哪里跑、边界是什么、什么时候用。
// 与 internal/plugins/memory/tool.go 的描述同一种语言与口气。
const runDescription = "Run one shell command on this machine and return what happened: its exit code, how long it took, the directory it ran in, and its standard output and standard error, labelled `stdout:` and `stderr:`. The command is passed to `sh -lc`, so a pipeline, a redirection or several commands separated by `;` are all part of the one command you give. Use it when the task needs the machine to actually do something — build, run tests, inspect a repository with git or another command-line tool, or find out something only a command can answer — rather than when one of the file tools already answers it. `cwd` is where the command runs: a path relative to one of the directories this session works in, resolved against exactly the same boundary the file tools obey, so a command can neither be started outside it nor reach anything the file tools could not; with no `cwd` the command runs in the first of those directories. `timeout_s` bounds the run: 30 seconds by default and 120 at most, and when the time is up the command and its whole process group are killed — the answer then says it was stopped instead of presenting a killed process as a finished one. Output is capped in total across both streams, and when the cap is reached the answer says which stream was cut, what is shown and how much was withheld. The command gets a minimal environment (only PATH, HOME and TMPDIR) and no standard input, so it cannot read anything interactively and the rest of this process's environment does not leak into it."

// RunTool 是 luna_run：模型在本次运行的工作目录里执行一条命令的唯一入口。
//
// 它不持有跨调用共享的状态，工作目录与超时都来自每次调用；它也不解释路径，cwd 交给
// fileread 的边界检查。
type RunTool struct{}

// NewRunTool 建这个工具。
func NewRunTool() *RunTool { return &RunTool{} }

func (t *RunTool) Name() string { return RunToolName }

func (t *RunTool) Description() string { return runDescription }

func (t *RunTool) Schema() *jsonschema.Schema { return runSchema() }

// runSchema 是工具的公开 schema：一条必填的命令，加两个可选参数。additionalProperties
// 关闭，所以未声明的参数在触及任何进程之前就被拒绝。
func runSchema() *jsonschema.Schema {
	type args struct {
		Command  string `json:"command" jsonschema_description:"The one command to run, passed to sh -lc; a pipeline or a redirection is part of it"`
		Cwd      string `json:"cwd,omitempty" jsonschema_description:"Where to run it, as a path relative to one of the directories this session works in. Optional: the first of those directories is the default"`
		TimeoutS int    `json:"timeout_s,omitempty" jsonschema_description:"How many seconds the command may run, 30 by default and 120 at most"`
	}
	r := jsonschema.Reflector{DoNotReference: true, AllowAdditionalProperties: false}
	s := r.Reflect(args{})
	s.Required = []string{"command"}
	return s
}

// Invoke 执行命令。除了插件侧无法服务这次调用（例如 sh 起不来）的情况，其余失败都是
// 这次调用的拒绝：模型给错了参数，可以自己纠正，因此不带 plugin.ErrUnavailable 标记。
func (t *RunTool) Invoke(ctx context.Context, arguments string) (string, error) {
	var in struct {
		Command  string `json:"command"`
		Cwd      string `json:"cwd"`
		TimeoutS int    `json:"timeout_s"`
	}
	if err := decodeOne(arguments, &in); err != nil {
		return "", fmt.Errorf("the call is not a single JSON object with the declared parameters: %w", err)
	}
	if strings.TrimSpace(in.Command) == "" {
		return "", errors.New("command is required")
	}
	roots := plugin.Roots(ctx)
	if len(roots) == 0 {
		return "", errors.New("this run works in no directory, so there is nowhere to run a command")
	}
	dir, err := resolveDir(roots, in.Cwd)
	if err != nil {
		return "", err
	}
	timeout, err := resolveTimeout(in.TimeoutS)
	if err != nil {
		return "", err
	}
	return t.run(ctx, dir, in.Command, timeout)
}

// resolveDir 定下命令的工作目录。给了 cwd 就交给 fileread 的多 root 边界检查——与文件
// 工具同一套实现，不在这里另写一遍路径规范化与符号链接解析。cwd 因此与文件工具的 path
// 同形：相对本次运行的工作目录、按顺序在第一个包含它的目录里解析；绝对路径会被同一处
// 拒绝（fileread.ErrPathAbsolute），这样“命令的落脚点”和“文件工具能读到的地方”不会
// 是两套范围。
func resolveDir(roots []string, cwd string) (string, error) {
	if strings.TrimSpace(cwd) == "" {
		return defaultDir(roots)
	}
	resolved, err := fileread.ResolveDirInRoots(roots, cwd)
	if err != nil {
		return "", fmt.Errorf("cwd %q: %w", cwd, err)
	}
	return resolved.Path, nil
}

// defaultDir 是 cwd 缺省时的落点：按顺序第一个存在且确实是目录的工作目录。列表里的
// 目录可能已被删除，跳过不存在的那些，比把命令交给一个不存在的目录更诚实；全都不可用
// 时拒绝，并说清是因为没有可用的工作目录。
func defaultDir(roots []string) (string, error) {
	for _, root := range roots {
		if info, err := os.Stat(root); err == nil && info.IsDir() {
			return root, nil
		}
	}
	return "", errors.New("none of this run's working directories exists")
}

// resolveTimeout 把 timeout_s 折算成时长：0 是“没提”，用默认值；负数或超过上限一律
// 拒绝并说明范围，不静默截断。
func resolveTimeout(requested int) (time.Duration, error) {
	switch {
	case requested == 0:
		return DefaultTimeout, nil
	case requested < 0 || time.Duration(requested)*time.Second > MaxTimeout:
		return 0, fmt.Errorf("timeout_s must be between 1 and %d seconds, or 0 for the default of %d (got %d)",
			int(MaxTimeout/time.Second), int(DefaultTimeout/time.Second), requested)
	default:
		return time.Duration(requested) * time.Second, nil
	}
}

// run 执行一条命令并渲染模型可见的结果。
//
// 结束的三种方式分工明确：命令自己跑完，报退出码；超过 timeout 被停掉，报“被停止了”
// （仍然是结果，不是错误）；ctx 被取消，报 ctx 的错误，由内核当作结束整轮而不是拒绝。
func (t *RunTool) run(ctx context.Context, dir, command string, timeout time.Duration) (string, error) {
	cmd := exec.Command("sh", "-lc", command)
	cmd.Dir = dir
	// 不接 stdin：命令读不到任何交互输入，也不会把运行挂在一句永远等不到的输入上。
	cmd.Stdin = nil
	cmd.Env = commandEnv()
	// 自成一个进程组，超时或被取消时才能杀掉整组：一条管道、一个后台任务、一层子
	// shell 都在这一组里，只杀直接子进程会把它们留下继续跑。
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	capture := newOutputCapture(MaxOutputBytes)
	cmd.Stdout = capture.writer(stdoutStream)
	cmd.Stderr = capture.writer(stderrStream)

	started := time.Now()
	if err := cmd.Start(); err != nil {
		// 连 sh 都起不来是宿主侧的问题，不是模型这次调用的内容问题：标记成基础设施
		// 故障，让内核结束整轮，而不是回给模型一句“工具拒绝了这次调用”。
		return "", plugin.Unavailable(fmt.Errorf("start the command: %w", err))
	}
	pgid := cmd.Process.Pid
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	finished, timedOut := false, false
	select {
	case <-waited:
		finished = true
	case <-timer.C:
		timedOut = true
	case <-ctx.Done():
	}

	// 取消优先于超时：两者同时到达时，运行是被停掉的，报超时会把一次取消说成一次
	// 超时。ctx 的错误交回内核。
	if stopped := ctx.Err(); stopped != nil {
		if !finished {
			killGroup(pgid)
			waitOrGiveUp(waited)
		}
		return "", stopped
	}
	if timedOut {
		killGroup(pgid)
		waitOrGiveUp(waited)
	}
	return t.render(cmd, dir, time.Since(started), timeout, timedOut, capture), nil
}

// render 组装模型可见的结果：退出码、耗时、实际工作目录、两路带标签的输出，以及任何
// 截断与停止的说明。它只报告已经发生的事，不把被杀掉的过程描述成正常结束。
func (t *RunTool) render(cmd *exec.Cmd, dir string, elapsed, timeout time.Duration, timedOut bool, capture *outputCapture) string {
	var b strings.Builder
	fmt.Fprintf(&b, "exit code %d after %s in %s\n", exitCode(cmd), shortDuration(elapsed), dir)
	if timedOut {
		fmt.Fprintf(&b, "(the command was stopped: it was still running after the %s timeout, so it and its whole process group were killed)\n",
			shortDuration(timeout))
	}
	capture.render(&b)
	return b.String()
}

// exitCode 报告退出码；进程被信号杀掉时 ProcessState.ExitCode() 已经是 -1，而
// ProcessState 为空（Wait 在宽限期内没有返回）时同样给 -1，并由此处的说明交代清楚。
func exitCode(cmd *exec.Cmd) int {
	if cmd.ProcessState == nil {
		return -1
	}
	return cmd.ProcessState.ExitCode()
}

// shortDuration 用毫秒精度渲染时长，避免一条跑了几毫秒的命令报成 "1.0001234s"。
func shortDuration(d time.Duration) string { return d.Round(time.Millisecond).String() }

// killGroup 杀掉整组：子进程是组长，所以负的 pid 覆盖它拉起的所有进程。
func killGroup(pgid int) {
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
	_ = syscall.Kill(pgid, syscall.SIGKILL)
}

// waitOrGiveUp 等 Wait 收尾，但不超过 killGrace。
func waitOrGiveUp(waited <-chan error) {
	select {
	case <-waited:
	case <-time.After(killGrace):
	}
}

// commandEnv 是交给命令的环境。只放运行一条命令真正需要的三项，父进程的其余变量一律
// 不带过去：放开命令执行不等于把整个宿主环境交给模型构造的命令。
//
// 这是有意的收窄，代价是一条依赖其它变量的命令（例如 LANG 决定输出编码、GOPATH 决定
// 构建缓存位置）行为可能与用户在终端里看到的不同。它与 timeout_s 同一个取舍：范围写
// 死在这里，不靠父进程恰好带了什么。
func commandEnv() []string {
	env := []string{"PATH=" + os.Getenv("PATH")}
	if home := os.Getenv("HOME"); home != "" {
		env = append(env, "HOME="+home)
	}
	if tmp := os.Getenv("TMPDIR"); tmp != "" {
		env = append(env, "TMPDIR="+tmp)
	}
	return env
}

// stdoutStream 与 stderrStream 是 outputCapture 里两路输出的下标。
const (
	stdoutStream = 0
	stderrStream = 1
)

// outputCapture 在一个共享字节预算下收集两路输出：预算之内照单全收，越过的字节只计数。
// 分开保存是为了结果里能带上标签，共享预算是因为模型要读的是“这次调用的输出”，总量
// 该有上限而不是每路各有一份。
type outputCapture struct {
	mu      sync.Mutex
	limit   int
	kept    [2][]byte
	dropped [2]int
}

func newOutputCapture(limit int) *outputCapture { return &outputCapture{limit: limit} }

// writer 返回某一路的写入端。
func (c *outputCapture) writer(stream int) io.Writer { return captureWriter{c: c, stream: stream} }

type captureWriter struct {
	c      *outputCapture
	stream int
}

// Write 留下预算装得下的部分并统计其余。它总是报告整段都被写入：命令自己的输出量
// 不该由这里的上限改变，一个短写会让子进程因为本属于工具的上限而收到 EPIPE 而失败。
// 没有留下的字节会写在结果里，而不是悄悄丢掉。
func (w captureWriter) Write(p []byte) (int, error) {
	c := w.c
	c.mu.Lock()
	defer c.mu.Unlock()
	room := c.limit - len(c.kept[stdoutStream]) - len(c.kept[stderrStream])
	if room > len(p) {
		room = len(p)
	}
	if room > 0 {
		c.kept[w.stream] = append(c.kept[w.stream], p[:room]...)
	}
	c.dropped[w.stream] += len(p) - room
	return len(p), nil
}

// render 写下两路输出，各自带标签；空的一路也照实说“没有输出”，因为“这一路没有说
// 什么”与“这一路不存在”对模型是两件不同的事。触到上限的一路要说清被截在哪里、还有
// 多少没有给出。
func (c *outputCapture) render(b *strings.Builder) {
	for stream, name := range [2]string{"stdout", "stderr"} {
		fmt.Fprintf(b, "%s:\n", name)
		text := c.kept[stream]
		if len(text) == 0 {
			b.WriteString("(no output)\n")
		} else {
			b.Write(text)
			if text[len(text)-1] != '\n' {
				b.WriteString("\n")
			}
		}
		if dropped := c.dropped[stream]; dropped > 0 {
			fmt.Fprintf(b, "(%s truncated: only its first %d bytes are shown; %d more bytes were not given, the %d-byte total output cap was reached)\n",
				name, len(text), dropped, c.limit)
		}
	}
}

// decodeOne 只接受一个 JSON 对象：拒绝未知字段与尾随的 JSON 值。它是工具自己的一份
// 拷贝，与内核包装器和 memory 工具同一条规则：格式不对的调用必须在触及任何进程之前
// 被拒绝。
func decodeOne(arguments string, into any) error {
	d := json.NewDecoder(strings.NewReader(arguments))
	d.DisallowUnknownFields()
	if err := d.Decode(into); err != nil {
		return err
	}
	var extra any
	if extraErr := d.Decode(&extra); extraErr != io.EOF {
		if extraErr == nil {
			return errors.New("expected exactly one JSON object")
		}
		return extraErr
	}
	return nil
}
