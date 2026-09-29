package web

import (
	"context"
	"fmt"
	"github.com/Qaraku/luna-agent/internal/netpolicy"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"
)

const (
	// MaxBodyBytes 是一次抓取最多读进的原始响应体。比它更长的响应不是被拒绝，而是只读
	// 前一段：结果会把“读过多少、还有多少没读”如实报出来，因为一个 2 MB 的文档页在直接
	// 拒绝下模型一个字也拿不到。
	MaxBodyBytes = 1 << 20

	// MaxTextBytes 是单次返回最多带上的提取文本。触到它的结果会说清是哪条上限、这次
	// 给了哪一段，以及下一次该用哪个 start_offset 接着读。
	MaxTextBytes = 16 << 10

	// MaxRedirects 是一次调用允许跟随的重定向上限。跟随的每一跳都会重新过一遍主机名
	// 检查与拨号层检查，所以“跟到哪儿去了”不会绕过地址策略。
	MaxRedirects = 5

	// FetchTimeout 是一次抓取的总上限：从发出请求到读完响应体为止，超时的那次调用会
	// 说清这个秒数，不以“抓到了”收场。
	FetchTimeout = 20 * time.Second

	// userAgent 是请求头里的自称。
	userAgent = "luna-agent"

	// acceptHeader 优先要 text/html：这项能力读的是正文，不是随便什么字节。
	acceptHeader = "text/html,application/xhtml+xml,application/xml;q=0.9,text/plain;q=0.8,*/*;q=0.1"
)

// addrPolicy 判定一个地址能不能被连上：nil 表示可以，非 nil 是拒绝的理由（一句可以直接
// 给模型的话）。两层检查用的是同一个函数类型，产品的取值是同一个函数实例。
type addrPolicy func(ip net.IP) error

// proxyFunc 决定一个请求是否经过代理。产品的取值是 http.ProxyFromEnvironment，也就是
// http.Transport 自己用的那个函数；它可以被替换，因为那个函数在进程内只读一次环境变量
// （sync.Once），测试改不了它的结论。
type proxyFunc func(*http.Request) (*url.URL, error)

// fetchRules 是一次抓取遵守的整套规则。产品只有一份（productRules）；测试通过
// newFetchTool 注入别的版本，好让 httptest 的 loopback 地址能被真正取回——产品那份对着
// 它会（正确地）拒绝。
type fetchRules struct {
	// addresses 是请求前的主机名策略：解析主机名，逐个判定它解析出的地址。主机名本身就是
	// IP 字面量时直接判定。重定向的每一跳也会再过一遍这条。
	addresses addrPolicy
	// dial 是拨号时的策略：net.Dialer.Control 拿到的是解析后的 IP，所以这条不看名字，
	// 也就躲不开“名字先解析成公开地址、连的时候再指向内网”这种把戏。
	// 产品与 addresses 是同一个函数；测试可以把两者分开，好分别钉住每一层。
	dial addrPolicy
	// proxy 同时是 http.Client 的 Transport.Proxy 和工具判断“这次请求会不会走代理”的
	// 依据，两处必须是同一个函数：走了代理时拨号面对的是代理的地址（通常就是
	// 127.0.0.1），而不是目标的地址，检查代理地址没有意义。
	proxy proxyFunc
	// timeout 是一次抓取的总上限，它同时是请求的 deadline 与拨号的上限。
	timeout time.Duration
}

// productRules 是产品使用的规则：两层都只放行公开地址，代理判定看环境变量，一次抓取
// 最多 FetchTimeout。
var productRules = fetchRules{
	addresses: publicAddresses,
	dial:      publicAddresses,
	proxy:     http.ProxyFromEnvironment,
	timeout:   FetchTimeout,
}

// publicAddresses 是产品使用的地址策略：只有公开地址可以连。
func publicAddresses(ip net.IP) error {
	if reason := blockedAddress(ip); reason != "" {
		return refuse("the address %s is %s, and only public addresses can be fetched", ip, reason)
	}
	return nil
}

// blockedAddress 说出一个地址为什么不算公开，公开地址返回空串。它是“哪些地址不算公开”
// 的唯一一处实现：请求前的检查与拨号时的检查都从这里出发，两层不可能判得不一样。
//
// 类别是这张清单：未指定、loopback、RFC1918 私有段与 IPv6 ULA（net.IP.IsPrivate 覆盖
// 两者）、link-local、组播，加上两种“把别的地址装在里面的 IPv6 前缀”——6to4
// （2002::/16）与 NAT64（64:ff9b::/96）。后两类是补上的漏洞：它们各自把一个 IPv4 地址
// 装在 v6 里，在有相应路由的网络上照样回到本机或局域网，而 IsLoopback、IsPrivate 都覆盖
// 不到它们。IPv4 与 IPv6 走同一份判断，写成 IPv6 的 IPv4 地址（::ffff:127.0.0.1）由
// net.IP 自己折算，不会漏。
func blockedAddress(ip net.IP) string { return netpolicy.BlockedReason(ip) }

// checkHost 是请求前的那层检查：主机名是 IP 字面量就直接判，否则解析之后逐个地址判，
// 只要有一个地址不允许就拒绝整次抓取。解析失败、解析不出地址也拒绝——一个名字此刻指不到
// 公开地址，就不能把请求交出去。
func checkHost(ctx context.Context, policy addrPolicy, host string) error {
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	if host == "" {
		return refuse("the address names no host, so there is nothing to check or fetch")
	}
	if ip := net.ParseIP(host); ip != nil {
		if err := policy(ip); err != nil {
			return fmt.Errorf("the host %q: %w", host, err)
		}
		return nil
	}
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return refuse("the host %q cannot be resolved, so this call is refused", host)
	}
	if len(addrs) == 0 {
		return refuse("the host %q resolves to no address at all, so this call is refused", host)
	}
	for _, addr := range addrs {
		if err := policy(addr.IP); err != nil {
			return fmt.Errorf("the host %q resolves to %s: %w", host, addr.IP, err)
		}
	}
	return nil
}

// checkDialAddress 是拨号时的那层检查。net.Dialer.Control 收到的 address 是解析后的
// "ip:port"，所以这里判的是真正要连上去的地址：DNS 解析陷阱（公开的名字指向内网）与
// DNS 重绑定（解析两次给出不同答案）都在连接建立之前被拦下。
func checkDialAddress(policy addrPolicy, address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("the address being dialled (%q) cannot be read: %s", address, err)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("the address being dialled (%q) is not an IP address", address)
	}
	return policy(ip)
}

// proxiedKey 是放进请求 context 里的标记：这一跳会不会走代理。
//
// http.Transport 用 Proxy 选路，而 net.Dialer.Control 只拿得到地址、拿不到请求，所以
// “走不走代理”只能由工具在发请求之前算出来，跟着 context 带到拨号那一刻（Transport
// 拨号时保留请求 context 的值）。重定向的每一跳在 CheckRedirect 里重算一次。
type proxiedKey struct{}

// markProxy 把这次请求会不会走代理记进它自己的 context。
func markProxy(req *http.Request, proxy proxyFunc) *http.Request {
	target, err := proxy(req)
	return req.WithContext(context.WithValue(req.Context(), proxiedKey{}, err == nil && target != nil))
}

// proxied 报告这次拨号所属的请求是否会走代理。
func proxied(ctx context.Context) bool {
	throughProxy, _ := ctx.Value(proxiedKey{}).(bool)
	return throughProxy
}

// refusal 是一次抓取没有完成、而理由已经是一句可以直接给模型的拒绝：上限到了、地址不
// 允许、响应不是文本。它带着整句往上走，不再被包成“抓取失败：……”。
type refusal struct{ reason string }

func (r *refusal) Error() string { return r.reason }

// refuse 造一条这样整句的拒绝。
func refuse(format string, args ...any) *refusal {
	return &refusal{reason: fmt.Sprintf(format, args...)}
}

// newClient 造这次抓取用的 http.Client。它是规则的一处投影：选路用 rules.proxy，拨号
// 用 rules.dial，重定向的每一跳重新判一次地址并重新记一次代理标记，超过 MaxRedirects
// 就停。
func newClient(rules fetchRules) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy: rules.proxy,
			DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
				dialer := &net.Dialer{Timeout: rules.timeout}
				// 走代理时这一层跳过：此时拨的是代理的地址（通常就是 127.0.0.1），判它
				// 没有意义——真正要去的目标地址由代理去解析和连接。请求前的主机名检查
				// 照样执行，所以目标地址的策略一点没放松；代理定下来的出口地址不在这
				// 项能力能管的范围内，这一点写在注释里，也在测试里钉住。
				if !proxied(ctx) {
					dialer.Control = func(_, address string, _ syscall.RawConn) error {
						return checkDialAddress(rules.dial, address)
					}
				}
				return dialer.DialContext(ctx, network, address)
			},
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > MaxRedirects {
				return refuse("the address redirected more than %d times, which is this call's limit, so the fetch was stopped", MaxRedirects)
			}
			// 每一跳都重新判一次主机名：重定向会把这次抓取带到另一个地址，而请求前的
			// 那次检查只覆盖最初那个地址。
			if err := checkHost(req.Context(), rules.addresses, req.URL.Hostname()); err != nil {
				return refuse("the address redirected to %q, and that address cannot be fetched: %s", req.URL, err)
			}
			*req = *markProxy(req, rules.proxy)
			return nil
		},
	}
}
