package filewrite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/Qaraku/luna-agent/internal/atomicfile"
	"github.com/Qaraku/luna-agent/internal/fileread"
	"github.com/Qaraku/luna-agent/internal/plugin"
	"github.com/Qaraku/luna-agent/internal/settings"
	jsonschema "github.com/eino-contrib/jsonschema"
)

const (
	// WriteToolName is the model-visible name of the file write tool.
	WriteToolName = "luna_write_file"

	// MaxContentBytes is the largest file this tool writes or replaces, and it is
	// the read limit on purpose: a file this tool wrote has to be one the model
	// can read back with luna_read_file. A limit of its own would let the tool
	// create a file it could not read again, and a write the tool cannot show the
	// model is not one it should be making. The same number is used for the file
	// being replaced: this tool does not overwrite a file it could not have read.
	MaxContentBytes = fileread.DefaultLimit

	// MaxDiffBytes is the largest diff one result carries. A diff is the whole
	// reason this tool reports what it did, so it is bounded like every other
	// model-visible result in this repository — and a diff that hit the limit
	// says which limit it hit and how much of it is missing rather than ending
	// in the middle of the change as if that were all of it.
	MaxDiffBytes = 8 * 1024

	// newFileMode is the permission a file this tool creates gets. A file that is
	// already there keeps the permission bits it has: replacing a file's contents
	// is not the place to also change who may read it.
	newFileMode fs.FileMode = 0o644

	// allowHint tells the model what to tell the user when a directory has not
	// been allowed. It names the product's own control — the settings page and
	// the 工作区 category — because "not permitted" without that is a dead end
	// for the person who has to fix it.
	allowHint = "the settings page, under its 工作区 category"

	// writeDescription is the tool's model-visible description. It has to keep
	// the four things the model can act on: content is the whole text rather than
	// a patch, create_only exists, the result carries a diff, and a refusal
	// writes nothing while naming its own reason.
	writeDescription = "Create or replace one project text file under the effective read/write policy. ask pauses for one-time approval; deny refuses before reading or writing. Outside the automatic write-directory scope, a separate one-time scope approval is required, and the path must still remain inside this run's project directories. `path` is relative to one of this run's working directories; `content` is the file's whole new text — it replaces what the file holds rather than patching it — and an empty `content` makes an empty file. Set `create_only` when the file must not already exist. The result says whether the file was created or overwritten and shows a diff of the change, so you can tell the user what changed; an overwrite that would change nothing writes nothing. A call is refused, leaving everything on disk as it was, when the path leaves this run's working directories, when a required approval is refused, when the content or the file being replaced is not text or is over the size limit, or when the file cannot be written — each refusal states its own reason, and the reason is what you pass on to the user."
)

// WriteTool is the capability's luna_write_file: the model's only way to change
// a file on this machine, and a narrow one — one file at a time, text only, and
// only where the user has said yes.
//
// It emits nothing. tool.started, tool.failed and tool.finished are the Kernel's
// to emit for this round; the tool's job is the arguments, the boundary and the
// write.
type WriteTool struct {
	// settingsPath is the file the user's choices are read from. It is read on
	// every call rather than once at start-up: the settings page writes that file,
	// so a directory the user allows while a conversation is going is allowed for
	// the model's next call in the same round — no restart, no rebuild.
	settingsPath string
}

// NewWriteTool wires the tool to the settings file it reads the user's write
// grants from.
func NewWriteTool(settingsPath string) *WriteTool { return &WriteTool{settingsPath: settingsPath} }

func (t *WriteTool) Name() string { return WriteToolName }

func (t *WriteTool) Description() string { return writeDescription }

func (t *WriteTool) Schema() *jsonschema.Schema { return writeSchema() }

// writeSchema is the exact public schema of the write tool. `content` is a
// required parameter whose value may be the empty string — an empty file is a
// legal request — so the schema keeps the two questions apart: whether the model
// gave the field at all, and what it gave. additionalProperties is closed, so a
// parameter that is not one of the three is refused before anything is read.
func writeSchema() *jsonschema.Schema {
	type args struct {
		Path    string `json:"path" jsonschema_description:"The file to write, relative to one of this run's working directories"`
		Content string `json:"content" jsonschema_description:"The file's entire new text; it replaces the file's contents, and an empty string makes an empty file"`
		// No omitempty and no pointer in the reflected type: the schema says
		// boolean with a default, and Invoke is where "absent" is decided.
		CreateOnly bool `json:"create_only" jsonschema_description:"Refuse this call instead of replacing a file that is already there (default false)"`
	}
	r := jsonschema.Reflector{DoNotReference: true, AllowAdditionalProperties: false}
	s := r.Reflect(args{})
	s.Required = []string{"path", "content"}
	return s
}

// decodeOne accepts exactly one JSON object and rejects unknown fields and
// trailing JSON values. It is this tool's own copy of the rule the kernel's
// wrappers share: a malformed call must be refused before it reaches the disk.
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

// Invoke writes one file, and reports what it did in enough detail for the model
// to tell the user: whether the file was created or replaced, its size, its
// permission bits, and a diff of the change.
//
// The order of the checks is the order of the questions, and every one of them
// ends in a refusal that leaves the disk untouched:
//
//  1. the call has a path and has content at all;
//  2. this run has somewhere to work;
//  3. the path, interpreted once by internal/fileread against this run's
//     working directories, is a place a write may go — its reason is passed
//     through rather than restated here, so the write boundary and the read
//     boundary cannot drift apart;
//  4. the user has allowed some directory at all, read from the settings file
//     now rather than at start-up;
//  5. the resolved target is inside one of those directories, symbolic links
//     resolved — the grant narrows this run's working directories, and can
//     never widen them;
//  6. the content is text, and is no larger than a file this tool could read
//     back;
//  7. the file being replaced, if any, is text no larger than that limit, since
//     the result has to describe that change;
//  8. the call did not ask to create only a file that is already there;
//  9. the content is not already exactly what the file holds.
//
// Checks 4 and 5 are two questions on purpose, because their refusals ask the
// user for two different things: the first is "nothing has been allowed yet",
// the second is "this directory is not among what you allowed".
func (t *WriteTool) Invoke(ctx context.Context, arguments string) (string, error) {
	var in struct {
		Path    string  `json:"path"`
		Content *string `json:"content"`
		// A pointer, so "the model did not send content" is not silently read as
		// "make this file empty": the schema requires the parameter, and this is
		// where that requirement is enforced rather than trusted.
		CreateOnly bool `json:"create_only"`
	}
	if err := decodeOne(arguments, &in); err != nil {
		return "", err
	}
	if strings.TrimSpace(in.Path) == "" {
		return "", errors.New("a path is required: name the file to write, relative to one of this run's working directories")
	}
	if in.Content == nil {
		return "", errors.New("content is required: send the file's whole new text (an empty string is allowed and makes an empty file)")
	}
	content := *in.Content
	if err := plugin.CheckAccess(ctx, plugin.AccessRead, plugin.AccessWrite); err != nil {
		return "", err
	}

	roots := plugin.Roots(ctx)
	if len(roots) == 0 {
		// A run with no working directory is a session that names no workspace. The fix
		// belongs to the user, so the refusal names the control rather than only stating
		// that there is nowhere to write.
		return "", errors.New("this run has no working directory, so there is nowhere to write a file; nothing was written — this session is not bound to a workspace, and the user binds one on the settings page under 工作区")
	}
	target, err := fileread.ResolveWriteInRoots(roots, in.Path)
	if err != nil {
		// The boundary's own reason, unchanged: it is the same sentence a read
		// of this path would have been refused with.
		return "", err
	}
	info, _ := plugin.Run(ctx)
	if info.WriteScopeError != "" {
		return "", errors.New(info.WriteScopeError)
	}
	var scopeErr error
	if info.AutomaticWriteDirs != nil {
		scopeErr = allowedInDirs(target.Path, in.Path, info.AutomaticWriteDirs)
	} else {
		scopeErr = t.allowed(target.Path, in.Path)
	}
	if scopeErr != nil && !errors.Is(scopeErr, errOutsideWriteScope) {
		return "", scopeErr
	}
	if strings.ContainsRune(content, 0) {
		return "", errors.New("content contains a NUL byte, so it is not text; this tool writes text files, and nothing was written")
	}
	if len(content) > MaxContentBytes {
		return "", fmt.Errorf("content is %d bytes, over the %d-byte limit this tool writes; nothing was written", len(content), MaxContentBytes)
	}

	preview := content
	if len(preview) > 4096 {
		preview = preview[:4096] + "\n[preview truncated; full content is bound by digest]"
	}
	if err := plugin.RequireAccess(ctx, plugin.AccessRequest{Tool: WriteToolName, Summary: fmt.Sprintf("write %d bytes to one project file", len(content)), Target: target.Path, Preview: preview, ReadRoots: roots, WriteRoots: []string{target.Path}, Permissions: []plugin.AccessKind{plugin.AccessRead, plugin.AccessWrite}, ScopeApproval: scopeErr != nil, ParametersDigest: plugin.AccessDigest(arguments)}); err != nil {
		if errors.Is(err, plugin.ErrApprovalRequired) && scopeErr != nil {
			return "", fmt.Errorf("%w: %v", err, scopeErr)
		}
		return "", err
	}
	checked, err := fileread.ResolveWriteInRoots(roots, in.Path)
	if err != nil {
		return "", err
	}
	if checked.Path != target.Path {
		return "", fmt.Errorf("write target changed while awaiting approval; request again")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	current, exists, mode, err := existingText(target.Path, in.Path)
	if err != nil {
		return "", err
	}
	if in.CreateOnly && exists {
		return "", fmt.Errorf("%q is already there and this call asked to create only; nothing was written — call again without create_only to replace it", in.Path)
	}
	if exists && current == content {
		// Nothing to do and nothing done. A write here would have replaced the
		// file with the same bytes and moved its modification time, which is a
		// change the user did not ask for and the model would have to explain.
		return fmt.Sprintf("%s already has exactly this content; nothing was written", in.Path), nil
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := atomicfile.WriteFile(target.Path, []byte(content), mode); err != nil {
		return "", fmt.Errorf("write %q: %w", in.Path, err)
	}

	body, added, removed := diffText(current, content)
	var b strings.Builder
	if exists {
		fmt.Fprintf(&b, "overwrote %s (%d bytes, mode %#o): %d lines added, %d removed\n", in.Path, len(content), mode.Perm(), added, removed)
	} else {
		fmt.Fprintf(&b, "created %s (%d bytes, mode %#o)\n", in.Path, len(content), mode.Perm())
	}
	b.WriteString("diff:\n")
	b.WriteString(fitDiff(body))
	return b.String(), nil
}

// allowed checks the resolved target against the directories the user allowed
// Luna to write in, reading the settings file now so that a grant made while
// this conversation is going takes effect on the next call.
//
// Each allowed directory is resolved with filepath.EvalSymlinks first, so the
// comparison happens between real paths: a grant is written down as the user's
// spelling of a directory, while the target is always a resolved path. A
// directory that cannot be resolved is skipped — it is not there, and a
// directory that is not there cannot contain anything.
//
// The comparison itself is fileread.Within, the same rule the read and write
// boundaries are decided by, so a grant and a resolution can never disagree
// about where a directory ends.
var errOutsideWriteScope = errors.New("path is outside automatic write scope")

func (t *WriteTool) allowed(resolved, requested string) error {
	file, _, err := settings.Load(t.settingsPath)
	if err != nil {
		// A settings file that cannot be read is reported as a refusal, with the
		// settings package's own reason attached. It ends the round for nobody:
		// this is something the model can pass on to the user, who is the only
		// one who can fix the file.
		return fmt.Errorf("read the directories allowed to be written: %w", err)
	}
	return allowedInDirs(resolved, requested, file.WriteDirs())
}

func allowedInDirs(resolved, requested string, dirs []string) error {
	if len(dirs) == 0 {
		return fmt.Errorf("%w: %q is not inside a directory Luna may write in: no directory has been allowed yet, so nothing was written. The user allows one in %s.", errOutsideWriteScope, requested, allowHint)
	}
	for _, dir := range dirs {
		real, err := filepath.EvalSymlinks(dir)
		if err != nil {
			continue
		}
		if fileread.Within(real, resolved) {
			return nil
		}
	}
	return fmt.Errorf("%w: %q is not inside a directory the user allowed Luna to write in; nothing was written. The user allows one in %s.", errOutsideWriteScope, requested, allowHint)
}

// existingText reads the file a call is about to replace and answers the three
// things the write needs: what it holds now, whether it is there at all, and
// which permission bits it must keep.
//
// A file that is not there is a creation, not a failure: the mode is the one a
// new file gets, and the old text is empty, so the diff of a creation is every
// line the new file adds.
//
// The criteria for reading the old text are the read tool's, applied by the same
// function at the same limit. A file over the limit is refused rather than
// replaced — "overwrite a file I could not read back" is a promise this result
// could not keep, since the diff it is supposed to show would be about a file it
// never saw whole — and a file with a NUL byte in it is refused because there is
// no text diff to give for it.
func existingText(resolved, requested string) (text string, exists bool, mode fs.FileMode, err error) {
	info, statErr := os.Lstat(resolved)
	switch {
	case statErr == nil && !info.Mode().IsRegular():
		// The resolver already required a regular file, so this is a target that
		// changed between being resolved and being written to — a name this tool
		// will not take, whatever it is now.
		return "", false, 0, fmt.Errorf("%q is not a regular file; nothing was written", requested)
	case statErr != nil && !errors.Is(statErr, fs.ErrNotExist):
		return "", false, 0, fmt.Errorf("examine %q before writing it: %s", requested, reason(statErr))
	case statErr != nil:
		return "", false, newFileMode, nil
	}
	mode = info.Mode().Perm()
	text, readErr := fileread.Read(resolved, MaxContentBytes)
	switch {
	case readErr == nil:
		return text, true, mode, nil
	case errors.Is(readErr, fileread.ErrTooLarge):
		return "", false, 0, fmt.Errorf("%q is larger than the %d-byte limit, and this tool does not replace a file it could not read back in full; nothing was written", requested, MaxContentBytes)
	case errors.Is(readErr, fileread.ErrBinary):
		return "", false, 0, fmt.Errorf("%q is not text (it contains a NUL byte), so there is no diff to give for replacing it; nothing was written", requested)
	default:
		return "", false, 0, fmt.Errorf("read %q before replacing it: %s", requested, reason(readErr))
	}
}

// reason reduces an *os.PathError to its cause, so an error string never carries
// an absolute host path: these strings reach the model, which already knows the
// relative path it asked for and has no business learning the layout of the
// machine it runs on.
func reason(err error) string {
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Err.Error()
	}
	return err.Error()
}
