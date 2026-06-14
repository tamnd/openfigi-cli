package openfigi

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/tamnd/any-cli/kit"
	"github.com/tamnd/any-cli/kit/errs"
)

// domain.go exposes openfigi as a kit Domain: a driver that a multi-domain
// host (ant) enables with a single blank import,
//
//	import _ "github.com/tamnd/openfigi-cli/openfigi"
//
// exactly as a database/sql program enables a driver with `import _
// "github.com/lib/pq"`. The init below registers it; the host then
// dereferences openfigi:// URIs by routing to the operations Register installs.
// The same Domain also builds the standalone openfigi binary (see cli.NewApp),
// so the binary and a host share one source of truth.
func init() { kit.Register(Domain{}) }

// Domain is the openfigi driver. It carries no state; the per-run client is
// built by the factory Register hands kit.
type Domain struct{}

// Info describes the scheme, the hostnames a pasted link is matched against,
// and the identity reused for the binary's help and version.
func (Domain) Info() kit.DomainInfo {
	return kit.DomainInfo{
		Scheme: "openfigi",
		Hosts:  []string{Host},
		Identity: kit.Identity{
			Binary: "openfigi",
			Short:  "A command line for the OpenFIGI financial instrument identifier API.",
			Long: `A command line for the OpenFIGI financial instrument identifier API.

openfigi maps identifiers (TICKER, ISIN, CUSIP, SEDOL, FIGI) to Bloomberg
FIGI codes and searches the OpenFIGI database. No API key required for
basic use; a key unlocks higher rate limits.`,
			Site: Host,
			Repo: "https://github.com/tamnd/openfigi-cli",
		},
	}
}

// Register installs the client factory and every operation onto app.
func (Domain) Register(app *kit.App) {
	app.SetClient(newClient)

	// map: resolve an identifier to FIGI records.
	kit.Handle(app, kit.OpMeta{
		Name:    "map",
		Group:   "read",
		List:    true,
		Summary: "Map an identifier to FIGI records",
		URIType: "instrument",
		Args:    []kit.Arg{{Name: "value", Help: "identifier value (e.g. AAPL, US4592001014)"}},
	}, mapOp)

	// search: search for instruments by query string.
	kit.Handle(app, kit.OpMeta{
		Name:    "search",
		Group:   "read",
		List:    true,
		Summary: "Search for financial instruments",
		URIType: "instrument",
		Args:    []kit.Arg{{Name: "query", Help: "free-text search query"}},
	}, searchOp)
}

// newClient builds the Client from the host-resolved kit.Config.
func newClient(_ context.Context, cfg kit.Config) (any, error) {
	c := NewClient()
	if cfg.UserAgent != "" {
		c.UserAgent = cfg.UserAgent
	}
	if cfg.Rate > 0 {
		c.Rate = cfg.Rate
	}
	if cfg.Retries > 0 {
		c.Retries = cfg.Retries
	}
	if cfg.Timeout > 0 {
		c.HTTP.Timeout = cfg.Timeout
	}
	return c, nil
}

// --- inputs ---

type mapInput struct {
	Value    string  `kit:"arg" help:"identifier value (e.g. AAPL, US4592001014)"`
	Type     string  `kit:"flag" help:"identifier type (TICKER, ID_ISIN, ID_CUSIP, ID_SEDOL, ID_FIGI, ID_COMPOSITE_FIGI)" name:"type"`
	Exchange string  `kit:"flag" help:"exchange code filter (e.g. US, LN)" name:"exchange"`
	Client   *Client `kit:"inject"`
}

type searchInput struct {
	Query    string  `kit:"arg" help:"free-text search query"`
	Exchange string  `kit:"flag" help:"exchange code filter (e.g. US, LN)" name:"exchange"`
	Limit    int     `kit:"flag,inherit" help:"max results"`
	Client   *Client `kit:"inject"`
}

// --- handlers ---

func mapOp(ctx context.Context, in mapInput, emit func(*Instrument) error) error {
	idType := in.Type
	if idType == "" {
		idType = "TICKER"
	}
	instruments, err := in.Client.Map(ctx, idType, in.Value, in.Exchange)
	if err != nil {
		return mapErr(err)
	}
	for i := range instruments {
		if err := emit(&instruments[i]); err != nil {
			return err
		}
	}
	return nil
}

func searchOp(ctx context.Context, in searchInput, emit func(*Instrument) error) error {
	limit := in.Limit
	if limit <= 0 {
		limit = 10
	}
	instruments, err := in.Client.Search(ctx, in.Query, in.Exchange, limit)
	if err != nil {
		return mapErr(err)
	}
	for i := range instruments {
		if err := emit(&instruments[i]); err != nil {
			return err
		}
	}
	return nil
}

// --- Resolver: pure string functions, no network ---

// isinRE matches a 12-character ISIN-like string: 2 letters + 9 alphanumerics + 1 digit.
var isinRE = regexp.MustCompile(`^[A-Z]{2}[A-Z0-9]{9}[0-9]$`)

// Classify turns any accepted input into (uriType, id).
//   - Starts with "BBG" -> ("figi", input)
//   - Matches ISIN pattern -> ("isin", input)
//   - Else -> ("query", input)
func (Domain) Classify(input string) (uriType, id string, err error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", "", errs.Usage("empty OpenFIGI reference")
	}
	upper := strings.ToUpper(input)
	switch {
	case strings.HasPrefix(upper, "BBG"):
		return "figi", input, nil
	case isinRE.MatchString(upper):
		return "isin", input, nil
	default:
		return "query", input, nil
	}
}

// Locate returns the live https URL for a (uriType, id).
func (Domain) Locate(uriType, id string) (string, error) {
	switch uriType {
	case "figi":
		return fmt.Sprintf("https://www.openfigi.com/id/%s", id), nil
	case "isin", "query":
		return fmt.Sprintf("https://www.openfigi.com/search#!?q=%s", id), nil
	default:
		return "", errs.Usage("openfigi has no resource type %q", uriType)
	}
}

// mapErr converts a library error into the appropriate kit error.
func mapErr(err error) error {
	return err
}
