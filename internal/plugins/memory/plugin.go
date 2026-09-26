package memory

import (
	"github.com/Qaraku/luna-agent/internal/plugin"
)

const (
	// PluginID is the registry id of the official Memory plugin.
	PluginID = "memory"
	// PluginTitle is the plugin's human-readable title.
	PluginTitle = "Memory"

	// FactsContextID is the id of the context contribution that carries the
	// stored facts into a run.
	FactsContextID = "facts"

	// FactsBudgetBytes is the byte budget the plugin asks the Kernel to reserve
	// for the facts block. The plugin's own cap is MaxInjectBytes (8 KiB, the
	// same number the agent's memory injection used); the remaining 1 KiB is
	// headroom for the block's title line plus the annotation the Kernel adds to
	// every reference contribution. The Kernel owns the actual budget — this is
	// what the plugin expects, not a limit it imposes.
	FactsBudgetBytes = 9 * 1024

	// MemoryRoutePath answers the user's view of the store: the facts in effect
	// and the ones that were retracted.
	MemoryRoutePath = "/api/memory"
	// RetractRoutePath retracts one fact, named by its text and its timestamp.
	RetractRoutePath = "/api/memory/retract"

	// stateNamespace is the state directory the plugin writes into. It is the
	// repository's existing convention: the default state root is the repository
	// root and the plugin's own file inside it keeps the name memory.jsonl, so
	// the default data path does not change and no migration is needed.
	stateNamespace = ".runtime"
)

// Plugin is the official Memory contribution: one write-only tool, one injected
// context block and the user's two routes over the same store.
//
// The store is the plugin's own state, not something the Kernel manages: the
// Kernel hands over a state directory and knows nothing about the file inside
// it, and it sees only a tool, a context block and two routes — never a fact, a
// retraction or a rendering rule. Being built in is a deployment choice, not a
// privilege: this plugin declares the same claims and asks for the same
// permissions any other built-in plugin would.
type Plugin struct {
	store *Store
	tool  *RememberTool
}

// New opens the durable fact file at path and wires the plugin to it. Only
// Open's failure (an empty path, an unusable directory) can fail here: the file
// itself is created by the first write.
func New(path string) (*Plugin, error) {
	store, err := Open(path)
	if err != nil {
		return nil, err
	}
	return &Plugin{store: store, tool: NewRememberTool(store)}, nil
}

// Descriptor declares exactly what the plugin exposes. The registry checks both
// directions, so this has to stay in step with Tools, Contexts and Routes: a
// declared tool or route that is never exposed fails registration.
func (p *Plugin) Descriptor() plugin.Descriptor {
	return plugin.Descriptor{
		ID:         PluginID,
		Title:      PluginTitle,
		Deployment: plugin.DeploymentBuiltin,
		Contributions: []plugin.Contribution{
			{Kind: plugin.ContributionTool, ID: RememberToolName},
			{Kind: plugin.ContributionContext, ID: FactsContextID, BudgetBytes: FactsBudgetBytes},
			{Kind: plugin.ContributionRoute, ID: MemoryRoutePath},
			{Kind: plugin.ContributionRoute, ID: RetractRoutePath},
		},
		Claims: []plugin.Claim{
			{Kind: plugin.ClaimRoutePrefix, ID: MemoryRoutePath},
			{Kind: plugin.ClaimStateNamespace, ID: stateNamespace},
		},
		Permissions: []plugin.Permission{
			// The plugin writes the fact file under the state root; Detail stays
			// empty, which is the only form the registry accepts today.
			{Kind: plugin.PermissionStateWrite},
		},
	}
}

// Tools returns the model-visible surface: exactly luna_remember, append only.
func (p *Plugin) Tools() []plugin.Tool {
	return []plugin.Tool{p.tool}
}

// Routes returns the two HTTP entries. The Kernel owns the Host and Origin
// checks and the method matching; each route only serves its own method.
func (p *Plugin) Routes() []plugin.Route {
	return []plugin.Route{
		factsRoute{store: p.store},
		retractRoute{store: p.store},
	}
}

// The three provider interfaces the descriptor declares. Contexts renders the
// injected facts block and lives in context.go; the compile-time assertions make
// a drift between the descriptor and the implementation impossible to miss.
var (
	_ plugin.ToolProvider    = (*Plugin)(nil)
	_ plugin.ContextProvider = (*Plugin)(nil)
	_ plugin.RouteProvider   = (*Plugin)(nil)
)
