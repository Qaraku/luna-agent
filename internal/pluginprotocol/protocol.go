package pluginprotocol

import (
	"github.com/hashicorp/go-plugin"
	"net/rpc"
)

var Handshake = plugin.HandshakeConfig{ProtocolVersion: 1, MagicCookieKey: "LUNA_PLUGIN", MagicCookieValue: "luna-core-v1"}

// Input is the plugin RPC request. Text/DelayMS serve the text-transform
// plugin; Path/MaxBytes serve the file-read plugin, where Path is an absolute
// path the host has already validated against the read root, MaxBytes bounds
// the bytes returned and StartLine/MaxLines bound which lines of the file are
// returned. Path/MaxEntries/
// MaxLineBytes serve the directory-listing plugin, where Path is the same kind
// of validated absolute path — a directory — and the two caps bound how much of
// Path/Query/MaxMatches/MaxFiles/MaxFileBytes serve the search plugin, where
// Path is a validated absolute file or directory, Query is the text to look
// for, MaxLineBytes bounds one rendered line and the other three bound how many
// matches are rendered, how many files are read and how large a file may be
// before it is skipped; Mode says how Query is to be read, and an empty Mode is
// the literal default the search has always had. Path/Pattern/MaxPaths/MaxScanned
// serve the name-search
// plugin, where Path is a validated absolute file or directory, Pattern is the
// glob matched against one entry name, MaxLineBytes bounds one rendered line,
// MaxPaths bounds how many matching paths are rendered and MaxScanned how many
// entries are examined. A plugin never receives a model- or browser-supplied
// path in Path.
type Input struct {
	Text     string `json:"text"`
	Path     string `json:"path"`
	MaxBytes int    `json:"max_bytes"`
	// StartLine and MaxLines carry the optional line range of a file read:
	// StartLine is the 1-based number of the first line to return and
	// MaxLines is the largest number of lines to return. Zero means the
	// request did not name one — StartLine 0 is the first line and MaxLines 0
	// is every line from there on — so a request with both at zero is the
	// whole-file read it has always been, and a negative value is a malformed
	// call the plugin refuses instead of guessing at. A plugin sent a range
	// states the lines it returned and the lines it did not read; it never
	// answers a range read with a silently cut result.
	StartLine int `json:"start_line"`
	MaxLines  int `json:"max_lines"`
	// Query is the text a search looks for. It is data, not a pattern, unless
	// Mode explicitly says otherwise: in the default mode the plugin passes it
	// to fileread.Search, which matches it with a substring test, so nothing in
	// it is interpreted.
	Query string `json:"query"`
	// Mode says how a search is to read Query. It is empty when the request
	// named no mode, which is the literal default — not a third mode — and
	// fileread.ModeRegex when the request asked for the query to be compiled as
	// an RE2 regular expression. Any other value is a malformed call: a plugin
	// passes it through and fileread refuses it rather than reading it as the
	// default, so a request that asked for a mode the search does not have can
	// never be answered as though it had asked for literal text.
	Mode string `json:"mode"`
	// MaxEntries and MaxLineBytes carry the listing caps: at most that many
	// entries are rendered, and one rendered line is at most that many bytes.
	// A plugin that is sent no cap falls back to its own default rather than
	// rendering an unbounded listing, and it states the cap it hit in the
	// result instead of cutting the list silently. MaxLineBytes bounds one
	// rendered line of a search as well.
	MaxEntries   int `json:"max_entries"`
	MaxLineBytes int `json:"max_line_bytes"`
	// MaxMatches, MaxFiles and MaxFileBytes carry the search caps: at most that
	// many matching lines are rendered, at most that many files are read, and no
	// file larger than that many bytes is read at all. A plugin sent no cap
	// falls back to its own default, and it states the cap that stopped it
	// rather than returning a prefix of the answer silently.
	MaxMatches   int `json:"max_matches"`
	MaxFiles     int `json:"max_files"`
	MaxFileBytes int `json:"max_file_bytes"`
	// Pattern carries the name pattern of a name search. It is a glob over one
	// entry name and nothing else — `*`, `?` and character classes are the only
	// things interpreted — and the plugin passes it to fileread.Find, which
	// matches it against a name rather than a path.
	Pattern string `json:"pattern"`
	// MaxPaths and MaxScanned carry the name-search caps: at most that many
	// matching paths are rendered, and at most that many directory entries are
	// examined. A plugin sent no cap falls back to its own default, and it
	// states the cap that stopped it rather than returning a prefix of the
	// answer silently.
	MaxPaths   int `json:"max_paths"`
	MaxScanned int `json:"max_scanned"`
	// DelayMS makes a candidate take a known amount of time before it answers.
	// It exists for the replacement tests, which have to keep one call in flight
	// while a reload publishes the next generation: without a delay the call is
	// over before there is anything to pin. MaxDelayMS is the largest value a
	// candidate accepts, and it is deliberately generous — the window has to
	// survive a machine running the whole test suite in parallel under the race
	// detector, where starting and hand-shaking a candidate has been measured at
	// over three seconds. A window sized for an idle machine is a test that fails
	// on a busy one, for a reason that has nothing to do with what it tests.
	DelayMS int `json:"delay_ms"`
}

// MaxDelayMS bounds Input.DelayMS. It is stated once, here, because both the
// host and every candidate enforce it: two copies of a bound are two bounds that
// can drift apart, and the plugin that enforced the smaller one would refuse a
// call the host thought was fine.
const MaxDelayMS = 30000

type Metadata struct {
	Version  string
	PID      int
	Protocol int
}
type Tool interface {
	Metadata() (Metadata, error)
	Invoke(Input) (string, error)
}

type RPCClient struct{ Client *rpc.Client }

func (c *RPCClient) Metadata() (out Metadata, err error) {
	err = c.Client.Call("Plugin.Metadata", struct{}{}, &out)
	return
}
func (c *RPCClient) Invoke(in Input) (out string, err error) {
	err = c.Client.Call("Plugin.Invoke", in, &out)
	return
}

type RPCServer struct{ Impl Tool }

func (s *RPCServer) Metadata(_ struct{}, out *Metadata) error {
	v, err := s.Impl.Metadata()
	*out = v
	return err
}
func (s *RPCServer) Invoke(in Input, out *string) error {
	v, err := s.Impl.Invoke(in)
	*out = v
	return err
}

type ToolPlugin struct{ Impl Tool }

func (p *ToolPlugin) Server(*plugin.MuxBroker) (interface{}, error) {
	return &RPCServer{Impl: p.Impl}, nil
}
func (p *ToolPlugin) Client(_ *plugin.MuxBroker, c *rpc.Client) (interface{}, error) {
	return &RPCClient{Client: c}, nil
}
