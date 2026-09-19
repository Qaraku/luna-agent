package pluginprotocol

import (
	"github.com/hashicorp/go-plugin"
	"net/rpc"
)

var Handshake = plugin.HandshakeConfig{ProtocolVersion: 1, MagicCookieKey: "LUNA_PLUGIN", MagicCookieValue: "luna-core-v1"}

type Input struct {
	Text    string `json:"text"`
	DelayMS int    `json:"delay_ms"`
}
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
