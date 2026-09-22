package units

import (
	"fmt"
	"strings"

	"github.com/m-d-brown/cascade/work"
	"github.com/m-d-brown/cascade/world"
)

// SecretSource fetches secrets by reference. A source that can fetch
// several in one call is why [Secrets] exists: every secret from one source
// can be folded into a single fetch, which for a vault that asks for a
// fingerprint means one prompt rather than one per secret.
type SecretSource interface {
	// ID identifies the source, for a message naming where a secret failed
	// to come from.
	ID() string
	// Read returns a value per reference. It is given every reference the
	// caller asked for in one call.
	Read(ctx *work.Context, w world.World, refs []string) (map[string]string, error)
}

// Secret fetches one secret and returns it. It is marked [work.Secret], so
// it is never written to the state journal, which means there is no last
// run to return on [work.Options.Continue], and it always runs again.
//
//	token, err := units.Secret(ctx, w, vault, "publish-token", "op://Private/widget/token")
func Secret(ctx *work.Context, w world.World, source SecretSource, name, ref string) (string, error) {
	return work.Do(ctx, name, func(ctx *work.Context) (string, error) {
		values, err := source.Read(ctx, w, []string{ref})
		if err != nil {
			return "", err
		}
		value, ok := values[ref]
		if !ok || value == "" {
			return "", fmt.Errorf("%s returned nothing for %s", source.ID(), ref)
		}
		ctx.Summarize("read %s", ref)
		return value, nil
	}, work.Secret())
}

// Secrets fetches several secrets from one source in a single call: the
// reason to call it rather than [Secret] once per reference is that a vault that
// asks for a fingerprint is asked once, not once per secret. refs maps the
// key a caller wants a value back under to the source's own reference.
//
//	values, err := units.Secrets(ctx, w, vault, "secrets", map[string]string{
//	    "restic-password": "op://Private/restic/password",
//	    "influx-token":    "op://Private/influxdb/token",
//	})
//	password := values["restic-password"]
//
// Like [Secret], it is marked [work.Secret] and always runs again.
func Secrets(ctx *work.Context, w world.World, source SecretSource, name string, refs map[string]string) (map[string]string, error) {
	return work.Do(ctx, name, func(ctx *work.Context) (map[string]string, error) {
		keys := make([]string, 0, len(refs))
		for _, ref := range refs {
			keys = append(keys, ref)
		}
		values, err := source.Read(ctx, w, keys)
		if err != nil {
			return nil, err
		}
		out := make(map[string]string, len(refs))
		for key, ref := range refs {
			v, ok := values[ref]
			if !ok || v == "" {
				return nil, fmt.Errorf("%s returned nothing for %s", source.ID(), ref)
			}
			out[key] = v
		}
		ctx.Summarize("read %d secrets from %s in one call", len(refs), source.ID())
		return out, nil
	}, work.Secret())
}

// OnePassword reads secrets with the `op` command line tool, in one call
// when there is more than one to read.
type OnePassword struct {
	// Bin is the op binary; defaults to "op".
	Bin string
}

// ID implements [SecretSource].
func (o OnePassword) ID() string { return "1password" }

func (o OnePassword) bin() string {
	if o.Bin != "" {
		return o.Bin
	}
	return "op"
}

// Read implements [SecretSource].
func (o OnePassword) Read(ctx *work.Context, w world.World, refs []string) (map[string]string, error) {
	if len(refs) == 1 {
		res, err := Run(ctx, w, Cmd{
			Path:    o.bin(),
			Args:    []string{"read", refs[0]},
			Quiet:   true,
			Instead: []string{"s3cret-" + shortRef(refs[0])},
		})
		if err != nil {
			return nil, err
		}
		value := strings.TrimSpace(res.Output())
		if value == "" {
			return nil, fmt.Errorf("op read %s returned nothing", refs[0])
		}
		return map[string]string{refs[0]: value}, nil
	}

	// One call for all of them: feed op a template and let it substitute.
	var template strings.Builder
	instead := make([]string, 0, len(refs))
	for i, ref := range refs {
		fmt.Fprintf(&template, "%d=%s\n", i, ref)
		instead = append(instead, fmt.Sprintf("%d=s3cret-%s", i, shortRef(ref)))
	}
	res, err := Run(ctx, w, Cmd{
		Path:    o.bin(),
		Args:    []string{"inject"},
		Stdin:   strings.NewReader(template.String()),
		Quiet:   true,
		Instead: instead,
	})
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(refs))
	for _, line := range res.Lines {
		idx, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		var i int
		if _, err := fmt.Sscanf(idx, "%d", &i); err != nil || i < 0 || i >= len(refs) {
			continue
		}
		out[refs[i]] = value
	}
	return out, nil
}

// shortRef is the last element of a reference, for readable fake output.
func shortRef(ref string) string {
	s := ref
	if i := strings.LastIndexAny(s, "/:"); i >= 0 {
		s = s[i+1:]
	}
	return strings.ToLower(strings.ReplaceAll(s, "_", "-"))
}
