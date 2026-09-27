package pluginprotocol

import (
	"github.com/hashicorp/go-plugin"
	"net/rpc"
)

var Handshake = plugin.HandshakeConfig{ProtocolVersion: 1, MagicCookieKey: "LUNA_PLUGIN", MagicCookieValue: "luna-core-v1"}

// Input is the plugin RPC request. Text/DelayMS serve the text-transform
// plugin; Path/MaxBytes serve the file-read plugin, where Path is an absolute
// path the host has already validated against the read root. Path/MaxEntries/
// MaxLineBytes serve the directory-listing plugin, where Path is the same kind
// of validated absolute path — a directory — and the two caps bound how much of
// it one listing renders. Path/Query/MaxMatches/MaxFiles/MaxFileBytes serve the
// search plugin, where Path is a validated absolute file or directory, Query is
// the literal to look for, MaxLineBytes bounds one rendered line and the other
// three bound how many matches are rendered, how many files are read and how
// large a file may be before it is skipped. A plugin never receives a model- or
// browser-supplied path in Path.
type Input struct {
	Text     string `json:"text"`
	Path     string `json:"path"`
	MaxBytes int    `json:"max_bytes"`
	// Query is the literal a search looks for. It is data, not a pattern: the
	// plugin passes it to fileread.Search, which matches it with a substring
	// test, so nothing in it is interpreted.
	Query string `json:"query"`
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
